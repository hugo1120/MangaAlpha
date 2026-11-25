package httpserver

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xJogger/fake-komga-115/internal/bangumi"
	"github.com/xJogger/fake-komga-115/internal/database"
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
