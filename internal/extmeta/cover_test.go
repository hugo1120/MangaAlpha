package extmeta

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestCoverFailureDiagnosticsAreVerboseAndSanitized(t *testing.T) {
	rec := testMetadataDiagnostics(t)
	client := &http.Client{Transport: managerTransport(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/missing":
			return managerResponse(r, http.StatusNotFound, strings.NewReader("fixture-secret")), nil
		case "/transport":
			return nil, errors.New("request failed to repo.example.test with fixture-secret")
		case "/length":
			resp := managerResponse(r, http.StatusOK, strings.NewReader("fixture-secret"))
			resp.ContentLength = 100
			return resp, nil
		case "/size":
			return managerResponse(r, http.StatusOK, strings.NewReader(strings.Repeat("a", 20))), nil
		default:
			return managerResponse(r, http.StatusOK, strings.NewReader("invalid")), nil
		}
	})}
	covers, err := NewCoverService(CoverConfig{
		DataDir: t.TempDir(), Client: client, Burst: 12, MaxImageBytes: 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	fetch := func(path string) error {
		t.Helper()
		_, err := covers.GetOrFetch(t.Context(), "fixture", "cover", "https://repo.example.test"+path+"?token=fixture-secret")
		if err == nil {
			t.Fatalf("%s was expected to fail", path)
		}
		return err
	}
	_ = fetch("/concise")
	if len(rec.Recent(10)) != 0 {
		t.Fatal("cover fetch diagnostic was recorded in concise mode")
	}
	rec.SetVerbose(true)
	for _, tc := range []struct {
		path, code string
		status     any
	}{
		{"/missing", "HTTP_STATUS", float64(http.StatusNotFound)},
		{"/transport", "TRANSPORT_ERROR", nil},
		{"/length", "SIZE_LIMIT", nil},
		{"/size", "SIZE_LIMIT", nil},
		{"/invalid", "INVALID_IMAGE", nil},
	} {
		_ = fetch(tc.path)
		events := rec.Recent(1)
		if len(events) != 1 {
			t.Fatal("missing cover diagnostic event")
		}
		event := events[0]
		if event.Component != "extmeta" || event.Stage != "extmeta_cover" || event.Action != "fetch" ||
			event.Level != "debug" || event.Code != tc.code || event.Detail == "" || event.Meta["httpStatus"] != tc.status {
			t.Errorf("%s diagnostic = %+v", tc.path, event)
		}
	}
	if err := fetch("/missing"); !errors.Is(err, ErrNegativeCached) {
		t.Fatalf("repeated request = %v", err)
	}
	if events := rec.Recent(1); len(events) != 1 || events[0].Code != "NEGATIVE_CACHE_HIT" {
		t.Fatalf("negative cache diagnostic = %+v", events)
	}
	assertSafeMetadataDiagnostics(t, rec.ExportText(20))
}
