//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type trayConfig struct {
	Version  string
	Port     int
	ShelfURL string
	AdminURL string
	DataDir  string
	LogPath  string
	IconPNG  []byte
	Logger   *slog.Logger
}

const (
	wmNull            = 0x0000
	wmDestroy         = 0x0002
	wmClose           = 0x0010
	wmQueryEndSession = 0x0011
	wmEndSession      = 0x0016
	wmContextMenu     = 0x007B
	wmLButtonUp       = 0x0202
	wmRButtonUp       = 0x0205
	wmApp             = 0x8000
	wmTrayCallback    = wmApp + 1

	nimAdd    = 0x00000000
	nimDelete = 0x00000002

	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004

	mfString    = 0x00000000
	mfGrayed    = 0x00000001
	mfDisabled  = 0x00000002
	mfSeparator = 0x00000800

	tpmLeftAlign   = 0x0000
	tpmRightButton = 0x0002
	tpmBottomAlign = 0x0020
	tpmNoNotify    = 0x0080
	tpmReturnCmd   = 0x0100

	mbOK              = 0x00000000
	mbYesNo           = 0x00000004
	mbIconError       = 0x00000010
	mbIconInformation = 0x00000040
	idYes             = 6

	idiApplication = 32512
	smCxSmIcon     = 49
	smCySmIcon     = 50

	attachParentProcess = ^uintptr(0) // (DWORD)-1

	cmdOpenShelf   = 1001
	cmdOpenAdmin   = 1002
	cmdOpenDataDir = 1003
	cmdOpenLog     = 1004
	cmdExit        = 1005

	maxWindowsLogBytes = 5 << 20 // 5 MiB rolling log
)

var (
	modUser32   = windows.NewLazySystemDLL("user32.dll")
	modShell32  = windows.NewLazySystemDLL("shell32.dll")
	modKernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procRegisterClassExW         = modUser32.NewProc("RegisterClassExW")
	procCreateWindowExW          = modUser32.NewProc("CreateWindowExW")
	procDestroyWindow            = modUser32.NewProc("DestroyWindow")
	procDefWindowProcW           = modUser32.NewProc("DefWindowProcW")
	procGetMessageW              = modUser32.NewProc("GetMessageW")
	procTranslateMessage         = modUser32.NewProc("TranslateMessage")
	procDispatchMessageW         = modUser32.NewProc("DispatchMessageW")
	procPostQuitMessage          = modUser32.NewProc("PostQuitMessage")
	procPostMessageW             = modUser32.NewProc("PostMessageW")
	procRegisterWindowMessageW   = modUser32.NewProc("RegisterWindowMessageW")
	procCreatePopupMenu          = modUser32.NewProc("CreatePopupMenu")
	procAppendMenuW              = modUser32.NewProc("AppendMenuW")
	procTrackPopupMenu           = modUser32.NewProc("TrackPopupMenu")
	procDestroyMenu              = modUser32.NewProc("DestroyMenu")
	procSetForegroundWindow      = modUser32.NewProc("SetForegroundWindow")
	procGetCursorPos             = modUser32.NewProc("GetCursorPos")
	procCreateIconFromResourceEx = modUser32.NewProc("CreateIconFromResourceEx")
	procLoadIconW                = modUser32.NewProc("LoadIconW")
	procDestroyIcon              = modUser32.NewProc("DestroyIcon")
	procGetSystemMetrics         = modUser32.NewProc("GetSystemMetrics")
	procMessageBoxW              = modUser32.NewProc("MessageBoxW")
	procShellNotifyIconW         = modShell32.NewProc("Shell_NotifyIconW")
	procGetModuleHandleW         = modKernel32.NewProc("GetModuleHandleW")
	procAttachConsole            = modKernel32.NewProc("AttachConsole")
)

// Exact Win32 ABI layouts for x64 Windows (verified by tray_windows_test.go).
type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type winPoint struct {
	x int32
	y int32
}

type winMsg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      winPoint
}

type notifyIconDataW struct {
	cbSize           uint32
	hWnd             uintptr
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	hIcon            uintptr
	szTip            [128]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	uVersion         uint32
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	guidItem         windows.GUID
	hBalloonIcon     uintptr
}

type windowsTrayState struct {
	cfg               trayConfig
	cancel            context.CancelFunc
	hwnd              uintptr
	hIcon             uintptr
	ownedIcon         bool
	taskbarCreatedMsg uint32
	menuOpen          bool
}

var (
	activeTrayMu    sync.RWMutex
	activeTray      *windowsTrayState
	wndProcCallback = syscall.NewCallback(trayWindowProc)
)

func isWindowsTrayEnabled(noTray bool) bool {
	return !noTray
}

