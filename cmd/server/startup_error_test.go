package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestIsAddressInUseError(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"typed address in use", fmt.Errorf("listen: %w", syscall.EADDRINUSE), true},
		{"typed Windows address in use", fmt.Errorf("listen: %w", syscall.Errno(10048)), true},
		{"Linux message", errors.New("listen: bind: address already in use"), true},
		{"Windows message", errors.New("bind: Only one usage of each socket address is normally permitted."), true},
		{"generic bind error", errors.New("bind failed"), false},
		{"permission message", errors.New("bind: permission denied"), false},
		{"typed permission error", fmt.Errorf("bind: %w", syscall.EACCES), false},
		{"nil", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAddressInUseError(tc.err); got != tc.want {
				t.Errorf("isAddressInUseError(%v) = %t, want %t", tc.err, got, tc.want)
			}
		})
	}
}

const testHealthResponse = `{"status":"ok","service":"mangaalpha"}`

func TestProbeMangaAlphaHealth(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"MangaAlpha", http.StatusOK, testHealthResponse, true},
		{"HTML", http.StatusOK, "<html><body>OK</body></html>", false},
		{"missing service", http.StatusOK, `{"status":"ok"}`, false},
		{"different service", http.StatusOK, `{"status":"ok","service":"other"}`, false},
		{"unhealthy", http.StatusOK, `{"status":"error","service":"mangaalpha"}`, false},
		{"redirect", http.StatusFound, testHealthResponse, false},
		{"non-200", http.StatusServiceUnavailable, testHealthResponse, false},
		{"oversized", http.StatusOK, testHealthResponse + strings.Repeat(" ", 4096), false},
		{"trailing data", http.StatusOK, testHealthResponse + "{}", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/redirected" {
					t.Error("probe followed the redirect")
					_, _ = io.WriteString(w, testHealthResponse)
					return
				}
				if r.Method != http.MethodGet || r.URL.Path != "/health" {
					t.Errorf("unexpected probe request %s %s", r.Method, r.URL.Path)
				}
				if tc.status == http.StatusFound {
					w.Header().Set("Location", "/redirected")
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			if got := probeMangaAlphaHealth(t.Context(), server.URL+"///", server.Client()); got != tc.want {
				t.Errorf("probeMangaAlphaHealth() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestProbeMangaAlphaHealthDirect(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, testHealthResponse)
	}))
	defer server.Close()
	transport := server.Client().Transport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	transport.Proxy = func(*http.Request) (*url.URL, error) {
		return nil, errors.New("proxy is unavailable")
	}
	client := &http.Client{Transport: transport}
	if !probeMangaAlphaHealth(t.Context(), server.URL, client) {
		t.Fatal("direct probe failed")
	}
	if _, err := transport.Proxy(nil); err == nil {
		t.Fatal("probe modified the supplied transport")
	}
}

func TestProbeMangaAlphaHealthTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
			_, _ = io.WriteString(w, testHealthResponse)
		}
	}))
	defer server.Close()
	if probeMangaAlphaHealth(t.Context(), server.URL, nil) {
		t.Fatal("probe accepted a slow response")
	}
}

func TestSuggestedFallbackPort(t *testing.T) {
	for port, want := range map[int]int{-1: 25601, 0: 25601, 1: 2, 25600: 25601, 65534: 65535, 65535: 25601, 65536: 25601} {
		if got := suggestedFallbackPort(port); got != want {
			t.Errorf("suggestedFallbackPort(%d) = %d, want %d", port, got, want)
		}
	}
}

func TestFormatPortConflictDialog(t *testing.T) {
	const shelfURL = "http://localhost:25600/"
	title, message, offerOpen := formatPortConflictDialog(true, 25600, shelfURL)
	want := "端口 25600 已有 MangaAlpha 实例在后台运行。\n\n是否立即在浏览器中打开网页书架（" + shelfURL + "）？"
	if !offerOpen || title != "MangaAlpha 已在运行" || message != want {
		t.Errorf("MangaAlpha dialog = (%q, %q, %t)", title, message, offerOpen)
	}

	title, message, offerOpen = formatPortConflictDialog(false, 25600, shelfURL)
	if offerOpen || title != "端口 25600 已被占用" || strings.Contains(message, shelfURL) {
		t.Errorf("other program dialog = (%q, %q, %t)", title, message, offerOpen)
	}
	for _, phrase := range []string{"端口 25600 已被其他程序或旧版实例占用", "MangaAlpha 无法启动", "关闭占用端口的程序", "--port 25601", "FK115_PORT=25601"} {
		if !strings.Contains(message, phrase) {
			t.Errorf("other program dialog is missing %q", phrase)
		}
	}
}
