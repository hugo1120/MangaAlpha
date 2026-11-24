package database

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSearchParserKohariaAndFields(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "search.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.UpsertLibrary(ctx, Library{
		ID: "lib1", Name: "Main", RootCID: "r1", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	insertCatalogSeries(t, store, "lib1", "s1", "Aria Orig", []catalogBookTime{
		{created: "2024-01-01T00:00:00Z", modified: "2024-01-01T00:00:00Z"},
	})
	insertCatalogSeries(t, store, "lib1", "s2", "Space Story", []catalogBookTime{
		{created: "2024-01-02T00:00:00Z", modified: "2024-01-02T00:00:00Z"},
	})
	insertCatalogSeries(t, store, "lib1", "s3", "Water Planet Chronicle", []catalogBookTime{
		{created: "2024-01-03T00:00:00Z", modified: "2024-01-03T00:00:00Z"},
	})

	s1, _ := store.SeriesByID(ctx, "s1")
	s2, _ := store.SeriesByID(ctx, "s2")
	s3, _ := store.SeriesByID(ctx, "s3")

	if _, err := store.ApplyScrapedMetadata(ctx, s1, ScrapedMetadata{
		SubjectID: 101,
		Title:     "水星领航员",
		TitleSort: "水星领航员",
		Publisher: "Mag Garden",
		Status:    "ENDED",
		Genres:    []string{"治愈"},
		Tags:      []string{"科幻", "日常"},
		Authors:   []Author{{Name: "天野梢", Role: "writer"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyScrapedMetadata(ctx, s2, ScrapedMetadata{
		SubjectID: 102,
		Title:     "星际漫游",
		TitleSort: "星际漫游",
		Publisher: "集英社",
		Status:    "ONGOING",
		Genres:    []string{"冒险"},
		Tags:      []string{"水星领航员", "治愈"},
		Authors:   []Author{{Name: "示例作者", Role: "artist"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyScrapedMetadata(ctx, s3, ScrapedMetadata{
		SubjectID: 103,
		Title:     "水星领航员 特别篇",
		TitleSort: "水星领航员 特别篇",
		Publisher: "Mag Garden",
		Status:    "ENDED",
		Genres:    []string{"治愈"},
		Tags:      []string{"日常"},
		Authors:   []Author{{Name: "天野梢", Role: "writer"}},
	}); err != nil {
		t.Fatal(err)
	}

	// 1. Koharia exact "(q) OR tag:(q)" union query + relevance sorting
	items, total, err := store.SeriesPage(ctx, SeriesQuery{
		Search: "(水星领航员) OR tag:(水星领航员)",
		Sort:   "relevance,asc",
		Size:   20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(items) != 3 {
		t.Fatalf("koharia union total=%d len=%d, want 3", total, len(items))
	}
	if items[0].ID != "s1" || items[1].ID != "s3" || items[2].ID != "s2" {
		t.Fatalf("unexpected relevance order: %s, %s, %s (want s1, s3, s2)", items[0].ID, items[1].ID, items[2].ID)
	}

	// 2. Koharia BooksPage union query
	books, bTotal, err := store.BooksPage(ctx, BookQuery{
		Search: "(水星领航员) OR tag:(水星领航员)",
		Sort:   "relevance,asc",
		Size:   20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if bTotal != 3 || len(books) != 3 {
		t.Fatalf("koharia books union total=%d len=%d, want 3", bTotal, len(books))
	}

	// 3. Field prefixes, boolean AND / NOT, role prefix
	tests := []struct {
		query string
		want  []string
	}{
		{`tag:科幻`, []string{"s1"}},
		{`publisher:"Mag Garden" NOT 特别篇`, []string{"s1"}},
		{`writer:天野梢`, []string{"s1", "s3"}},
		{`artist:示例作者`, []string{"s2"}},
		{`status:ONGOING`, []string{"s2"}},
		{`genre:冒险 OR tag:科幻`, []string{"s1", "s2"}},
		{`水星领航员 -特别篇`, []string{"s1"}},
	}
	for _, tc := range tests {
		got, cnt, err := store.SeriesPage(ctx, SeriesQuery{Search: tc.query, Size: 20, Sort: "metadata.titleSort,asc"})
		if err != nil {
			t.Fatalf("query %q err=%v", tc.query, err)
		}
		if int(cnt) != len(tc.want) || len(got) != len(tc.want) {
			t.Fatalf("query %q got count=%d items=%d want %v", tc.query, cnt, len(got), tc.want)
		}
		gotSet := map[string]bool{}
		for _, item := range got {
			gotSet[item.ID] = true
		}
		for _, id := range tc.want {
			if !gotSet[id] {
				t.Fatalf("query %q missing %s in %#v", tc.query, id, gotSet)
			}
		}
	}

	// 4. Out-of-range page (> 0) must still return exact total count on both SeriesPage & BooksPage
	emptySeries, outTotal, err := store.SeriesPage(ctx, SeriesQuery{
		Search: "(水星领航员) OR tag:(水星领航员)",
		Sort:   "relevance,asc",
		Page:   5,
		Size:   20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(emptySeries) != 0 || outTotal != 3 {
		t.Fatalf("out-of-range SeriesPage: len=%d total=%d, want 0/3", len(emptySeries), outTotal)
	}
	emptyBooks, outBookTotal, err := store.BooksPage(ctx, BookQuery{
		Search: "(水星领航员) OR tag:(水星领航员)",
		Sort:   "relevance,asc",
		Page:   5,
		Size:   20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(emptyBooks) != 0 || outBookTotal != 3 {
		t.Fatalf("out-of-range BooksPage: len=%d total=%d, want 0/3", len(emptyBooks), outBookTotal)
	}

	// 5. BooksPage boolean AND NOT, standalone NOT, extra tag filter, and unpaged search
	bNot, bNotTotal, err := store.BooksPage(ctx, BookQuery{
		Search: "水星领航员 NOT 特别篇",
		Sort:   "relevance,asc",
		Size:   20,
	})
	if err != nil || bNotTotal != 1 || len(bNot) != 1 || bNot[0].SeriesID != "s1" {
		t.Fatalf("BooksPage AND NOT: err=%v total=%d items=%#v", err, bNotTotal, bNot)
	}
	bPureNot, bPureNotTotal, err := store.BooksPage(ctx, BookQuery{
		Search: "-特别篇",
		Size:   20,
	})
	if err != nil || bPureNotTotal != 2 || len(bPureNot) != 2 {
		t.Fatalf("BooksPage standalone NOT: err=%v total=%d len=%d", err, bPureNotTotal, len(bPureNot))
	}
	bFiltered, bFilteredTotal, err := store.BooksPage(ctx, BookQuery{
		Search: "(水星领航员) OR tag:(水星领航员)",
		Tags:   []string{"科幻"},
		Size:   20,
	})
	if err != nil || bFilteredTotal != 1 || len(bFiltered) != 1 || bFiltered[0].SeriesID != "s1" {
		t.Fatalf("BooksPage search+tag filter: err=%v total=%d items=%#v", err, bFilteredTotal, bFiltered)
	}
	bUnpaged, bUnpagedTotal, err := store.BooksPage(ctx, BookQuery{
		Search:  "(水星领航员) OR tag:(水星领航员)",
		Unpaged: true,
	})
	if err != nil || bUnpagedTotal != 3 || len(bUnpaged) != 3 {
		t.Fatalf("BooksPage unpaged search: err=%v total=%d len=%d", err, bUnpagedTotal, len(bUnpaged))
	}

	// 6. NSFW exclusion + disabled library exclusion in candidate-set searches
	if err := store.UpsertLibrary(ctx, Library{
		ID: "lib_hidden", Name: "Hidden", RootCID: "r2", Enabled: false,
	}); err != nil {
		t.Fatal(err)
	}
	insertCatalogSeries(t, store, "lib_hidden", "s_hidden", "水星领航员 Hidden", []catalogBookTime{
		{created: "2024-01-04T00:00:00Z", modified: "2024-01-04T00:00:00Z"},
	})
	insertCatalogSeries(t, store, "lib1", "s_nsfw", "水星领航员 R18", []catalogBookTime{
		{created: "2024-01-05T00:00:00Z", modified: "2024-01-05T00:00:00Z"},
	})
	sNSFW, _ := store.SeriesByIDAny(ctx, "s_nsfw", true)
	if _, err := store.ApplyScrapedMetadata(ctx, sNSFW, ScrapedMetadata{
		SubjectID: 104,
		Title:     "水星领航员 R18",
		TitleSort: "水星领航员 R18",
		IsNSFW:    true,
	}); err != nil {
		t.Fatal(err)
	}
	// Default (ShowNSFW=false): neither hidden library nor NSFW series/book appears
	sVis, sVisTotal, err := store.SeriesPage(ctx, SeriesQuery{
		Search: "(水星领航员) OR tag:(水星领航员)",
		Sort:   "relevance,asc",
		Size:   20,
	})
	if err != nil || sVisTotal != 3 || len(sVis) != 3 {
		t.Fatalf("SeriesPage visibility filter: err=%v total=%d len=%d", err, sVisTotal, len(sVis))
	}
	bVis, bVisTotal, err := store.BooksPage(ctx, BookQuery{
		Search: "(水星领航员) OR tag:(水星领航员)",
		Sort:   "relevance,asc",
		Size:   20,
	})
	if err != nil || bVisTotal != 3 || len(bVis) != 3 {
		t.Fatalf("BooksPage visibility filter: err=%v total=%d len=%d", err, bVisTotal, len(bVis))
	}
	// With ShowNSFW enabled, NSFW series & book appear, hidden library stays excluded
	if err := store.SetSetting(ctx, "show_nsfw", "true"); err != nil {
		t.Fatal(err)
	}
	_, sNSFWTotal, err := store.SeriesPage(ctx, SeriesQuery{
		Search: "(水星领航员) OR tag:(水星领航员)",
		Sort:   "relevance,asc",
		Size:   20,
	})
	if err != nil || sNSFWTotal != 4 {
		t.Fatalf("SeriesPage showNSFW=true: err=%v total=%d want 4", err, sNSFWTotal)
	}
	_, bNSFWTotal, err := store.BooksPage(ctx, BookQuery{
		Search: "(水星领航员) OR tag:(水星领航员)",
		Sort:   "relevance,asc",
		Size:   20,
	})
	if err != nil || bNSFWTotal != 4 {
		t.Fatalf("BooksPage showNSFW=true: err=%v total=%d want 4", err, bNSFWTotal)
	}
	if err := store.SetSetting(ctx, "show_nsfw", "false"); err != nil {
		t.Fatal(err)
	}

	// 7. Shelf cache covers Koharia browse sorts & sizes (metadata.titleSort,asc size=20; lastModifiedDate,desc size=25)
	for _, q := range []SeriesQuery{
		{Page: 0, Size: 20, Sort: "metadata.titleSort,asc"},
		{Page: 0, Size: 25, Sort: "lastModifiedDate,desc"},
	} {
		first, t1, err := store.SeriesPage(ctx, q)
		if err != nil || t1 != 3 || len(first) != 3 {
			t.Fatalf("shelf cache cold %+v: err=%v total=%d len=%d", q, err, t1, len(first))
		}
		second, t2, err := store.SeriesPage(ctx, q)
		if err != nil || t2 != t1 || len(second) != len(first) || second[0].ID != first[0].ID {
			t.Fatalf("shelf cache warm %+v mismatch: err=%v t=%d/%d", q, err, t2, t1)
		}
	}
}

func TestSearchFoldsTraditionalSimplified(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "cjk-search.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.UpsertLibrary(ctx, Library{ID: "lib", Name: "Main", RootCID: "root", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	insertCatalogSeries(t, store, "lib", "trad", "灌籃高手", []catalogBookTime{
		{created: "2024-01-01T00:00:00Z", modified: "2024-01-01T00:00:00Z"},
	})
	insertCatalogSeries(t, store, "lib", "simp", "灌篮高手 全国大赛", []catalogBookTime{
		{created: "2024-01-02T00:00:00Z", modified: "2024-01-02T00:00:00Z"},
	})
	source, err := store.SeriesByID(ctx, "trad")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyScrapedMetadata(ctx, source, ScrapedMetadata{
		SubjectID: 2001,
		Title:     "灌籃高手",
		TitleSort: "灌籃高手",
		Publisher: "東立出版社",
		Genres:    []string{"少年"},
		Tags:      []string{"籃球", "熱血"},
		Authors:   []Author{{Name: "井上雄彥", Role: "writer"}},
	}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		query SeriesQuery
		want  int64
	}{
		{"simplified query hits traditional name", SeriesQuery{Search: "灌篮高手", Sort: "relevance,asc", Size: 20}, 2},
		{"traditional query hits simplified/traditional names", SeriesQuery{Search: "灌籃高手", Sort: "relevance,asc", Size: 20}, 2},
		{"tag", SeriesQuery{Search: "tag:篮球", Size: 20}, 1},
		{"tag traditional", SeriesQuery{Search: "tag:熱血", Size: 20}, 1},
		{"publisher", SeriesQuery{Search: "publisher:东立出版社", Size: 20}, 1},
		{"author", SeriesQuery{Search: "author:井上雄彦", Size: 20}, 1},
		{"tag filter", SeriesQuery{Tags: []string{"篮球"}, Size: 20}, 1},
		{"publisher filter", SeriesQuery{Publishers: []string{"东立出版社"}, Size: 20}, 1},
		{"author filter", SeriesQuery{Authors: []string{"井上雄彦"}, Size: 20}, 1},
	}
	for _, test := range tests {
		items, total, err := store.SeriesPage(ctx, test.query)
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		if total != test.want {
			t.Fatalf("%s: total=%d want=%d items=%v", test.name, total, test.want, items)
		}
	}

	items, _, err := store.SeriesPage(ctx, SeriesQuery{Search: "灌篮高手", Sort: "relevance,asc", Size: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) < 2 || items[0].ID != "trad" {
		t.Fatalf("relevance should put the exact folded key first, got %v", items)
	}

	books, bookTotal, err := store.BooksPage(ctx, BookQuery{Search: "灌篮高手", Size: 20})
	if err != nil {
		t.Fatal(err)
	}
	if bookTotal != 2 || len(books) != 2 {
		t.Fatalf("book search total=%d len=%d books=%v", bookTotal, len(books), books)
	}
	if _, total, err := store.BooksPage(ctx, BookQuery{Tags: []string{"篮球"}, Size: 20}); err != nil || total != 1 {
		t.Fatalf("book tag filter total=%d err=%v", total, err)
	}

	// Punctuation / stripped-only queries must NEVER fail open.
	for _, emptyQ := range []string{`""`, `'`, `ー`, `〜`} {
		if _, total, err := store.SeriesPage(ctx, SeriesQuery{Search: emptyQ, Size: 20}); err != nil || total != 0 {
			t.Fatalf("stripped query %q: total=%d err=%v, want 0", emptyQ, total, err)
		}
		if authors, total, err := store.AuthorsPage(ctx, AuthorQuery{Search: emptyQ, Size: 20}); err != nil || total != 0 || len(authors) != 0 {
			t.Fatalf("AuthorsPage stripped query %q: total=%d len=%d err=%v, want 0", emptyQ, total, len(authors), err)
		}
	}

	// AuthorsPage & Authors top query fold Traditional/Simplified.
	authors, aTotal, err := store.AuthorsPage(ctx, AuthorQuery{Search: "井上雄彦", Size: 20})
	if err != nil || aTotal != 1 || len(authors) != 1 || authors[0].Name != "井上雄彥" {
		t.Fatalf("AuthorsPage folded search: total=%d authors=%v err=%v", aTotal, authors, err)
	}

	// Collections & ReadLists fold Traditional/Simplified.
	if _, err := store.CreateCollection(ctx, "經典籃球漫畫", false, []string{"trad"}); err != nil {
		t.Fatal(err)
	}
	cols, cTotal, err := store.ListCollections(ctx, "经典篮球", nil, 0, 20, false, "")
	if err != nil || cTotal != 1 || len(cols) != 1 {
		t.Fatalf("ListCollections folded: total=%d len=%d err=%v", cTotal, len(cols), err)
	}
	if _, err := store.CreateReadList(ctx, "灌籃高手補番", "", false, []string{books[0].ID}); err != nil {
		t.Fatal(err)
	}
	rls, rTotal, err := store.ListReadLists(ctx, "灌篮高手补番", nil, 0, 20, false, "")
	if err != nil || rTotal != 1 || len(rls) != 1 {
		t.Fatalf("ListReadLists folded: total=%d len=%d err=%v", rTotal, len(rls), err)
	}

	// Reading history (/history -> BooksPage with ReadStatus IN_PROGRESS/READ) folds CJK.
	completed := false
	page := 5
	if err := store.UpdateBookReadProgress(ctx, books[0], &completed, &page); err != nil {
		t.Fatal(err)
	}
	hist, hTotal, err := store.BooksPage(ctx, BookQuery{
		Search:     "灌篮高手",
		ReadStatus: []string{"IN_PROGRESS", "READ"},
		Sort:       "readProgress.readDate,desc",
		Size:       20,
	})
	if err != nil || hTotal != 1 || len(hist) != 1 {
		t.Fatalf("History BooksPage folded: total=%d len=%d err=%v", hTotal, len(hist), err)
	}
}

func TestSearchFTSBackgroundRebuildAndAlternateTitlesAndKana(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "cjk-fts-rebuild.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.UpsertLibrary(ctx, Library{ID: "lib", Name: "Main", RootCID: "root", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	insertCatalogSeries(t, store, "lib", "s_kana", "葬送のフリーレン", []catalogBookTime{
		{created: "2024-01-01T00:00:00Z", modified: "2024-01-01T00:00:00Z"},
	})
	insertCatalogSeries(t, store, "lib", "s_alt", "Original Title", []catalogBookTime{
		{created: "2024-01-02T00:00:00Z", modified: "2024-01-02T00:00:00Z"},
	})
	sAlt, err := store.SeriesByID(ctx, "s_alt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyScrapedMetadata(ctx, sAlt, ScrapedMetadata{
		SubjectID: 3001,
		Title:     "進撃の巨人",
		TitleSort: "進撃の巨人",
		AlternateTitles: []AlternateTitle{
			{Label: "alias", Title: "進擊的巨人"},
			{Label: "alias", Title: "日々のカルテ"},
		},
		Tags: []string{"校园", "恋爱"},
	}); err != nil {
		t.Fatal(err)
	}

	// Simulate an existing pre-upgrade DB by wiping FTS version markers and reopening.
	if _, err := store.DB().Exec(`DELETE FROM settings WHERE key IN (?, ?)`, searchFTSSeriesVersionKey, searchFTSBooksVersionKey); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.WaitSearchIndexReady(ctx); err != nil {
		t.Fatal(err)
	}

	// 1. Symmetric kana folding (katakana <-> hiragana <-> dropped ー)
	for _, q := range []string{"ふりーれん", "フリレン", "フリーレン", "ふりれん"} {
		items, total, err := reopened.SeriesPage(ctx, SeriesQuery{Search: q, Size: 20})
		if err != nil || total != 1 || items[0].ID != "s_kana" {
			t.Fatalf("kana query %q: total=%d items=%v err=%v", q, total, items, err)
		}
	}
	// 2. AlternateTitles JSON objects + 々 expansion + Shinjitai folding
	for _, q := range []string{"进击的巨人", "进击の巨人", "日日"} {
		items, total, err := reopened.SeriesPage(ctx, SeriesQuery{Search: q, Size: 20})
		if err != nil || total != 1 || items[0].ID != "s_alt" {
			t.Fatalf("alt/shinjitai query %q: total=%d items=%v err=%v", q, total, items, err)
		}
	}
	// 3. Multi-value boundary sentinel prevents false cross-tag phrase matches ("园恋" across "校园" + "恋爱")
	if _, total, err := reopened.SeriesPage(ctx, SeriesQuery{Search: "tag:园恋", Size: 20}); err != nil || total != 0 {
		t.Fatalf("cross-tag boundary should not match: total=%d err=%v", total, err)
	}
	// 4. Deleting a series removes it cleanly from FTS5 via triggers
	if _, err := reopened.DB().Exec(`DELETE FROM series WHERE id='s_kana'`); err != nil {
		t.Fatal(err)
	}
	if _, total, err := reopened.SeriesPage(ctx, SeriesQuery{Search: "フリーレン", Size: 20}); err != nil || total != 0 {
		t.Fatalf("deleted series still matched FTS: total=%d err=%v", total, err)
	}

	// 5. Multi-term BooksPage OR must keep SQL fragments paired with their arguments (no FTS column error)
	if _, total, err := reopened.BooksPage(ctx, BookQuery{Search: "进击 OR 不存在", Size: 20}); err != nil || total != 1 {
		t.Fatalf("BooksPage multi-term OR: total=%d err=%v, want 1", total, err)
	}
	if _, total, err := reopened.BooksPage(ctx, BookQuery{Search: "Alice OR Bob", Size: 20}); err != nil || total != 0 {
		t.Fatalf("BooksPage multi-term OR (empty): total=%d err=%v, want 0", total, err)
	}

	// 6. Negated stripped operand (NOT ー / -"") must NOT expand to TRUE / whole catalog
	for _, negQ := range []string{"NOT ー", "-ー", `NOT ""`} {
		if _, total, err := reopened.SeriesPage(ctx, SeriesQuery{Search: negQ, Size: 20}); err != nil || total != 0 {
			t.Fatalf("SeriesPage %q: total=%d err=%v, want 0", negQ, total, err)
		}
		if _, total, err := reopened.BooksPage(ctx, BookQuery{Search: negQ, Size: 20}); err != nil || total != 0 {
			t.Fatalf("BooksPage %q: total=%d err=%v, want 0", negQ, total, err)
		}
	}

	// 7. Internal multi-value / CamelCase separator must NOT match normal letter queries like "z"
	insertCatalogSeries(t, reopened, "lib", "s_camel", "SecretManga", []catalogBookTime{
		{created: "2024-01-03T00:00:00Z", modified: "2024-01-03T00:00:00Z"},
	})
	if _, total, err := reopened.SeriesPage(ctx, SeriesQuery{Search: "z", Size: 20}); err != nil || total != 0 {
		t.Fatalf("query 'z' should not match internal FTS separator: total=%d err=%v", total, err)
	}
	if items, total, err := reopened.SeriesPage(ctx, SeriesQuery{Search: "Manga", Size: 20}); err != nil || total != 1 || items[0].ID != "s_camel" {
		t.Fatalf("CamelCase split search 'Manga': total=%d items=%v err=%v", total, items, err)
	}

	// 8. Unchanged scanner/rank-backfill UPDATEs must NOT enqueue dirty rows
	if err := reopened.WaitSearchIndexReady(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.DB().Exec(`UPDATE series SET name=name, updated_at='2026-01-01T00:00:00Z' WHERE id='s_alt'`); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.DB().Exec(`UPDATE books SET name=name, updated_at='2026-01-01T00:00:00Z' WHERE series_id='s_alt'`); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.DB().Exec(`UPDATE series_metadata SET title=title, bgm_rank=42 WHERE series_id='s_alt'`); err != nil {
		t.Fatal(err)
	}
	var dirtyCnt int
	if err := reopened.DB().QueryRow(`SELECT count(*) FROM search_fts_dirty`).Scan(&dirtyCnt); err != nil {
		t.Fatal(err)
	}
	if dirtyCnt != 0 {
		t.Fatalf("unchanged UPDATEs enqueued %d dirty FTS rows, want 0", dirtyCnt)
	}
}

func TestSearchFTSLiveCopyBenchmark(t *testing.T) {
	dbPath := os.Getenv("FK115_BENCH_DB")
	if dbPath == "" {
		t.Skip("set FK115_BENCH_DB to an isolated copy of a real DB to run benchmark")
	}
	infoBefore, err := os.Stat(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	tOpen := time.Now()
	store, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	openDur := time.Since(tOpen)
	defer store.Close()
	t.Logf("Open() returned in %v (before=%.1f MB)", openDur, float64(infoBefore.Size())/1048576.0)

	ctx := context.Background()
	tWait := time.Now()
	var seriesDur, booksDur time.Duration
	for {
		sr, br := store.SearchIndexReady()
		if sr && seriesDur == 0 {
			seriesDur = time.Since(tWait)
			t.Logf("series_search_fts READY in %v", seriesDur)
		}
		if br && booksDur == 0 {
			booksDur = time.Since(tWait)
			t.Logf("books_search_fts READY in %v", booksDur)
		}
		if sr && br {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := store.WaitSearchIndexReady(ctx); err != nil {
		t.Fatal(err)
	}
	_, _ = store.DB().Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	infoAfter, _ := os.Stat(dbPath)
	t.Logf("After full FTS5 build: size=%.1f MB (delta=+%.1f MB)",
		float64(infoAfter.Size())/1048576.0,
		float64(infoAfter.Size()-infoBefore.Size())/1048576.0)

	seriesBench := []SeriesQuery{
		{Search: "灌篮高手", Sort: "relevance,asc", Size: 36},
		{Search: "灌籃高手", Sort: "relevance,asc", Size: 36},
		{Search: "(灌篮) OR tag:(灌篮)", Sort: "relevance,asc", Size: 36},
		{Search: "葬送のふりーれん", Sort: "relevance,asc", Size: 36},
		{Search: "进击の巨人", Sort: "relevance,asc", Size: 36},
		{Search: "tag:恋爱", Size: 36},
		{Search: "author:井上雄彦", Size: 36},
		{Publishers: []string{"东立出版社"}, Size: 36},
		{Tags: []string{"篮球"}, Size: 36},
	}
	for _, q := range seriesBench {
		t0 := time.Now()
		items, total, err := store.SeriesPage(ctx, q)
		if err != nil {
			t.Fatalf("SeriesPage %+v: %v", q, err)
		}
		t.Logf("SeriesPage search=%q tags=%v pubs=%v -> total=%d len=%d in %v",
			q.Search, q.Tags, q.Publishers, total, len(items), time.Since(t0))
	}

	bookBench := []BookQuery{
		{Search: "灌篮高手", Sort: "relevance,asc", Size: 36},
		{Search: "灌籃高手", Sort: "relevance,asc", Size: 36},
		{Search: "(灌篮) OR tag:(灌篮)", Sort: "relevance,asc", Size: 36},
	}
	for _, q := range bookBench {
		t0 := time.Now()
		items, total, err := store.BooksPage(ctx, q)
		if err != nil {
			t.Fatalf("BooksPage %+v: %v", q, err)
		}
		t.Logf("BooksPage search=%q -> total=%d len=%d in %v", q.Search, total, len(items), time.Since(t0))
	}

	t0 := time.Now()
	authors, aTotal, err := store.AuthorsPage(ctx, AuthorQuery{Search: "井上雄彦", Size: 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("AuthorsPage search=井上雄彦 -> total=%d len=%d in %v", aTotal, len(authors), time.Since(t0))
}