// printVersionForPlatform prints version to the parent console when launched from
// PowerShell/CMD (reopening CONOUT$ because -H=windowsgui starts without a bound
// stdout handle), or shows a clean native dialog if double-clicked from Explorer.
func printVersionForPlatform(ver string) {
	ret, _, _ := procAttachConsole.Call(attachParentProcess)
	if ret != 0 {
		if conOut, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
			_, _ = fmt.Fprintln(conOut, ver)
			_ = conOut.Close()
			return
		}
	}
	showMessageBox("MangaAlpha", "MangaAlpha "+ver, mbOK|mbIconInformation)
}

func showStartupErrorDialog(err error, port int, shelfURL, logPath string) {
	if err == nil {
		return
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "only one usage of each socket address") ||
		strings.Contains(lower, "address already in use") ||
		strings.Contains(lower, "bind") {
		text := fmt.Sprintf(
			"端口 %d 已被占用，MangaAlpha 可能已在系统托盘运行。\n\n是否立即在浏览器中打开网页书架（%s）？",
			port, shelfURL,
		)
		if showMessageBox("MangaAlpha 已在运行或端口被占用", text, mbYesNo|mbIconInformation) == idYes {
			_ = openURL("windows", shelfURL)
		}
		return
	}
	text := fmt.Sprintf("MangaAlpha 启动失败：\n\n%s", msg)
	if logPath != "" {
		text += fmt.Sprintf("\n\n详情请查看日志文件：\n%s", logPath)
	}
	showMessageBox("MangaAlpha 启动失败", text, mbOK|mbIconError)
}

func showMessageBox(title, text string, flags uint32) int {
	tPtr, _ := windows.UTF16PtrFromString(title)
	mPtr, _ := windows.UTF16PtrFromString(text)
	ret, _, _ := procMessageBoxW.Call(0, uintptr(unsafe.Pointer(mPtr)), uintptr(unsafe.Pointer(tPtr)), uintptr(flags))
	return int(ret)
}

func configureCommandForPlatform(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

func openWindowsPath(target string) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return errors.New("empty path")
	}
	cmd := exec.Command("explorer.exe", target)
	configureCommandForPlatform(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func newServerLogger(dataDir string, trayMode bool) (*slog.Logger, string, func()) {
	if !trayMode || strings.TrimSpace(dataDir) == "" {
		return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})), "", func() {}
	}
	logPath := filepath.Join(dataDir, "mangaalpha.log")
	rw, err := newRollingLogWriter(logPath, maxWindowsLogBytes)
	if err != nil {
		return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})), "", func() {}
	}
	// Under -H=windowsgui, os.Stdout may hold an invalid handle; log directly to the rolling log file.
	return slog.New(slog.NewTextHandler(rw, &slog.HandlerOptions{Level: slog.LevelInfo})), logPath, rw.Close
}

func runPlatformLoop(ctx context.Context, cancel context.CancelFunc, errCh <-chan error, cfg trayConfig) error {
	var (
		runErrMu sync.Mutex
		runErr   error
	)
	setRunErr := func(err error) {
		if err == nil {
			return
		}
		runErrMu.Lock()
		if runErr == nil {
			runErr = err
		}
		runErrMu.Unlock()
	}

	trayReady := make(chan uintptr, 1)
	go func() {
		var hwnd uintptr
		select {
		case h, ok := <-trayReady:
			if !ok || h == 0 {
				return
			}
			hwnd = h
		case err := <-errCh:
			setRunErr(err)
			cancel()
			if h, ok := <-trayReady; ok && h != 0 {
				_, _, _ = procPostMessageW.Call(h, wmClose, 0, 0)
			}
			return
		case <-ctx.Done():
			if h, ok := <-trayReady; ok && h != 0 {
				_, _, _ = procPostMessageW.Call(h, wmClose, 0, 0)
			}
			return
		}
		select {
		case <-ctx.Done():
		case err := <-errCh:
			setRunErr(err)
			cancel()
		}
		_, _, _ = procPostMessageW.Call(hwnd, wmClose, 0, 0)
	}()

	if err := runWindowsTrayLoop(cancel, cfg, trayReady); err != nil {
		if cfg.Logger != nil {
			cfg.Logger.Warn("windows system tray unavailable; falling back to background wait", "error", err)
		}
		select {
		case <-ctx.Done():
		case sErr := <-errCh:
			setRunErr(sErr)
		}
	}

	runErrMu.Lock()
	err := runErr
	runErrMu.Unlock()
	return err
}

