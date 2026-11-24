package httpserver

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xJogger/fake-komga-115/internal/archive"
	"github.com/xJogger/fake-komga-115/internal/bangumi"
	"github.com/xJogger/fake-komga-115/internal/cache"
	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/maintenance"
	"github.com/xJogger/fake-komga-115/internal/oneonefive"
	"github.com/xJogger/fake-komga-115/internal/scanner"
	"github.com/xJogger/fake-komga-115/internal/thumbnail"
	"github.com/xJogger/fake-komga-115/internal/webdav"
)

func TestDeleteWebDAVAccountHTTPStatusAndMessages(t *testing.T) {
	tempDir := t.TempDir()
	store, err := database.Open(filepath.Join(tempDir, "webdav_http.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	if _, err := store.ClaimAdmin(ctx, "admin", "password123"); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := oneonefive.New(store, logger)
	webdavMgr := webdav.NewManager()
	scanManager := scanner.NewWithWebDAV(store, client, webdavMgr, logger)
	defer scanManager.Close()
	cacheManager, err := cache.New(store, filepath.Join(tempDir, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	bangumiSvc, err := bangumi.NewService(store, filepath.Join(tempDir, "bangumi"), logger)
	if err != nil {
		t.Fatal(err)
	}
	defer bangumiSvc.Close()
	thumbnailService, err := thumbnail.New(store, filepath.Join(tempDir, "thumbnails"), logger)
	if err != nil {
		t.Fatal(err)
	}
	archiveService := archive.NewServiceWithWebDAV(store, client, webdavMgr, cacheManager, logger)
	defer archiveService.Close()
	coverManager := thumbnail.NewBatchManager(store, archiveService, thumbnailService, logger)
	defer coverManager.Close()
	maintenanceManager := maintenance.New(store, archiveService, thumbnailService, logger)
	defer maintenanceManager.Close()

	handler := New(
		store, client, scanManager, cacheManager,
		archiveService, thumbnailService, coverManager,
		maintenanceManager,
		RuntimeInfo{DataDir: tempDir, Host: "127.0.0.1", Port: 25600},
		logger,
	).WithServices(webdavMgr, bangumiSvc).Handler()

	acc, err := store.UpsertWebDAVAccount(ctx, database.WebDAVAccountSecret{
		WebDAVAccount: database.WebDAVAccount{
			Name:    "NAS DAV",
			BaseURL: "http://127.0.0.1:5244/dav",
		},
		Password: "pw",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertLibrary(ctx, database.Library{
		ID:              "lib_dav_in_use",
		Name:            "群晖漫画库",
		ProviderType:    database.ProviderWebDAV,
		WebDAVAccountID: acc.ID,
		RootCID:         "webdav:" + acc.ID + ":/comics",
		RootPath:        "/comics",
		Enabled:         true,
	}); err != nil {
		t.Fatal(err)
	}

	doDelete := func(id string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodDelete, "/admin/api/webdav-accounts/"+id, nil)
		req.SetBasicAuth("admin", "password123")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	// 1. Referenced account -> 409 WEBDAV_IN_USE naming the blocking library.
	rec := doDelete(acc.ID)
	if rec.Code != http.StatusConflict {
		t.Fatalf("in-use status = %d body=%s, want 409", rec.Code, rec.Body.String())
	}
	var errBody struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil {
		t.Fatal(err)
	}
	if errBody.Error != "WEBDAV_IN_USE" || !strings.Contains(errBody.Message, "群晖漫画库") {
		t.Fatalf("unexpected 409 payload: %+v", errBody)
	}

	// 2. Remove referencing library, then delete succeeds with 204.
	if err := store.DeleteLibrary(ctx, "lib_dav_in_use"); err != nil {
		t.Fatal(err)
	}
	rec = doDelete(acc.ID)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("unused delete status = %d body=%s, want 204", rec.Code, rec.Body.String())
	}

	// 3. Deleting again returns 404 NOT_FOUND.
	rec = doDelete(acc.ID)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing delete status = %d body=%s, want 404", rec.Code, rec.Body.String())
	}
}
