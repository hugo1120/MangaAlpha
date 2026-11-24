package httpserver

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

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

func TestSeriesSearchTraditionalSimplifiedHTTP(t *testing.T) {
	tempDir := t.TempDir()
	store, err := database.Open(filepath.Join(tempDir, "cjk.db"))
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

	if err := store.UpsertLibrary(ctx, database.Library{
		ID: "lib_cjk", Name: "CJK", ProviderType: "local", RootCID: "local:cjk", RootPath: t.TempDir(), Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := store.DB().Exec(`
INSERT INTO series(id,library_id,cid,name,relative_path,one_shot,books_count,created_at,updated_at,seen_scan_id)
VALUES('s_trad','lib_cjk','c1','灌籃高手','灌籃高手',0,1,?,?,'sc1')`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`
INSERT INTO books(id,series_id,library_id,file_id,parent_cid,name,size,pick_code,number_sort,created_at,updated_at,seen_scan_id)
VALUES('b_trad','s_trad','lib_cjk','f1','c1','灌籃高手 Vol.01.cbz',100,'p1',1,?,?,'sc1')`, now, now); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/series?search="+url.QueryEscape("灌篮高手"), nil)
	req.SetBasicAuth("admin", "password123")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		TotalElements int64 `json:"totalElements"`
		Content       []struct {
			ID string `json:"id"`
		} `json:"content"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.TotalElements != 1 || len(payload.Content) != 1 || payload.Content[0].ID != "s_trad" {
		t.Fatalf("unexpected search payload: %+v", payload)
	}
}
