package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"syscall"
	"time"
)

func isAddressInUseError(err error) bool {
	if err == nil {
		return false
	}
	const wsaAddressInUse = syscall.Errno(10048)
	if errors.Is(err, syscall.EADDRINUSE) || errors.Is(err, wsaAddressInUse) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "address already in use") ||
		strings.Contains(message, "only one usage of each socket address")
}

func probeMangaAlphaHealth(ctx context.Context, baseURL string, client *http.Client) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/health", nil)
	if err != nil {
		return false
	}

	transport := &http.Transport{Proxy: nil}
	if client != nil {
		if supplied, ok := client.Transport.(*http.Transport); ok && supplied != nil {
			transport = supplied.Clone()
			transport.Proxy = nil
		}
	}
	defer transport.CloseIdleConnections()
	probeClient := &http.Client{
		Transport: transport,
		Timeout:   500 * time.Millisecond,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := probeClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}

	const maxHealthBytes = 4 * 1024
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHealthBytes))
	if err != nil || len(body) == maxHealthBytes {
		return false
	}
	var health struct {
		Status  string `json:"status"`
		Service string `json:"service"`
	}
	return json.Unmarshal(body, &health) == nil && health.Status == "ok" && health.Service == "mangaalpha"
}

func suggestedFallbackPort(port int) int {
	if port <= 0 || port >= 65535 {
		return 25601
	}
	return port + 1
}

func formatPortConflictDialog(isMangaAlpha bool, port int, shelfURL string) (title, text string, offerOpen bool) {
	if isMangaAlpha {
		return "MangaAlpha 已在运行", fmt.Sprintf(
			"端口 %d 已有 MangaAlpha 实例在后台运行。\n\n是否立即在浏览器中打开网页书架（%s）？", port, shelfURL,
		), true
	}
	fallbackPort := suggestedFallbackPort(port)
	return fmt.Sprintf("端口 %d 已被占用", port), fmt.Sprintf(
		"端口 %d 已被其他程序或旧版实例占用，MangaAlpha 无法启动。\n\n请关闭占用端口的程序（或旧版进程），或使用 --port %d / FK115_PORT=%d 更换端口后重新启动。",
		port, fallbackPort, fallbackPort,
	), false
}