func runWindowsTrayLoop(cancel context.CancelFunc, cfg trayConfig, readyCh chan<- uintptr) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	sentReady := false
	defer func() {
		if !sentReady {
			close(readyCh)
		}
	}()

	hInstance, _, _ := procGetModuleHandleW.Call(0)
	className, err := windows.UTF16PtrFromString("MangaAlphaTrayWindowClass")
	if err != nil {
		return err
	}
	windowTitle, err := windows.UTF16PtrFromString("MangaAlpha")
	if err != nil {
		return err
	}

	hIcon, ownedIcon := loadTrayIcon(cfg.IconPNG)
	wc := wndClassExW{
		cbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		lpfnWndProc:   wndProcCallback,
		hInstance:     hInstance,
		hIcon:         hIcon,
		hIconSm:       hIcon,
		lpszClassName: className,
	}
	atom, _, regErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	if atom == 0 && !errors.Is(regErr, windows.ERROR_CLASS_ALREADY_EXISTS) {
		if ownedIcon && hIcon != 0 {
			_, _, _ = procDestroyIcon.Call(hIcon)
		}
		return fmt.Errorf("RegisterClassExW: %w", regErr)
	}

	// Use a normal hidden top-level window (hWndParent = 0, never shown) rather than
	// HWND_MESSAGE so Windows delivers TaskbarCreated and WM_ENDSESSION broadcasts.
	hwnd, _, createErr := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(windowTitle)),
		0,
		0, 0, 0, 0,
		0, 0, hInstance, 0,
	)
	if hwnd == 0 {
		if ownedIcon && hIcon != 0 {
			_, _, _ = procDestroyIcon.Call(hIcon)
		}
		return fmt.Errorf("CreateWindowExW: %w", createErr)
	}

	taskbarMsgName, _ := windows.UTF16PtrFromString("TaskbarCreated")
	tbMsg, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(taskbarMsgName)))

	state := &windowsTrayState{
		cfg:               cfg,
		cancel:            cancel,
		hwnd:              hwnd,
		hIcon:             hIcon,
		ownedIcon:         ownedIcon,
		taskbarCreatedMsg: uint32(tbMsg),
	}
	activeTrayMu.Lock()
	activeTray = state
	activeTrayMu.Unlock()
	defer func() {
		state.removeIcon()
		if state.ownedIcon && state.hIcon != 0 {
			_, _, _ = procDestroyIcon.Call(state.hIcon)
		}
		activeTrayMu.Lock()
		if activeTray == state {
			activeTray = nil
		}
		activeTrayMu.Unlock()
	}()

	if !state.addIcon() {
		_, _, _ = procDestroyWindow.Call(hwnd)
		return errors.New("Shell_NotifyIconW NIM_ADD failed")
	}
	readyCh <- hwnd
	close(readyCh)
	sentReady = true

	var msg winMsg
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		_, _, _ = procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		_, _, _ = procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
	return nil
}

func (s *windowsTrayState) buildNID() notifyIconDataW {
	var nid notifyIconDataW
	nid.cbSize = uint32(unsafe.Sizeof(nid))
	nid.hWnd = s.hwnd
	nid.uID = 1
	nid.uFlags = nifMessage | nifIcon | nifTip
	nid.uCallbackMessage = wmTrayCallback
	nid.hIcon = s.hIcon

	tip := fmt.Sprintf("MangaAlpha %s (:%d)", s.cfg.Version, s.cfg.Port)
	copyUTF16Bounded(nid.szTip[:], tip)
	return nid
}

func copyUTF16Bounded(dst []uint16, text string) {
	if len(dst) == 0 {
		return
	}
	encoded, err := windows.UTF16FromString(text)
	if err != nil {
		dst[0] = 0
		return
	}
	if len(encoded) > len(dst) {
		copy(dst[:len(dst)-1], encoded[:len(dst)-1])
		dst[len(dst)-1] = 0
		return
	}
	copy(dst, encoded)
}

func (s *windowsTrayState) addIcon() bool {
	nid := s.buildNID()
	ret, _, _ := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
	return ret != 0
}

func (s *windowsTrayState) removeIcon() {
	nid := s.buildNID()
	_, _, _ = procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
}

