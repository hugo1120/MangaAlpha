package bangumi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xJogger/fake-komga-115/internal/database"
)

func TestBangumiTrackerOAuthExchangeRefreshAnd401Retry(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := database.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	svc, err := NewService(store, dir, nil)
	if err != nil {
		t.Fatalf("new bangumi service: %v", err)
	}
	defer svc.Close()

	var (
		mu                  sync.Mutex
		activeAccessToken   = "oauth_tok_v1"
		activeRefreshToken  = "oauth_ref_v1"
		refreshCalls        atomic.Int32
		lastRefreshRedirect string
		pushedVol           int
	)

	mockBgm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/oauth/access_token":
			_ = r.ParseForm()
			grant := r.FormValue("grant_type")
			switch grant {
			case "authorization_code":
				if r.FormValue("code") != "good_code_123" {
					w.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant", "error_description": "bad code"})
					return
				}
				if r.FormValue("client_id") != DefaultOAuthClientID || r.FormValue("client_secret") != DefaultOAuthClientSecret {
					w.WriteHeader(http.StatusUnauthorized)
					_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_client"})
					return
				}
				mu.Lock()
				resp := map[string]any{
					"access_token":  activeAccessToken,
					"refresh_token": activeRefreshToken,
					"expires_in":    604800,
					"token_type":    "Bearer",
					"user_id":       4242,
				}
				mu.Unlock()
				_ = json.NewEncoder(w).Encode(resp)
			case "refresh_token":
				n := refreshCalls.Add(1)
				mu.Lock()
				expectedRef := activeRefreshToken
				if r.FormValue("refresh_token") != expectedRef {
					mu.Unlock()
					w.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant", "error_description": "stale refresh token"})
					return
				}
				lastRefreshRedirect = r.FormValue("redirect_uri")
				activeAccessToken = "oauth_tok_v" + string(rune('1'+n))
				activeRefreshToken = "oauth_ref_v" + string(rune('1'+n))
				resp := map[string]any{
					"access_token":  activeAccessToken,
					"refresh_token": activeRefreshToken,
					"expires_in":    604800,
					"token_type":    "Bearer",
				}
				mu.Unlock()
				_ = json.NewEncoder(w).Encode(resp)
			default:
				w.WriteHeader(http.StatusBadRequest)
			}
		case r.Method == http.MethodPost && r.URL.Path == "/oauth/token_status":
			_ = r.ParseForm()
			exp := time.Now().UTC().Add(15 * 24 * time.Hour).Unix()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": r.FormValue("access_token"),
				"expires":      exp,
				"user_id":      4242,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v0/me":
			mu.Lock()
			want := "Bearer " + activeAccessToken
			mu.Unlock()
			if r.Header.Get("Authorization") != want && r.Header.Get("Authorization") != "Bearer pat_token_near_expiry" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":       4242,
				"username": "oauthtester",
				"nickname": "OAuth漫迷",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v0/users/oauthtester/collections/9901":
			mu.Lock()
			want := "Bearer " + activeAccessToken
			mu.Unlock()
			if r.Header.Get("Authorization") != want {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/v0/users/-/collections/9901":
			mu.Lock()
			want := "Bearer " + activeAccessToken
			mu.Unlock()
			if r.Header.Get("Authorization") != want {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			if v, ok := payload["vol_status"].(float64); ok {
				mu.Lock()
				pushedVol = int(v)
				mu.Unlock()
			}
			w.WriteHeader(http.StatusAccepted)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockBgm.Close()

	svc.SetAPIBaseURLForTest(mockBgm.URL)
	svc.SetOAuthBaseURLForTest(mockBgm.URL)

	// 1. Build authorize URL with builtin app
	callback := "http://127.0.0.1:25600/admin/api/bangumi/tracker/oauth/callback"
	authURL, err := svc.BuildTrackerOAuthAuthorizeURL(ctx, callback, "state_xyz")
	if err != nil {
		t.Fatalf("build auth url: %v", err)
	}
	parsedAuth, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse auth url: %v", err)
	}
	if parsedAuth.Query().Get("client_id") != DefaultOAuthClientID || parsedAuth.Query().Get("redirect_uri") != callback {
		t.Fatalf("unexpected authorize url: %s", authURL)
	}

	// 2. Exchange code for OAuth token pair
	st, err := svc.ExchangeTrackerOAuthCode(ctx, "good_code_123", callback)
	if err != nil {
		t.Fatalf("exchange code: %v", err)
	}
	if !st.HasToken || !st.Enabled || st.AuthMode != TrackerAuthModeOAuth || !st.AutoRefresh {
		t.Fatalf("expected active auto-refresh OAuth status, got %+v", st)
	}
	if st.Username != "oauthtester" || st.ExpiresAt == "" || st.LastRefreshAt == "" {
		t.Fatalf("missing oauth profile/expiry fields: %+v", st)
	}

	// 3. Simulate server-side token invalidation (401 on current access_token) and verify SyncSeries auto-refreshes & retries once!
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.DB().ExecContext(ctx,
		`INSERT INTO libraries(id,name,provider_type,root_cid,root_path,enabled,created_at,updated_at) VALUES('lib_o','OAuthLib','local','local:/tmp',?,1,?,?)`,
		dir, now, now); err != nil {
		t.Fatalf("insert lib: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx,
		`INSERT INTO series(id,library_id,cid,name,relative_path,books_count,created_at,updated_at,seen_scan_id) VALUES('s_o1','lib_o','c_o1','TestManga','TestManga',1,?,?,'sc1')`,
		now, now); err != nil {
		t.Fatalf("insert series: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx,
		`INSERT INTO books(id,series_id,library_id,file_id,parent_cid,name,size,pick_code,number_sort,created_at,updated_at,seen_scan_id) VALUES('b_o1','s_o1','lib_o','f_o1','c_o1','Vol 01.cbz',100,'p1',1,?,?,'sc1')`,
		now, now); err != nil {
		t.Fatalf("insert book: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `
INSERT INTO series_metadata(series_id,bgm_subject_id,scrape_status,status,title,is_nsfw,updated_at)
VALUES('s_o1',9901,'matched','ONGOING','TestManga',0,?)`, now); err != nil {
		t.Fatalf("insert meta: %v", err)
	}
	page20 := 20
	completedTrue := true
	if err := store.UpdateBookReadProgress(ctx, database.Book{ID: "b_o1", SeriesID: "s_o1"}, &completedTrue, &page20); err != nil {
		t.Fatalf("update progress: %v", err)
	}

	// Expire access_token on mock server without touching local expiresAt -> forces reactive 401 refresh!
	mu.Lock()
	activeAccessToken = "server_invalidated_v1"
	mu.Unlock()

	rec, err := svc.SyncSeriesTrackerNow(ctx, "s_o1")
	if err != nil {
		t.Fatalf("sync with 401 auto-refresh failed: %v", err)
	}
	if rec.Status != "synced" || rec.RemoteVolStatus != 1 {
		t.Fatalf("unexpected sync record after 401 retry: %+v", rec)
	}
	mu.Lock()
	gotVol := pushedVol
	gotRedir := lastRefreshRedirect
	mu.Unlock()
	if gotVol != 1 || refreshCalls.Load() != 1 || gotRedir != callback {
		t.Fatalf("expected 1 refresh with stored callback=%q & pushedVol=1, got calls=%d redir=%q vol=%d",
			callback, refreshCalls.Load(), gotRedir, gotVol)
	}

	// 4. Verify PAT expiry discovery & expiresSoon warning (<30d remaining)
	pat := "pat_token_near_expiry"
	patSt, err := svc.UpdateTrackerConfig(ctx, TrackerConfigUpdate{AccessToken: &pat})
	if err != nil {
		t.Fatalf("switch to PAT: %v", err)
	}
	if patSt.AuthMode != TrackerAuthModePAT || patSt.AutoRefresh || !patSt.ExpiresSoon || patSt.ExpiresAt == "" {
		t.Fatalf("expected PAT mode with expiresSoon=true, got %+v", patSt)
	}

	// 5. Verify custom OAuth app validation & override
	badCID := "bgm_custom_only"
	if _, err := svc.UpdateTrackerConfig(ctx, TrackerConfigUpdate{CustomOAuthClientID: &badCID}); err == nil {
		t.Fatal("expected error when custom client_id set without secret")
	}
	customCID, customSec := "bgm_custom_123", "sec_custom_456"
	customSt, err := svc.UpdateTrackerConfig(ctx, TrackerConfigUpdate{
		CustomOAuthClientID:     &customCID,
		CustomOAuthClientSecret: &customSec,
	})
	if err != nil {
		t.Fatalf("save custom oauth app: %v", err)
	}
	if !customSt.UsingCustomOAuthApp || customSt.CustomOAuthClientID != customCID || !customSt.HasCustomOAuthSecret {
		t.Fatalf("unexpected custom oauth status: %+v", customSt)
	}
	customURL, err := svc.BuildTrackerOAuthAuthorizeURL(ctx, callback, "st2")
	if err != nil || !strings.Contains(customURL, "client_id=bgm_custom_123") {
		t.Fatalf("expected custom client_id in authorize url, got %q err=%v", customURL, err)
	}
}
