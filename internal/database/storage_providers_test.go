package database

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeleteWebDAVAccountStatesAndBlockingLibraryNames(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "webdav_del.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	// Missing account -> sql.ErrNoRows
	if err := store.DeleteWebDAVAccount(ctx, "wd_missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing account err = %v, want sql.ErrNoRows", err)
	}

	acc, err := store.UpsertWebDAVAccount(ctx, WebDAVAccountSecret{
		WebDAVAccount: WebDAVAccount{
			Name:    "OpenList 测试",
			BaseURL: "http://127.0.0.1:5244/dav",
		},
		Password: "secret",
	}, false)
	if err != nil {
		t.Fatal(err)
	}

	// Create a disabled WebDAV library and a union library enrolling a second WebDAV member library.
	if err := store.UpsertLibrary(ctx, Library{
		ID:              "lib_wd_disabled",
		Name:            "隐藏 WebDAV 库",
		ProviderType:    ProviderWebDAV,
		WebDAVAccountID: acc.ID,
		RootCID:         "webdav:" + acc.ID + ":/hidden",
		RootPath:        "/hidden",
		Enabled:         false,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertLibrary(ctx, Library{
		ID:              "lib_wd_member",
		Name:            "云端主库",
		ProviderType:    ProviderWebDAV,
		WebDAVAccountID: acc.ID,
		RootCID:         "webdav:" + acc.ID + ":/main",
		RootPath:        "/main",
		Enabled:         true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertLibrary(ctx, Library{
		ID:           "lib_local_member",
		Name:         "本地补充库",
		ProviderType: ProviderLocal,
		RootCID:      "local:/tmp/demo",
		RootPath:     "/tmp/demo",
		Enabled:      true,
	}); err != nil {
		t.Fatal(err)
	}
	unionLib, _, err := store.CreateUnionLibrary(ctx, "漫画总库", false, []string{"lib_wd_member", "lib_local_member"})
	if err != nil {
		t.Fatal(err)
	}

	// Deleting while referenced must return ErrWebDAVAccountInUse with both library labels.
	err = store.DeleteWebDAVAccount(ctx, acc.ID)
	if !errors.Is(err, ErrWebDAVAccountInUse) {
		t.Fatalf("in-use err = %v, want ErrWebDAVAccountInUse", err)
	}
	var inUse *WebDAVAccountInUseError
	if !errors.As(err, &inUse) {
		t.Fatalf("expected *WebDAVAccountInUseError, got %T", err)
	}
	joined := strings.Join(inUse.Libraries, " | ")
	if !strings.Contains(joined, "隐藏 WebDAV 库（已隐藏）") {
		t.Fatalf("expected disabled library label in %q", joined)
	}
	if !strings.Contains(joined, "云端主库") || !strings.Contains(joined, "联合库「漫画总库」子库") {
		t.Fatalf("expected union member library label in %q", joined)
	}

	// Dissolve union and remove both referencing libraries, then delete must succeed.
	if err := store.DeleteLibrary(ctx, unionLib.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteLibrary(ctx, "lib_wd_member"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteLibrary(ctx, "lib_wd_disabled"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteWebDAVAccount(ctx, acc.ID); err != nil {
		t.Fatalf("expected clean delete after removing libraries, got %v", err)
	}
	// Repeating delete must report sql.ErrNoRows.
	if err := store.DeleteWebDAVAccount(ctx, acc.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("second delete err = %v, want sql.ErrNoRows", err)
	}
	// Creating a WebDAV library against a deleted account must fail atomically with ErrWebDAVAccountNotFound.
	if err := store.UpsertLibrary(ctx, Library{
		ID:              "lib_wd_dangling",
		Name:            "悬空库",
		ProviderType:    ProviderWebDAV,
		WebDAVAccountID: acc.ID,
		RootCID:         "webdav:" + acc.ID + ":/dangling",
		RootPath:        "/dangling",
		Enabled:         true,
	}); !errors.Is(err, ErrWebDAVAccountNotFound) {
		t.Fatalf("dangling WebDAV library err = %v, want ErrWebDAVAccountNotFound", err)
	}
}
