package main

import (
	"bytes"
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/xJogger/fake-komga-115/internal/httpserver"
)

func TestDefaultDataDir(t *testing.T) {
	failedConfig := func() (string, error) { return "", errors.New("unavailable") }
	if got := defaultDataDir("linux", func(string) string { return "" }, failedConfig); got != "./data" {
		t.Fatalf("linux data directory = %q", got)
	}
	got := defaultDataDir(
		"windows",
		func(key string) string {
			if key == "LOCALAPPDATA" {
				return `C:\Users\reader\AppData\Local`
			}
			return ""
		},
		failedConfig,
	)
	want := filepath.Join(`C:\Users\reader\AppData\Local`, "fake-komga-115", "data")
	if got != want {
		t.Fatalf("windows data directory = %q, want %q", got, want)
	}
}

func TestLocalAdminURL(t *testing.T) {
	if got := localAdminURL("0.0.0.0", 25600); got != "http://127.0.0.1:25600/" {
		t.Fatalf("wildcard URL = %q", got)
	}
	if got := localAdminURL("::", 25600); got != "http://[::1]:25600/" {
		t.Fatalf("IPv6 URL = %q", got)
	}
}

func TestBrowserCommand(t *testing.T) {
	tests := []struct {
		goos string
		name string
		args []string
	}{
		{"windows", "rundll32.exe", []string{"url.dll,FileProtocolHandler", "http://localhost"}},
		{"darwin", "open", []string{"http://localhost"}},
		{"linux", "xdg-open", []string{"http://localhost"}},
	}
	for _, test := range tests {
		t.Run(test.goos, func(t *testing.T) {
			name, args, err := browserCommand(test.goos, "http://localhost")
			if err != nil {
				t.Fatal(err)
			}
			if name != test.name || !reflect.DeepEqual(args, test.args) {
				t.Fatalf("command = %q %#v", name, args)
			}
		})
	}
	if _, _, err := browserCommand("plan9", "http://localhost"); err == nil {
		t.Fatal("unsupported platform must return an error")
	}
}

func TestResizePNGForIcon(t *testing.T) {
	raw := httpserver.AppIconPNG()
	if len(raw) == 0 {
		t.Fatal("embedded AppIconPNG must not be empty")
	}
	scaled := resizePNGForIcon(raw, 64)
	img, err := png.Decode(bytes.NewReader(scaled))
	if err != nil {
		t.Fatalf("scaled PNG decode failed: %v", err)
	}
	b := img.Bounds()
	if b.Dx() > 64 || b.Dy() > 64 || b.Dx() <= 0 || b.Dy() <= 0 {
		t.Fatalf("unexpected scaled icon dimensions: %dx%d", b.Dx(), b.Dy())
	}
}

func TestRollingLogWriterRotation(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "mangaalpha.log")
	w, err := newRollingLogWriter(logPath, 32)
	if err != nil {
		t.Fatalf("newRollingLogWriter: %v", err)
	}
	defer w.Close()
	if _, err := w.Write([]byte("0123456789abcdef0123456789abcdef")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("rotated-entry\n")); err != nil {
		t.Fatal(err)
	}
	cur, err := os.ReadFile(logPath)
	if err != nil || string(cur) != "rotated-entry\n" {
		t.Fatalf("unexpected active log: %q err=%v", cur, err)
	}
	bak, err := os.ReadFile(logPath + ".1")
	if err != nil || len(bak) != 32 {
		t.Fatalf("unexpected backup log: len=%d err=%v", len(bak), err)
	}
}