func (s *windowsTrayState) showContextMenu() {
	if s.menuOpen {
		return
	}
	s.menuOpen = true
	defer func() { s.menuOpen = false }()

	hMenu, _, _ := procCreatePopupMenu.Call()
	if hMenu == 0 {
		return
	}
	defer procDestroyMenu.Call(hMenu)

	appendItem := func(flags uint32, id uintptr, label string) {
		if flags&mfSeparator != 0 {
			_, _, _ = procAppendMenuW.Call(hMenu, uintptr(flags), 0, 0)
			return
		}
		ptr, err := windows.UTF16PtrFromString(label)
		if err != nil {
			return
		}
		_, _, _ = procAppendMenuW.Call(hMenu, uintptr(flags), id, uintptr(unsafe.Pointer(ptr)))
	}

	appendItem(mfString|mfGrayed|mfDisabled, 0, fmt.Sprintf("MangaAlpha %s（端口 %d）", s.cfg.Version, s.cfg.Port))
	appendItem(mfSeparator, 0, "")
	appendItem(mfString, cmdOpenShelf, "打开网页书架")
	appendItem(mfString, cmdOpenAdmin, "打开管理后台")
	appendItem(mfString, cmdOpenDataDir, "打开数据目录")
	appendItem(mfString, cmdOpenLog, "查看运行日志")
	appendItem(mfSeparator, 0, "")
	appendItem(mfString, cmdExit, "退出 MangaAlpha")

	var pt winPoint
	_, _, _ = procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	// Per Microsoft KB Q135788: SetForegroundWindow before TrackPopupMenu and
	// PostMessage(WM_NULL) after so clicking outside the menu reliably dismisses it.
	_, _, _ = procSetForegroundWindow.Call(s.hwnd)
	cmd, _, _ := procTrackPopupMenu.Call(
		hMenu,
		tpmRightButton|tpmBottomAlign|tpmLeftAlign|tpmNoNotify|tpmReturnCmd,
		uintptr(pt.x),
		uintptr(pt.y),
		0,
		s.hwnd,
		0,
	)
	_, _, _ = procPostMessageW.Call(s.hwnd, wmNull, 0, 0)

	switch cmd {
	case cmdOpenShelf:
		go func() { _ = openURL("windows", s.cfg.ShelfURL) }()
	case cmdOpenAdmin:
		go func() { _ = openURL("windows", s.cfg.AdminURL) }()
	case cmdOpenDataDir:
		go func() { _ = openWindowsPath(s.cfg.DataDir) }()
	case cmdOpenLog:
		target := s.cfg.LogPath
		if target == "" {
			target = s.cfg.DataDir
		} else if _, err := os.Stat(target); err != nil {
			target = s.cfg.DataDir
		}
		go func() { _ = openWindowsPath(target) }()
	case cmdExit:
		s.cancel()
		_, _, _ = procPostMessageW.Call(s.hwnd, wmClose, 0, 0)
	}
}

func trayWindowProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	activeTrayMu.RLock()
	st := activeTray
	activeTrayMu.RUnlock()

	if st != nil {
		if st.taskbarCreatedMsg != 0 && msg == st.taskbarCreatedMsg {
			st.addIcon()
			return 0
		}
		switch msg {
		case wmTrayCallback:
			switch uint32(lParam & 0xffff) {
			case wmLButtonUp:
				go func() { _ = openURL("windows", st.cfg.ShelfURL) }()
			case wmRButtonUp, wmContextMenu:
				st.showContextMenu()
			}
			return 0
		case wmQueryEndSession:
			return 1
		case wmEndSession:
			if wParam != 0 {
				st.cancel()
				_, _, _ = procDestroyWindow.Call(hwnd)
			}
			return 0
		case wmClose:
			_, _, _ = procDestroyWindow.Call(hwnd)
			return 0
		case wmDestroy:
			st.removeIcon()
			_, _, _ = procPostQuitMessage.Call(0)
			return 0
		}
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
	return ret
}

func loadTrayIcon(pngBytes []byte) (uintptr, bool) {
	if len(pngBytes) > 0 {
		if scaled := resizePNGForIcon(pngBytes, 64); len(scaled) > 0 {
			cx, _, _ := procGetSystemMetrics.Call(smCxSmIcon)
			cy, _, _ := procGetSystemMetrics.Call(smCySmIcon)
			if cx == 0 {
				cx = 32
			}
			if cy == 0 {
				cy = 32
			}
			hIcon, _, _ := procCreateIconFromResourceEx.Call(
				uintptr(unsafe.Pointer(&scaled[0])),
				uintptr(len(scaled)),
				1,          // fIcon = TRUE
				0x00030000, // dwVer
				cx,
				cy,
				0, // LR_DEFAULTCOLOR
			)
			if hIcon == 0 {
				hIcon, _, _ = procCreateIconFromResourceEx.Call(
					uintptr(unsafe.Pointer(&scaled[0])),
					uintptr(len(scaled)),
					1,
					0x00030000,
					0,
					0,
					0,
				)
			}
			if hIcon != 0 {
				return hIcon, true
			}
		}
	}
	fallback, _, _ := procLoadIconW.Call(0, idiApplication)
	return fallback, false
}
