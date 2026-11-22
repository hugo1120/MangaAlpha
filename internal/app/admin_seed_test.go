package app

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/xJogger/fake-komga-115/internal/database"
)

func TestUnattendedAdminSeedingFromEnvOptions(t *testing.T) {
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	a1, err := New(context.Background(), Options{
		Host:                 "127.0.0.1",
		Port:                 0,
		DataDir:              dataDir,
		Logger:               logger,
		InitialAdminUsername: "bootadmin",
		InitialAdminPassword: "InitialPassword123!",
	})
	if err != nil {
		t.Fatalf("first app.New with env admin failed: %v", err)
	}
	claimed, err := a1.HasAdmin(context.Background())
	if err != nil || !claimed {
		t.Fatalf("expected admin claimed from env, got claimed=%v err=%v", claimed, err)
	}
	_ = a1.Close()

	// Second boot with different env credentials must NOT overwrite existing admin
	a2, err := New(context.Background(), Options{
		Host:                 "127.0.0.1",
		Port:                 0,
		DataDir:              dataDir,
		Logger:               logger,
		InitialAdminUsername: "otheradmin",
		InitialAdminPassword: "DifferentPassword999!",
	})
	if err != nil {
		t.Fatalf("second app.New must ignore env admin when already claimed: %v", err)
	}
	_ = a2.Close()

	store, err := database.Open(filepath.Join(dataDir, "fake-komga-115.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, ok, _ := store.VerifyAdminPassword(context.Background(), "bootadmin", "InitialPassword123!"); !ok {
		t.Fatal("original seeded admin credentials must remain intact across restarts")
	}
}
