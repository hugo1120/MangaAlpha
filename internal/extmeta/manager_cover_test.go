package extmeta

import (
	"bytes"
	"database/sql"
	"errors"
	"image"
	"image/jpeg"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xJogger/fake-komga-115/internal/database"
)

func TestFallbackNSFWCoverUsesReadySourcesAndCachesLookup(t *testing.T) {
	store, m, root := newTestManager(t, ManagerOptions{})
	seedMetadataLibrary(t, store, "fixtures", true)
	seedMetadataSeries(t, store, "bound", "fixtures", "Unrelated Folder")
	seedMetadataSeries(t, store, "missing", "fixtures", "Missing Cover")
	execManagerFixture(t, store.DB(), `
INSERT INTO series_metadata(series_id,bgm_subject_id,bgm_cover_path,ext_source_id,ext_entry_id,is_nsfw,title,alternate_titles_json,authors_json,match_origin,updated_at)
VALUES('bound',55,'fixture.jpg','existing-source','existing-entry',1,'Imported Title','[{"title":"Silver River"}]','[{"name":"Artist One"}]','manual',''),
      ('missing',56,'','','',1,'Missing Cover','[]','[]','auto','')`)
	var requests []string
	var err error
	var jpegCover bytes.Buffer
	if err := jpeg.Encode(&jpegCover, image.NewRGBA(image.Rect(0, 0, 12, 9)), nil); err != nil {
		t.Fatal(err)
	}
	m.covers, err = NewCoverService(CoverConfig{
		DataDir: root, RequestsPerSec: 100, Burst: 10,
		Client: &http.Client{Transport: managerTransport(func(r *http.Request) (*http.Response, error) {
			requests = append(requests, r.URL.Path)
			return managerResponse(r, http.StatusOK, bytes.NewReader(jpegCover.Bytes())), nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	addSource := func(name string, alternateSchema bool) database.MetadataSource {
		t.Helper()
		upstream := filepath.Join(root, name+".sqlite")
		if !alternateSchema {
			createManagerGallery(t, upstream, "(Expo) [Studio (Artist One)] Silver River [Chinese]")
		}
		db, err := sql.Open("sqlite", upstream)
		if err != nil {
			t.Fatal(err)
		}
		link := "https://covers.example.test/" + name + ".jpg"
		if alternateSchema {
			execManagerFixture(t, db, `
CREATE TABLE entries (id TEXT PRIMARY KEY, title TEXT, title_alt TEXT, authors TEXT, summary TEXT,
                      tags TEXT, category TEXT, cover_url TEXT, rating REAL, is_nsfw INTEGER,
                      updated_at INTEGER, extra_json TEXT);
`)
			execManagerFixture(t, db, "INSERT INTO entries(id,title,cover_url,is_nsfw) VALUES('fixture','Silver River',?,1)", link)
		} else {
			execManagerFixture(t, db, `
UPDATE gallery SET rating=5 WHERE gid=101;
INSERT INTO gallery(gid,title,thumb,category,rating,expunged) VALUES
 (200,'[Studio (Artist One)] Silver River [Digital]',?,'Manga',2,0),
 (201,'[Studio (Artist One)] Silver River [Chinese]',?,'Manga',5,1),
 (202,'Silver River [Digital]',?,'Manga',5,0)`, link, link+"?inactive", link+"?no-author")
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		src := addManagerSource(t, m, database.MetadataSource{Kind: KindOfflineSQLite, Name: name, LocalPath: upstream})
		syncManagerFixture(t, m, src, false, "success")
		return src
	}
	chain := addSource("chain", false)
	global := []database.MetadataSource{addSource("global-z", true), addSource("global-a", true)}
	slices.SortFunc(global, func(a, b database.MetadataSource) int { return strings.Compare(a.ID, b.ID) })

	scope := database.UserScope{UserID: "reader", AllLibraries: true, AllowNSFW: true, ShowNSFW: true}
	ctx := database.WithUserScope(t.Context(), scope)
	if _, err := m.SetLibrarySources(t.Context(), "fixtures", []string{chain.ID}); err != nil {
		t.Fatal(err)
	}
	before := readMetadata(t, store, "bound")
	expectCover := func(id, sourceID, entryID string) {
		t.Helper()
		raw, mediaType, err := m.FetchFallbackNSFWCover(ctx, id)
		if len(raw) == 0 || mediaType != "image/jpeg" || err != nil {
			t.Fatalf("fallback for %s = %s, %v", id, mediaType, err)
		}
		m.mu.RLock()
		hint := m.fallbackCovers[id].hint
		m.mu.RUnlock()
		if hint.sourceID != sourceID || hint.entryID != entryID {
			t.Fatalf("fallback hint selected %q/%q, want %q/%q", hint.sourceID, hint.entryID, sourceID, entryID)
		}
	}
	expectCover("bound", chain.ID, "200")
	expectCover("bound", chain.ID, "200")
	if !slices.Equal(requests, []string{"/chain.jpg"}) {
		t.Fatalf("expected one cover download, got %v", requests)
	}
	cacheFiles, err := os.ReadDir(filepath.Join(root, "thumbnails", "extmeta", SafePathSegment(chain.ID)))
	if err != nil || len(cacheFiles) != 1 {
		t.Fatalf("expected cached cover file, got %d files and error %v", len(cacheFiles), err)
	}
	scope.ShowNSFW = false
	if _, _, err := m.FetchFallbackNSFWCover(database.WithUserScope(ctx, scope), "bound"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("opted-out reader saw cached fallback: %v", err)
	}
	if _, _, err := m.FetchFallbackNSFWCover(t.Context(), "bound"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unscoped caller saw cached fallback: %v", err)
	}
	if after := readMetadata(t, store, "bound"); !reflect.DeepEqual(before, after) {
		t.Fatal("external fallback mutated bound metadata")
	}

	expectMiss := func() {
		t.Helper()
		if _, _, err := m.FetchFallbackNSFWCover(ctx, "missing"); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("fallback miss: %v", err)
		}
	}
	expectMiss()
	execManagerFixture(t, store.DB(), "UPDATE series_metadata SET title='Silver River' WHERE series_id='missing'")
	expectMiss()
	m.mu.Lock()
	miss := m.fallbackCovers["missing"]
	miss.expiresAt = time.Now().Add(-time.Second)
	m.fallbackCovers["missing"] = miss
	m.mu.Unlock()
	expectCover("missing", chain.ID, "202")

	disabled := false
	if _, err := m.UpdateSourceConfig(t.Context(), chain.ID, SourceConfig{Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	expectCover("bound", global[0].ID, "fixture")
	if err := m.DeleteSource(t.Context(), global[0].ID); err != nil {
		t.Fatal(err)
	}
	expectCover("bound", global[1].ID, "fixture")
	syncManagerFixture(t, m, global[1], true, "success")
	m.mu.RLock()
	cached := len(m.fallbackCovers)
	m.mu.RUnlock()
	if cached != 0 {
		t.Errorf("source sync left %d cached lookups", cached)
	}
	expectCover("bound", global[1].ID, "fixture")
	m.InvalidateFallbackCoverCache()
}
