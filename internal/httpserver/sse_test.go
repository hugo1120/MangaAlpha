package httpserver

import (
	"bufio"
	"context"
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
