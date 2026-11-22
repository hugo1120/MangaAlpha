package httpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xJogger/fake-komga-115/internal/database"
)

// TestKomgaSSESubscribesBeforeConnectedFlush ensures that publishing an event
// immediately after the client receives ": connected" never loses the event
// under concurrent load (regression test for subscribe-after-flush race).
func TestKomgaSSESubscribesBeforeConnectedFlush(t *testing.T) {
	store, err := database.Open(t.TempDir() + "/sse.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Also verifies New(..., nil) safely falls back to a discard logger without panicking.
	srv := New(store, nil, nil, nil, nil, nil, nil, nil, RuntimeInfo{}, nil)
	ts := httptest.NewServer(http.HandlerFunc(srv.komgaSSEEvents))
	defer ts.Close()

	for i := 0; i < 20; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL, nil)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		reader := bufio.NewReader(resp.Body)
		line, err := reader.ReadString('\n')
		if err != nil || !strings.HasPrefix(line, ": connected") {
			resp.Body.Close()
			cancel()
			t.Fatalf("iter %d: expected connected comment, got %q err=%v", i, line, err)
		}
		_, _ = reader.ReadString('\n')

		// Publish immediately after receiving ": connected" — subscription must already be active.
		srv.sse.Publish("ReadProgressChanged", map[string]any{"bookId": "b1", "iter": i})

		evLine, err := reader.ReadString('\n')
		resp.Body.Close()
		cancel()
		if err != nil || strings.TrimSpace(evLine) != "event: ReadProgressChanged" {
			t.Fatalf("iter %d: expected ReadProgressChanged event, got %q err=%v", i, evLine, err)
		}
	}
}

func TestHistoryRouteAndTimelineBooksQuery(t *testing.T) {
	store, err := database.Open(t.TempDir() + "/history.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	if _, err := store.ClaimAdmin(ctx, "admin", "password123"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertLibrary(ctx, database.Library{
		ID: "lib-vis", Name: "Visible", RootCID: "r1", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertLibrary(ctx, database.Library{
		ID: "lib-hid", Name: "Hidden", RootCID: "r2", Enabled: false,
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, item := range []struct {
		lib, sid, bid, name string
	}{
		{"lib-vis", "s1", "b1", "Vol 01.cbz"},
		{"lib-vis", "s1", "b2", "Vol 02.cbz"},
		{"lib-hid", "s-hid", "b-hid", "Hidden 01.cbz"},
	} {
		if _, err := store.DB().Exec(`INSERT OR IGNORE INTO series(id,library_id,cid,name,relative_path,created_at,updated_at,seen_scan_id) VALUES(?,?,?,?,?,?,?,'scan')`,
			item.sid, item.lib, "cid-"+item.sid, item.sid, item.sid, now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := store.DB().Exec(`INSERT INTO books(id,series_id,library_id,file_id,parent_cid,name,size,pick_code,number_sort,created_at,updated_at,seen_scan_id) VALUES(?,?,?,?,'p',?,1024,'pk',1,?,?,'scan')`,
			item.bid, item.sid, item.lib, "f-"+item.bid, item.name, now, now); err != nil {
			t.Fatal(err)
		}
	}
	t1 := "2026-11-22T08:00:00Z"
	t2 := "2026-11-22T09:00:00Z"
	t3 := "2026-11-22T10:00:00Z"
	if _, err := store.DB().Exec(`INSERT INTO book_read_progress(book_id,series_id,completed,page,read_date,completed_at,updated_at) VALUES
		('b1','s1',1,200,?,?,?),
		('b2','s1',0,42,?,NULL,?),
		('b-hid','s-hid',0,5,?,NULL,?)`,
		t1, t1, t1, t2, t2, t3, t3); err != nil {
		t.Fatal(err)
	}

	srv := New(store, nil, nil, nil, nil, nil, nil, nil, RuntimeInfo{}, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 1. /history SPA route returns 200 HTML
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/history", nil)
	req.SetBasicAuth("admin", "password123")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("GET /history status=%d ct=%q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	// 2. Timeline books query returns newest-first IN_PROGRESS + READ and excludes hidden library
	req2, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/books?read_status=IN_PROGRESS&read_status=READ&sort=readProgress.readDate,desc&size=20", nil)
	req2.SetBasicAuth("admin", "password123")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("timeline books status=%d", resp2.StatusCode)
	}
	var payload struct {
		TotalElements int64 `json:"totalElements"`
		Content       []struct {
			ID string `json:"id"`
		} `json:"content"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.TotalElements != 2 || len(payload.Content) != 2 || payload.Content[0].ID != "b2" || payload.Content[1].ID != "b1" {
		t.Fatalf("unexpected timeline books response: %+v", payload)
	}
}
