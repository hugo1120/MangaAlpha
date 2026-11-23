//go:build !windows

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
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

func isWindowsTrayEnabled(_ bool) bool { return false }

func printVersionForPlatform(ver string) { fmt.Println(ver) }

func showStartupErrorDialog(_ error, _ int, _ string, _ string) {}

func configureCommandForPlatform(_ *exec.Cmd) {}

func newServerLogger(_ string, _ bool) (*slog.Logger, string, func()) {
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})), "", func() {}
}

func runPlatformLoop(ctx context.Context, _ context.CancelFunc, errCh <-chan error, _ trayConfig) error {
	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}
