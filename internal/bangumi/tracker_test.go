package bangumi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xJogger/fake-komga-115/internal/database"
)

func trackerTestTransport(t *testing.T, upstream string) http.RoundTripper {
	t.Helper()
	target, err := url.Parse(upstream)
	if err != nil {
		t.Fatal(err)
	}
	return matchingTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "api.bgm.tv" {
			r = r.Clone(r.Context())
			r.URL.Scheme, r.URL.Host, r.Host = target.Scheme, target.Host, target.Host
		}
		return http.DefaultTransport.RoundTrip(r)
	})
}

type mockBgmCollection struct {
	Type      int    `json:"type"`
	VolStatus int    `json:"vol_status"`
	Private   bool   `json:"private"`
	Rate      int    `json:"rate"`
	Comment   string `json:"comment"`
}

func TestBangumiTrackerOneWaySyncRules(t *testing.T) {
	tempDir := t.TempDir()
	store, err := database.Open(filepath.Join(tempDir, "tracker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var (
		mu          sync.Mutex
		collections = map[int64]*mockBgmCollection{}
		requests    []string
		lastPatches = map[int64]map[string]any{}
	)
	const validToken = "bgm-pat-secret-token-123"

	mockAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+validToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		mu.Unlock()

		if r.Method == http.MethodGet && r.URL.Path == "/v0/me" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":9527,"username":"manga_fan","nickname":"漫迷"}`))
			return
		}
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v0/users/manga_fan/collections/") {
			sidStr := strings.TrimPrefix(r.URL.Path, "/v0/users/manga_fan/collections/")
			sid, _ := strconv.ParseInt(sidStr, 10, 64)
			mu.Lock()
			col, ok := collections[sid]
			mu.Unlock()
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(col)
			return
		}
		if (r.Method == http.MethodPost || r.Method == http.MethodPatch) && strings.HasPrefix(r.URL.Path, "/v0/users/-/collections/") {
			sidStr := strings.TrimPrefix(r.URL.Path, "/v0/users/-/collections/")
			sid, _ := strconv.ParseInt(sidStr, 10, 64)
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			mu.Lock()
			if r.Method == http.MethodPost {
				col := &mockBgmCollection{}
				if v, ok := payload["type"].(float64); ok {
					col.Type = int(v)
				}
				if v, ok := payload["vol_status"].(float64); ok {
					col.VolStatus = int(v)
				}
				if v, ok := payload["private"].(bool); ok {
					col.Private = v
				}
				collections[sid] = col
			} else {
				col := collections[sid]
				if col == nil {
					col = &mockBgmCollection{}
					collections[sid] = col
				}
				if v, ok := payload["type"].(float64); ok {
					col.Type = int(v)
				}
				if v, ok := payload["vol_status"].(float64); ok {
					col.VolStatus = int(v)
				}
				if v, ok := payload["private"].(bool); ok {
					col.Private = v
				}
				lastPatches[sid] = payload
			}
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	defer mockAPI.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc, err := NewService(store, filepath.Join(tempDir, "bgm"), logger)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	svc.SetAPITransportForTest(trackerTestTransport(t, mockAPI.URL))
	svc.SetTrackerDebounceForTest(25 * time.Millisecond)

	ctx := context.Background()

	// 1. Invalid token must fail verification and NOT enable tracker
	badTok := "wrong-token"
	enableTrue := true
	if _, err := svc.UpdateTrackerConfig(ctx, TrackerConfigUpdate{
		AccessToken: &badTok,
		Enabled:     &enableTrue,
	}); err == nil {
		t.Fatal("expected invalid token to fail verification")
	}

	// 2. Valid token verifies & saves; Settings() must NEVER leak bangumi_tracker_token
	goodTok := validToken
	st, err := svc.UpdateTrackerConfig(ctx, TrackerConfigUpdate{
		AccessToken: &goodTok,
		Enabled:     &enableTrue,
	})
	if err != nil {
		t.Fatalf("UpdateTrackerConfig failed: %v", err)
	}
	if !st.Enabled || !st.HasToken || st.Username != "manga_fan" || st.Nickname != "漫迷" {
		t.Fatalf("unexpected tracker status: %+v", st)
	}
	settingsMap, err := store.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, leaked := settingsMap[SettingTrackerToken]; leaked {
		t.Fatal("store.Settings() leaked bangumi_tracker_token!")
	}

	// Seed a library + test series
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = store.DB().ExecContext(ctx, `
INSERT INTO libraries(id,name,provider_type,root_cid,enabled,one_shot,created_at,updated_at)
VALUES('lib1','主库','local','local:/tmp',1,0,?,?)`, now, now)
	if err != nil {
		t.Fatal(err)
	}

	seedSeries := func(sid string, bgmID int64, status string, totalVols int, isNSFW bool, oneShot bool, bookCount int) {
		t.Helper()
		osInt := 0
		if oneShot {
			osInt = 1
		}
		nsfwInt := 0
		if isNSFW {
			nsfwInt = 1
		}
		_, err := store.DB().ExecContext(ctx, `
INSERT INTO series(id,library_id,cid,name,relative_path,one_shot,books_count,created_at,updated_at,seen_scan_id)
VALUES(?,'lib1',?,?,?, ?, ?,?,?,'scan1')`, sid, "cid-"+sid, sid, sid, osInt, bookCount, now, now)
		if err != nil {
			t.Fatal(err)
		}
		var tv any
		if totalVols > 0 {
			tv = totalVols
		}
		_, err = store.DB().ExecContext(ctx, `
INSERT INTO series_metadata(series_id,bgm_subject_id,scrape_status,status,title,total_book_count,is_nsfw,updated_at)
VALUES(?,?,'matched',?,?,?,?,?)`, sid, bgmID, status, "Title-"+sid, tv, nsfwInt, now)
		if err != nil {
			t.Fatal(err)
		}
		for i := 1; i <= bookCount; i++ {
			bid := sid + "-b" + strconv.Itoa(i)
			_, err := store.DB().ExecContext(ctx, `
INSERT INTO books(id,series_id,library_id,file_id,parent_cid,name,size,pick_code,number_sort,created_at,updated_at,seen_scan_id)
VALUES(?,?,'lib1',?,?,?,1000,'pc',?,?,?,'scan1')`,
				bid, sid, "fid-"+bid, "cid-"+sid, "Vol."+strconv.Itoa(i)+".cbz", float64(i), now, now)
			if err != nil {
				t.Fatal(err)
			}
		}
	}

	// Case A: ENDED 3-volume series, uncollected on Bangumi
	seedSeries("s_ended", 1001, "ENDED", 3, false, false, 3)

	// Starting book 1 (page 5, not completed) -> POST type=3, vol_status=0
	page5 := 5
	completedFalse := false
	if err := store.UpdateBookReadProgress(ctx, database.Book{ID: "s_ended-b1", SeriesID: "s_ended"}, &completedFalse, &page5); err != nil {
		t.Fatal(err)
	}
	rec, err := svc.SyncSeriesTrackerNow(ctx, "s_ended")
	if err != nil {
		t.Fatalf("sync s_ended start: %v", err)
	}
	if rec.Status != "synced" || rec.RemoteType != CollectionTypeDoing || rec.RemoteVolStatus != 0 {
		t.Fatalf("expected Doing vol=0, got %+v", rec)
	}

	// Simulate user adding a rating & comment on Bangumi directly; later PATCH must NOT overwrite them!
	mu.Lock()
	collections[1001].Rate = 9
	collections[1001].Comment = "神作"
	mu.Unlock()

	// Complete book 2 -> PATCH ONLY {"vol_status": 2}
	completedTrue := true
	if err := store.UpdateBookReadProgress(ctx, database.Book{ID: "s_ended-b2", SeriesID: "s_ended"}, &completedTrue, nil); err != nil {
		t.Fatal(err)
	}
	rec, err = svc.SyncSeriesTrackerNow(ctx, "s_ended")
	if err != nil {
		t.Fatalf("sync s_ended vol 2: %v", err)
	}
	if rec.RemoteType != CollectionTypeDoing || rec.RemoteVolStatus != 2 {
		t.Fatalf("expected Doing vol=2, got %+v", rec)
	}
	mu.Lock()
	patch1001 := lastPatches[1001]
	col1001 := *collections[1001]
	mu.Unlock()
	if _, hasRate := patch1001["rate"]; hasRate {
		t.Fatalf("PATCH must never send rate, got %#v", patch1001)
	}
	if _, hasComment := patch1001["comment"]; hasComment {
		t.Fatalf("PATCH must never send comment, got %#v", patch1001)
	}
	if _, hasType := patch1001["type"]; hasType {
		t.Fatalf("unchanged type should not be included in PATCH, got %#v", patch1001)
	}
	if col1001.Rate != 9 || col1001.Comment != "神作" {
		t.Fatalf("remote rate/comment must remain untouched: %+v", col1001)
	}

	// Complete all 3 books -> ENDED + all completed + vol>=3 -> PATCH {"type": 2, "vol_status": 3}
	if err := store.MarkSeriesCompleted(ctx, "s_ended"); err != nil {
		t.Fatal(err)
	}
	rec, err = svc.SyncSeriesTrackerNow(ctx, "s_ended")
	if err != nil {
		t.Fatalf("sync s_ended complete: %v", err)
	}
	if rec.RemoteType != CollectionTypeCollect || rec.RemoteVolStatus != 3 {
		t.Fatalf("expected Collect vol=3, got %+v", rec)
	}

	// Case B: ONGOING series with 5 local books vs total_book_count=3 -> clamp vol_status to 3, stay Doing
	seedSeries("s_ongoing", 1002, "ONGOING", 3, false, false, 5)
	if err := store.MarkSeriesCompleted(ctx, "s_ongoing"); err != nil {
		t.Fatal(err)
	}
	rec, err = svc.SyncSeriesTrackerNow(ctx, "s_ongoing")
	if err != nil {
		t.Fatalf("sync s_ongoing: %v", err)
	}
	if rec.RemoteType != CollectionTypeDoing || rec.RemoteVolStatus != 3 {
		t.Fatalf("ONGOING must stay Doing and clamp vol=3, got %+v", rec)
	}

	// Case C: Remote already OnHold(4) with vol=8 ahead of local vol=2 -> NEVER downgrade or decrease
	seedSeries("s_remote_ahead", 1003, "ENDED", 10, false, false, 10)
	mu.Lock()
	collections[1003] = &mockBgmCollection{Type: CollectionTypeOnHold, VolStatus: 8, Rate: 7}
	reqCountBefore := len(requests)
	mu.Unlock()
	if err := store.UpdateBookReadProgress(ctx, database.Book{ID: "s_remote_ahead-b2", SeriesID: "s_remote_ahead"}, &completedTrue, nil); err != nil {
		t.Fatal(err)
	}
	rec, err = svc.SyncSeriesTrackerNow(ctx, "s_remote_ahead")
	if err != nil {
		t.Fatalf("sync s_remote_ahead: %v", err)
	}
	if rec.RemoteType != CollectionTypeOnHold || rec.RemoteVolStatus != 8 {
		t.Fatalf("must preserve OnHold vol=8, got %+v", rec)
	}
	mu.Lock()
	if len(requests) != reqCountBefore+1 { // ONLY GET, zero PATCH!
		t.Fatalf("expected only GET and zero PATCH when remote is ahead, requests=%v", requests[reqCountBefore:])
	}
	mu.Unlock()

	// Case D: NSFW series skipped by default, then synced as private when nsfwMode="private"
	seedSeries("s_nsfw", 1004, "ENDED", 1, true, true, 1)
	if err := store.MarkSeriesCompleted(ctx, "s_nsfw"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	reqCountBefore = len(requests)
	mu.Unlock()
	rec, err = svc.SyncSeriesTrackerNow(ctx, "s_nsfw")
	if err != nil {
		t.Fatalf("sync s_nsfw skip: %v", err)
	}
	if rec.Status != "skipped" {
		t.Fatalf("expected NSFW to be skipped by default, got %+v", rec)
	}
	mu.Lock()
	if len(requests) != reqCountBefore {
		t.Fatalf("skipped NSFW must not make any outbound Bangumi API calls")
	}
	mu.Unlock()

	privateMode := TrackerNSFWModePrivate
	if _, err := svc.UpdateTrackerConfig(ctx, TrackerConfigUpdate{NSFWMode: &privateMode}); err != nil {
		t.Fatal(err)
	}
	rec, err = svc.SyncSeriesTrackerNow(ctx, "s_nsfw")
	if err != nil {
		t.Fatalf("sync s_nsfw private: %v", err)
	}
	if rec.Status != "synced" || !rec.RemotePrivate || rec.RemoteType != CollectionTypeCollect {
		t.Fatalf("expected NSFW synced as private Collect, got %+v", rec)
	}
	mu.Lock()
	if collections[1004] == nil || !collections[1004].Private {
		t.Fatalf("remote collection 1004 must have private=true, got %+v", collections[1004])
	}
	mu.Unlock()

	// Case E: Async debounced NotifyProgress coalesces rapid events
	seedSeries("s_async", 1005, "ENDED", 2, false, false, 2)
	if err := store.UpdateBookReadProgress(ctx, database.Book{ID: "s_async-b1", SeriesID: "s_async"}, &completedTrue, nil); err != nil {
		t.Fatal(err)
	}
	svc.NotifyProgress("s_async")
	svc.NotifyProgress("s_async")
	svc.NotifyProgress("s_async")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got, ok, _ := store.BangumiTrackerRecordBySubject(ctx, 1005); ok && got.Status == "synced" && got.RemoteVolStatus == 1 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("timed out waiting for async debounced Bangumi tracker sync")
}
