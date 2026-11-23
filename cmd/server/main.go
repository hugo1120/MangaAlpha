package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/xJogger/fake-komga-115/internal/app"
	"github.com/xJogger/fake-komga-115/internal/buildinfo"
	"github.com/xJogger/fake-komga-115/internal/diagnostics"
	"github.com/xJogger/fake-komga-115/internal/httpserver"
)

func main() {
	defaultDir := defaultDataDir(runtime.GOOS, os.Getenv, os.UserConfigDir)
	var (
		host        = flag.String("host", envOr("FK115_HOST", "0.0.0.0"), "listen host")
		port        = flag.Int("port", envInt("FK115_PORT", 25600), "listen port")
		dataDir     = flag.String("data-dir", envOr("FK115_DATA_DIR", defaultDir), "data directory")
		openBrowser = flag.Bool(
			"open-browser",
			envBool("FK115_OPEN_BROWSER", runtime.GOOS == "windows"),
			"open the web bookshelf after the server starts",
		)
		noTray = flag.Bool(
			"no-tray",
			envBool("FK115_NO_TRAY", false),
			"disable the Windows system tray icon and run headless",
		)
		trustedProxies = flag.String(
			"trusted-proxies",
			envOr("FK115_TRUSTED_PROXIES", ""),
			"comma-separated trusted reverse proxy IPs/CIDRs (default: loopback and private networks; use 'none' to disable)",
		)
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()
	if *showVersion {
		printVersionForPlatform(buildinfo.Version)
		return
	}

	trayMode := isWindowsTrayEnabled(*noTray)
	logger, logPath, closeLogger := newServerLogger(*dataDir, trayMode)
	defer closeLogger()

	parentCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	shelfURL := localAdminURL(*host, *port)
	adminURL := strings.TrimRight(shelfURL, "/") + "/admin"

	a, err := app.New(ctx, app.Options{
		Host:                 *host,
		Port:                 *port,
		DataDir:              *dataDir,
		Logger:               logger,
		TrustedProxies:       *trustedProxies,
		InitialAdminUsername: os.Getenv("FK115_ADMIN_USERNAME"),
		InitialAdminPassword: os.Getenv("FK115_ADMIN_PASSWORD"),
	})
	if err != nil {
		diagnostics.WriteStartupFailure(*dataDir, "server", "startup", err)
		logger.Error("initialize application", "error", err)
		if trayMode {
			showStartupErrorDialog(err, *port, shelfURL, logPath)
		}
		os.Exit(1)
	}
	defer a.Close()

	errCh, err := a.Start()
	if err != nil {
		diagnostics.WriteStartupFailure(*dataDir, "server", "startup", err)
		logger.Error("start server", "error", err)
		_ = a.Close()
		if trayMode {
			showStartupErrorDialog(err, *port, shelfURL, logPath)
		}
		os.Exit(1)
	}
	if *openBrowser {
		go func() {
			time.Sleep(250 * time.Millisecond)
			if err := openURL(runtime.GOOS, shelfURL); err != nil {
				logger.Warn("open browser", "error", err, "url", shelfURL)
			}
		}()
	}

	var runErr error
	if trayMode {
		runErr = runPlatformLoop(ctx, cancel, errCh, trayConfig{
			Version:  buildinfo.Version,
			Port:     *port,
			ShelfURL: shelfURL,
			AdminURL: adminURL,
			DataDir:  *dataDir,
			LogPath:  logPath,
			IconPNG:  httpserver.AppIconPNG(),
			Logger:   logger,
		})
	} else {
		select {
		case <-ctx.Done():
		case runErr = <-errCh:
		}
	}

	if runErr != nil {
		logger.Error("server stopped", "error", runErr)
		_ = a.Close()
		if trayMode {
			showStartupErrorDialog(runErr, *port, shelfURL, logPath)
		}
		os.Exit(1)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	if err := a.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown", "error", err)
	}
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	var value int
	if _, err := fmt.Sscanf(os.Getenv(key), "%d", &value); err == nil && value > 0 {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func defaultDataDir(
	goos string,
	getenv func(string) string,
	userConfigDir func() (string, error),
) string {
	if goos != "windows" {
		return "./data"
	}
	if base := strings.TrimSpace(getenv("LOCALAPPDATA")); base != "" {
		return filepath.Join(base, "fake-komga-115", "data")
	}
	if base, err := userConfigDir(); err == nil && strings.TrimSpace(base) != "" {
		return filepath.Join(base, "fake-komga-115", "data")
	}
	return filepath.Join(".", "data")
}

func localAdminURL(host string, port int) string {
	host = strings.TrimSpace(host)
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::", "[::]":
		host = "::1"
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/"
}

func openURL(goos, url string) error {
	name, args, err := browserCommand(goos, url)
	if err != nil {
		return err
	}
	command := exec.Command(name, args...)
	configureCommandForPlatform(command)
	if err := command.Start(); err != nil {
		return err
	}
	go func() { _ = command.Wait() }()
	return nil
}

func browserCommand(goos, url string) (string, []string, error) {
	switch goos {
	case "windows":
		return "rundll32.exe", []string{"url.dll,FileProtocolHandler", url}, nil
	case "darwin":
		return "open", []string{url}, nil
	case "linux":
		return "xdg-open", []string{url}, nil
	default:
		return "", nil, errors.New("opening a browser is not supported on " + goos)
	}
}
