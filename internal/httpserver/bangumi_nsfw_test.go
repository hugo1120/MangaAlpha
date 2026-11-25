package httpserver

import (
	"bytes"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xJogger/fake-komga-115/internal/bangumi"
	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/extmeta"
)

type bangumiTestTransport func(*http.Request) (*http.Response, error)

func (f bangumiTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestBangumiNSFWHTTPCoverAndManagementScope(t *testing.T) {
	server, store, bgm, _ := setupV27TestServer(t, "")
	ctx := t.Context()
	server.thumbs.SetBangumiCoverFetcher(bgm.FetchCoverBytes)
	if _, err := store.ClaimAdmin(ctx, "coverowner", "FixturePassword123!"); err != nil {
		t.Fatal(err)
	}
	lib := database.Library{ID: "coverlib", RootCID: "coverroot", Name: "Cover library", Enabled: true}
	if err := store.UpsertLibrary(ctx, lib); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := store.DB().ExecContext(ctx, `
INSERT INTO series(id,library_id,cid,name,relative_path,created_at,updated_at,seen_scan_id)
VALUES('cover-series','coverlib','folder','Fixture Manga','Fixture Manga',?,?,'scan')`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `
INSERT INTO series_metadata(series_id,bgm_subject_id,is_nsfw,cover_source,updated_at)
VALUES('cover-series',303,1,'bangumi',?)`, now); err != nil {
		t.Fatal(err)
	}
	const adminPAT = "fixture-admin-pat"
	const adminProfile = "fixture-admin-bgm-profile"
	if err := store.SaveUserBangumiTrackerConfig(ctx, database.UserBangumiTracker{
		UserID: database.PrimaryAdminUserID, AuthMode: bangumi.TrackerAuthModePAT, AccessToken: adminPAT, BgmUsername: adminProfile,
	}); err != nil {
		t.Fatal(err)
	}
	newKey := func(userID string) string {
		t.Helper()
		key, _, err := store.CreateAPIKeyForUser(ctx, userID, "fixture")
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	newReader := func(name string, allow, show bool, libraries []string) string {
		t.Helper()
		user, err := store.CreateUser(ctx, database.CreateUserInput{
			Username: name, Password: "FixturePassword123!", AllowNSFW: allow, AllowedLibraryIDs: libraries,
		})
		if err != nil {
			t.Fatal(err)
		}
		if show {
			if _, err := store.SetUserShowNSFW(ctx, user.ID, true); err != nil {
				t.Fatal(err)
			}
		}
		return newKey(user.ID)
	}
	adminKey := newKey(database.PrimaryAdminUserID)
	allowedKey := newReader("coverreader", true, true, []string{lib.ID})
	disabledKey := newReader("coverdisabled", true, false, []string{lib.ID})
	noNSFWKey := newReader("coverrestricted", false, false, []string{lib.ID})
	noLibraryKey := newReader("covernolibrary", true, true, nil)
	png := testCoverPNG(t)
	var upstream, downloads atomic.Int32
	const detail = `{"id":303,"type":1,"name":"Fixture Manga","nsfw":true}`
	bgm.SetAPITransportForTest(bangumiTestTransport(func(r *http.Request) (*http.Response, error) {
		upstream.Add(1)
		w := httptest.NewRecorder()
		switch r.URL.Hostname() {
		case "api.bgm.tv":
			switch r.URL.Path {
			case "/v0/subjects/303/image":
				downloads.Add(1)
				if r.Header.Get("Authorization") != "Bearer "+adminPAT {
					t.Error("image request did not use the expected authorization")
					w.WriteHeader(http.StatusUnauthorized)
				} else {
					w.Header().Set("Location", "https://lain.bgm.tv/fixture.png")
					w.WriteHeader(http.StatusFound)
				}
			case "/v0/subjects/303":
				_, _ = io.WriteString(w, detail)
			case "/v0/subjects/303/persons":
				_, _ = io.WriteString(w, "[]")
			default:
				t.Errorf("unexpected API path %s", r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}
		case "lain.bgm.tv":
			if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
				t.Error("credentials reached the cover CDN")
			}
			_, _ = w.Write(png)
		default:
			t.Errorf("unexpected upstream host %s", r.URL.Host)
			w.WriteHeader(http.StatusNotFound)
		}
		return w.Result(), nil
	}))

	get := func(key, path string, wantStatus int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("X-API-Key", key)
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		if w.Code != wantStatus {
			t.Fatalf("GET %s = %d, want %d, body %s", path, w.Code, wantStatus, w.Body)
		}
		if strings.Contains(w.Body.String(), adminPAT) || strings.Contains(w.Body.String(), adminProfile) ||
			strings.Contains(strings.ToLower(w.Body.String()), "borrow") {
			t.Fatal("response exposed authorization details")
		}
		return w
	}

	const cover = "/api/v1/series/cover-series/thumbnail"
	const preview = "/admin/api/bangumi/subjects/303/cover"
	const candidates = "/admin/api/series/cover-series/bangumi/candidates?q=303"
	for _, denied := range []string{disabledKey, noNSFWKey, noLibraryKey, adminKey} {
		get(denied, cover, http.StatusNotFound)
	}
	get(allowedKey, preview, http.StatusForbidden)
	get(allowedKey, candidates, http.StatusForbidden)
	get(allowedKey, "/api/agent/v1/series/cover-series/bangumi/candidates?q=303", http.StatusForbidden)
	if upstream.Load() != 0 {
		t.Fatal("denied routes reached Bangumi before authorization")
	}

	allowed := get(allowedKey, cover, http.StatusOK)
	if allowed.Header().Get("Cache-Control") != "private, no-store" ||
		allowed.Header().Get("Content-Type") != "image/jpeg" || downloads.Load() != 1 {
		t.Fatal("authorized cover was not fetched and served privately")
	}
	if again := get(adminKey, preview, http.StatusOK); !bytes.Equal(allowed.Body.Bytes(), again.Body.Bytes()) || downloads.Load() != 1 {
		t.Fatal("authorized requests did not reuse the disk cover")
	}
	get(disabledKey, cover, http.StatusNotFound)
	if w := get(adminKey, candidates, http.StatusOK); !strings.Contains(w.Body.String(), `"nsfw":true`) {
		t.Fatal("admin management search omitted the NSFW candidate")
	}

	// Even when an NSFW lock makes the local series visible, the restricted
	// reader cannot consume the already-warmed upstream private cover.
	if _, err := store.DB().ExecContext(ctx, "UPDATE series_metadata SET is_nsfw=0,nsfw_lock=1 WHERE series_id='cover-series'"); err != nil {
		t.Fatal(err)
	}
	locked := get(disabledKey, cover, http.StatusOK)
	if bytes.Equal(locked.Body.Bytes(), allowed.Body.Bytes()) || downloads.Load() != 1 {
		t.Fatal("local classification override exposed the private disk cover")
	}
	before := upstream.Load()
	get(disabledKey, cover, http.StatusOK)
	if upstream.Load() != before {
		t.Fatal("missing public cover was not negative cached")
	}

	lib.Enabled = false
	if err := store.UpsertLibrary(ctx, lib); err != nil {
		t.Fatal(err)
	}
	get(allowedKey, cover, http.StatusNotFound)
	get(adminKey, candidates, http.StatusNotFound)
	if upstream.Load() != before {
		t.Fatal("hidden library triggered a Bangumi request")
	}
	owner, err := store.UserBangumiTrackerConfig(ctx, database.PrimaryAdminUserID)
	if err != nil || owner.AccessToken != adminPAT {
		t.Fatal("catalog cover read changed the owner's PAT")
	}
}

func TestBangumiNSFWHTTPCoverUsesReadOnlyOfflineFallback(t *testing.T) {
	server, store, bgm, _ := setupV27TestServer(t, "")
	ctx := t.Context()
	if _, err := store.ClaimAdmin(ctx, "fixture-owner", "FixturePassword123!"); err != nil {
		t.Fatal(err)
	}
	lib := database.Library{ID: "fallback-lib", RootCID: "fixture-root", Name: "Fixture library", Enabled: true}
	if err := store.UpsertLibrary(ctx, lib); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`
INSERT INTO series(id,library_id,cid,name,relative_path,created_at,updated_at,seen_scan_id)
VALUES('bound','fallback-lib','folder','Blue River','Blue River','','','scan');
INSERT INTO series_metadata(series_id,bgm_subject_id,bgm_cover_path,is_nsfw,match_origin,updated_at)
VALUES('bound',303,'existing-fixture.jpg',1,'manual','')`); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	path := filepath.Join(root, "fixture.sqlite")
	upstream, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = upstream.Exec(`
CREATE TABLE entries (id TEXT PRIMARY KEY, title TEXT, title_alt TEXT, authors TEXT, summary TEXT,
                      tags TEXT, category TEXT, cover_url TEXT, rating REAL, is_nsfw INTEGER,
                      updated_at INTEGER, extra_json TEXT);
INSERT INTO entries(id,title,cover_url,is_nsfw) VALUES('fixture-entry','Blue River','https://covers.example.test/fixture.png',1);
`)
	if err := errors.Join(err, upstream.Close()); err != nil {
		t.Fatal(err)
	}
	var coverDownloads, bgmRequests atomic.Int32
	png := testCoverPNG(t)
	coverService, err := extmeta.NewCoverService(extmeta.CoverConfig{
		DataDir: root,
		Client: &http.Client{Transport: bangumiTestTransport(func(r *http.Request) (*http.Response, error) {
			coverDownloads.Add(1)
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(png)), Request: r}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := extmeta.NewManager(store, nil, coverService, root, nil, extmeta.ManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	source, err := manager.AddCustomSource(ctx, database.MetadataSource{
		Name: "Offline fixture", Kind: extmeta.KindOfflineSQLite, LocalPath: path,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.StartSourceSync(ctx, source.ID, false); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		run := manager.Syncs()[0]
		if run.Status == "success" {
			break
		}
		if run.Status != "running" || time.Now().After(deadline) {
			t.Fatalf("fixture sync status: %+v", run)
		}
	}
	bgm.SetAPITransportForTest(bangumiTestTransport(func(r *http.Request) (*http.Response, error) {
		bgmRequests.Add(1)
		return &http.Response{StatusCode: http.StatusNotFound, Body: http.NoBody, Request: r}, nil
	}))
	server.thumbs.SetBangumiCoverFetcher(bgm.FetchCoverBytes)
	server.thumbs.SetNSFWFallbackCoverFetcher(manager.FetchFallbackNSFWCover, manager.InvalidateFallbackCoverCache)

	user, err := store.CreateUser(ctx, database.CreateUserInput{
		Username: "fixture-reader", Password: "FixturePassword123!", AllowNSFW: true, AllowedLibraryIDs: []string{lib.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	key, _, err := store.CreateAPIKeyForUser(ctx, user.ID, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	requestCover := func(show bool) *httptest.ResponseRecorder {
		t.Helper()
		if _, err := store.SetUserShowNSFW(ctx, user.ID, show); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodGet, "/api/v1/series/bound/thumbnail", nil)
		r.Header.Set("X-API-Key", key)
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		return w
	}
	if w := requestCover(false); w.Code != http.StatusNotFound || bgmRequests.Load() != 0 || coverDownloads.Load() != 0 {
		t.Fatalf("opted-out reader reached cover upstreams, status=%d", w.Code)
	}
	for _, mode := range []string{database.CoverSourceAuto, database.CoverSourceBangumi, database.CoverSourceBook} {
		if _, err := store.DB().Exec("UPDATE series_metadata SET cover_source=? WHERE series_id='bound'", mode); err != nil {
			t.Fatal(err)
		}
		series := database.Series{ID: "bound"}
		before, err := store.SeriesMetadataByID(ctx, series)
		if err != nil {
			t.Fatal(err)
		}
		w := requestCover(true)
		isFallback := bytes.Equal(w.Body.Bytes(), png)
		if w.Code != http.StatusOK || isFallback != (mode != database.CoverSourceBook) ||
			(isFallback && (w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("Content-Type") != "image/png")) {
			t.Fatalf("mode %s returned status=%d and fallback=%v", mode, w.Code, isFallback)
		}
		after, err := store.SeriesMetadataByID(ctx, series)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("mode %s modified series metadata: %v", mode, err)
		}
	}
	if w := requestCover(false); w.Code != http.StatusNotFound || bgmRequests.Load() != 1 || coverDownloads.Load() != 1 {
		t.Fatalf("unexpected cached fallback behavior, status=%d, Bangumi=%d, external=%d", w.Code, bgmRequests.Load(), coverDownloads.Load())
	}
}
