package diagnostics

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeTextRedactsSensitiveValues(t *testing.T) {
	input := `GET http://user:pass@192.168.1.50:19798/dav/secret/book.cbz failed: Bearer abcdef1234567890 cookie=fk115_session=xyz123 path=/home/alice/private/manga.zip win=C:\Users\alice\comic.cbz`
	got := SanitizeText(input)
	for _, leaked := range []string{"user:pass", "192.168.1.50", "/dav/secret", "abcdef1234567890", "xyz123", "/home/alice", `C:\Users\alice`} {
		if strings.Contains(got, leaked) {
			t.Fatalf("sanitized text leaked %q: %s", leaked, got)
		}
	}
	if !strings.Contains(got, "[redacted-url]") || !strings.Contains(got, "[redacted-path]") {
		t.Fatalf("expected redaction markers in %q", got)
	}
	// Loopback stays readable for local diagnostics.
	if out := SanitizeText("listening on 127.0.0.1:25600"); !strings.Contains(out, "127.0.0.1:25600") {
		t.Fatalf("expected loopback address preserved, got %q", out)
	}
}

func TestRecorderConciseVerboseRotationAndExport(t *testing.T) {
	dir := t.TempDir()
	rec, err := New(dir, false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rec.maxFileBytes = 512

	// Concise mode drops verbose/debug and non-lifecycle info events.
	rec.Record(Event{Level: LevelInfo, Component: "archive", Stage: "page_read", Detail: "dropped in concise"})
	rec.RecordVerbose(Event{Level: LevelDebug, Component: "webdav", Stage: "range", Detail: "dropped verbose"})
	rec.Record(Event{
		Level:     LevelError,
		Component: "webdav",
		Stage:     "range",
		Code:      "TIMEOUT",
		Detail:    "request to https://user:secret@nas.local/dav/file.cbz timed out",
		Meta: map[string]any{
			"provider":   "webdav",
			"secretKey":  "MUST_NOT_APPEAR",
			"httpStatus": 504,
		},
	})

	events := rec.Recent(50)
	if len(events) != 1 {
		t.Fatalf("expected 1 concise event, got %d: %+v", len(events), events)
	}
	if _, leaked := events[0].Meta["secretKey"]; leaked {
		t.Fatalf("unlisted meta key should be stripped: %+v", events[0].Meta)
	}
	if strings.Contains(events[0].Detail, "secret") || strings.Contains(events[0].Detail, "nas.local") {
		t.Fatalf("detail leaked url/secret: %s", events[0].Detail)
	}

	// Enable verbose mode and force rotation.
	rec.SetVerbose(true)
	for i := 0; i < 25; i++ {
		rec.RecordVerbose(Event{
			Level:      LevelDebug,
			Component:  "archive",
			Stage:      "page_read",
			DurationMs: int64(i + 1),
			Meta:       map[string]any{"pageNumber": i + 1, "format": "zip"},
		})
	}
	if _, err := os.Stat(rec.backupPath); err != nil {
		t.Fatalf("expected backup rotation file to exist: %v", err)
	}

	// Write a simulated Android native log file and verify ExportText merges & sanitizes it.
	androidLog := filepath.Join(rec.Dir(), androidEventsFileName)
	if err := os.WriteFile(androidLog, []byte(`{"stage":"android_saf","detail":"failed content://com.android.externalstorage/tree/123"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write android log: %v", err)
	}
	exported := rec.ExportText(100)
	if !strings.Contains(exported, "## Core Events") || !strings.Contains(exported, "## Android Native Events") {
		t.Fatalf("export missing sections:\n%s", exported)
	}
	if strings.Contains(exported, "com.android.externalstorage") {
		t.Fatalf("android URI leaked in export:\n%s", exported)
	}

	if err := rec.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if got := rec.Recent(50); len(got) != 0 {
		t.Fatalf("expected 0 events after Clear, got %d", len(got))
	}
}

func TestWriteStartupFailure(t *testing.T) {
	dir := t.TempDir()
	WriteStartupFailure(dir, "server", "startup", errors.New("listen tcp 0.0.0.0:25600: bind: address already in use"))
	rec, err := New(dir, false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	events := rec.Recent(10)
	if len(events) != 1 || events[0].Stage != "startup" || events[0].Level != LevelError {
		t.Fatalf("unexpected startup failure event: %+v", events)
	}
}

func TestClassifySSRFError(t *testing.T) {
	for _, message := range []string{
		"SSRF_BLOCKED", "target resolved to a non-public IP", "disallowed IP",
		"disallowed host", "DNS returned only unspecified addresses",
	} {
		if code := ClassifyError(errors.New(message)); code != "SSRF_BLOCKED" {
			t.Errorf("ClassifyError(%q) = %q, want SSRF_BLOCKED", message, code)
		}
	}
}

func TestClassifyErrorIgnoresURLKeywords(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{
			name: "sqlite filename in URL does not trigger DB_ERROR",
			err:  &url.Error{Op: "Get", URL: "https://example.test/source.sqlite", Err: errors.New("http: server gave HTTP response to HTTPS client")},
			want: "ERROR",
		},
		{
			name: "zip and 404 in URL do not misclassify connection reset",
			err:  &url.Error{Op: "Get", URL: "https://example.test/404/archive.zip", Err: errors.New("connection reset by peer")},
			want: "CONN_RESET",
		},
		{
			name: "auth in URL does not hide SSRF_BLOCKED",
			err:  &url.Error{Op: "Get", URL: "https://example.test/auth/repo.json", Err: errors.New("request blocked by SSRF policy")},
			want: "SSRF_BLOCKED",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyError(tc.err); got != tc.want {
				t.Errorf("ClassifyError(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestRecorderKeepsExtmetaLifecycleWithSafeEnums(t *testing.T) {
	rec, err := New(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	rec.Record(Event{Level: LevelInfo, Component: "extmeta", Stage: "extmeta_repo", Action: "subscribe"})
	rec.Record(Event{
		Level: LevelInfo, Component: "extmeta", Stage: "extmeta_sync", Action: "sync",
		Meta: map[string]any{"kind": "offline_sqlite", "phase": "indexing", "skipped": false},
	})
	rec.Record(Event{
		Level: LevelInfo, Component: "extmeta", Stage: "extmeta_sync", Action: "sync",
		Meta: map[string]any{"kind": "fixture-secret", "phase": "fc00::1", "skipped": true},
	})
	events := rec.Recent(10)
	if len(events) != 3 || events[0].Stage != "extmeta_repo" || events[1].Stage != "extmeta_sync" ||
		events[1].Meta["kind"] != "offline_sqlite" || events[1].Meta["phase"] != "indexing" ||
		len(events[2].Meta) != 1 || events[2].Meta["skipped"] != true {
		t.Fatalf("unexpected concise metadata diagnostics: %+v", events)
	}
}
