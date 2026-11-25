package database

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func indexExists(db *sql.DB, name string) (bool, error) {
	var found string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func TestFreshOpenAndIdempotentReopenMetadataTables(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fresh.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open fresh db: %v", err)
	}

	for _, tbl := range []string{"metadata_repos", "metadata_sources", "library_metadata_sources"} {
		ok, err := tableExists(store.DB(), tbl)
		if err != nil || !ok {
			t.Fatalf("expected table %s to exist, ok=%v err=%v", tbl, ok, err)
		}
	}
	for _, idx := range []string{"idx_lib_meta_sources_prio", "idx_series_metadata_ext"} {
		ok, err := indexExists(store.DB(), idx)
		if err != nil || !ok {
			t.Fatalf("expected index %s to exist, ok=%v err=%v", idx, ok, err)
		}
	}
	for _, col := range []string{"ext_source_id", "ext_entry_id", "ext_cover_url", "ext_cover_path", "match_origin"} {
		ok, err := columnExists(store.DB(), "series_metadata", col)
		if err != nil || !ok {
			t.Fatalf("expected series_metadata.%s to exist, ok=%v err=%v", col, ok, err)
		}
	}
	assertPartial := func(store *Store) {
		t.Helper()
		var partial bool
		err := store.DB().QueryRow(`SELECT "partial" FROM pragma_index_list('series_metadata') WHERE name='idx_series_metadata_ext'`).Scan(&partial)
		if err != nil || !partial {
			t.Fatalf("expected partial external metadata index, got partial=%v err=%v", partial, err)
		}
	}
	assertPartial(store)
	if _, err := store.DB().Exec(`
DROP INDEX idx_series_metadata_ext;
CREATE INDEX idx_series_metadata_ext ON series_metadata(ext_source_id, ext_entry_id);`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close first open: %v", err)
	}

	// Re-open must be 100% idempotent.
	reopened, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer reopened.Close()
	assertPartial(reopened)
	ok, err := tableExists(reopened.DB(), "metadata_repos")
	if err != nil || !ok {
		t.Fatalf("metadata_repos missing after reopen: ok=%v err=%v", ok, err)
	}
}

func TestMigrateV311ToV32PreservesAllDataAndIsIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "v311.db")
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath))
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	now := nowText()
	pwHash, err := hashPassword("V311Pass!123")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}

	// Construct an authentic v3.1.1 database without the 3 new tables or 5 new series_metadata columns.
	v311DDL := `
PRAGMA foreign_keys = ON;
CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE users (
  id TEXT PRIMARY KEY,
  username TEXT NOT NULL UNIQUE COLLATE NOCASE,
  password_hash TEXT NOT NULL,
  role TEXT NOT NULL DEFAULT 'user',
  enabled INTEGER NOT NULL DEFAULT 1,
  all_libraries INTEGER NOT NULL DEFAULT 1,
  allow_nsfw INTEGER NOT NULL DEFAULT 0,
  show_nsfw INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE auth_admin (
  id INTEGER PRIMARY KEY CHECK(id = 1),
  username TEXT NOT NULL,
  password_hash TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE libraries (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  provider_type TEXT NOT NULL DEFAULT '115',
  root_cid TEXT NOT NULL UNIQUE,
  webdav_account_id TEXT NOT NULL DEFAULT '',
  root_path TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1,
  one_shot INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  last_scan_started_at TEXT,
  last_scan_completed_at TEXT,
  last_scan_status TEXT NOT NULL DEFAULT 'never',
  last_scan_error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE series (
  id TEXT PRIMARY KEY,
  library_id TEXT NOT NULL,
  cid TEXT NOT NULL,
  name TEXT NOT NULL,
  relative_path TEXT NOT NULL,
  one_shot INTEGER NOT NULL DEFAULT 0,
  file_modified_at TEXT,
  books_count INTEGER NOT NULL DEFAULT 0,
  latest_book_modified_at TEXT,
  latest_book_created_at TEXT,
  sort_modified_at TEXT NOT NULL DEFAULT '',
  sort_created_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  seen_scan_id TEXT NOT NULL,
  UNIQUE(library_id, cid),
  FOREIGN KEY(library_id) REFERENCES libraries(id) ON DELETE CASCADE
);
CREATE TABLE books (
  id TEXT PRIMARY KEY,
  series_id TEXT NOT NULL,
  library_id TEXT NOT NULL,
  file_id TEXT NOT NULL,
  parent_cid TEXT NOT NULL,
  name TEXT NOT NULL,
  size INTEGER NOT NULL,
  pick_code TEXT NOT NULL,
  sha1 TEXT NOT NULL DEFAULT '',
  file_created_at TEXT,
  file_modified_at TEXT,
  number_sort REAL NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  seen_scan_id TEXT NOT NULL,
  UNIQUE(library_id, file_id),
  FOREIGN KEY(series_id) REFERENCES series(id) ON DELETE CASCADE,
  FOREIGN KEY(library_id) REFERENCES libraries(id) ON DELETE CASCADE
);
CREATE TABLE series_metadata (
  series_id TEXT PRIMARY KEY,
  bgm_subject_id INTEGER,
  scrape_status TEXT NOT NULL DEFAULT 'none',
  scrape_confidence REAL NOT NULL DEFAULT 0,
  scrape_synced_at TEXT,
  cover_source TEXT NOT NULL DEFAULT 'auto',
  bgm_cover_url TEXT NOT NULL DEFAULT '',
  bgm_cover_path TEXT NOT NULL DEFAULT '',
  bgm_score REAL,
  bgm_rank INTEGER,
  status TEXT NOT NULL DEFAULT 'ONGOING',
  status_lock INTEGER NOT NULL DEFAULT 0,
  title TEXT NOT NULL DEFAULT '',
  title_lock INTEGER NOT NULL DEFAULT 0,
  title_sort TEXT NOT NULL DEFAULT '',
  title_sort_lock INTEGER NOT NULL DEFAULT 0,
  summary TEXT NOT NULL DEFAULT '',
  summary_lock INTEGER NOT NULL DEFAULT 0,
  reading_direction TEXT NOT NULL DEFAULT 'LEFT_TO_RIGHT',
  reading_direction_lock INTEGER NOT NULL DEFAULT 0,
  publisher TEXT NOT NULL DEFAULT '',
  publisher_lock INTEGER NOT NULL DEFAULT 0,
  age_rating INTEGER,
  age_rating_lock INTEGER NOT NULL DEFAULT 0,
  language TEXT NOT NULL DEFAULT '',
  language_lock INTEGER NOT NULL DEFAULT 0,
  total_book_count INTEGER,
  total_book_count_lock INTEGER NOT NULL DEFAULT 0,
  release_date TEXT NOT NULL DEFAULT '',
  release_date_lock INTEGER NOT NULL DEFAULT 0,
  genres_json TEXT NOT NULL DEFAULT '[]',
  genres_lock INTEGER NOT NULL DEFAULT 0,
  tags_json TEXT NOT NULL DEFAULT '[]',
  tags_lock INTEGER NOT NULL DEFAULT 0,
  authors_json TEXT NOT NULL DEFAULT '[]',
  authors_lock INTEGER NOT NULL DEFAULT 0,
  alternate_titles_json TEXT NOT NULL DEFAULT '[]',
  alternate_titles_lock INTEGER NOT NULL DEFAULT 0,
  links_json TEXT NOT NULL DEFAULT '[]',
  links_lock INTEGER NOT NULL DEFAULT 0,
  sharing_labels_json TEXT NOT NULL DEFAULT '[]',
  sharing_labels_lock INTEGER NOT NULL DEFAULT 0,
  is_nsfw INTEGER NOT NULL DEFAULT 0,
  nsfw_lock INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL,
  FOREIGN KEY(series_id) REFERENCES series(id) ON DELETE CASCADE
);
CREATE TABLE collections (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL DEFAULT 'admin',
  name TEXT NOT NULL COLLATE NOCASE,
  ordered INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(user_id, name COLLATE NOCASE)
);
CREATE TABLE collection_series (
  collection_id TEXT NOT NULL,
  series_id TEXT NOT NULL,
  position INTEGER NOT NULL,
  PRIMARY KEY(collection_id, series_id),
  FOREIGN KEY(collection_id) REFERENCES collections(id) ON DELETE CASCADE,
  FOREIGN KEY(series_id) REFERENCES series(id) ON DELETE CASCADE
);
CREATE TABLE book_read_progress (
  user_id TEXT NOT NULL DEFAULT 'admin',
  book_id TEXT NOT NULL,
  series_id TEXT NOT NULL,
  completed INTEGER NOT NULL DEFAULT 1,
  page INTEGER,
  read_date TEXT,
  completed_at TEXT,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(user_id, book_id),
  FOREIGN KEY(book_id) REFERENCES books(id) ON DELETE CASCADE,
  FOREIGN KEY(series_id) REFERENCES series(id) ON DELETE CASCADE
);`
	if _, err := raw.Exec(v311DDL); err != nil {
		raw.Close()
		t.Fatalf("seed v3.1.1 schema: %v", err)
	}
	if _, err := raw.Exec(`
INSERT INTO users(id,username,password_hash,role,enabled,all_libraries,allow_nsfw,show_nsfw,created_at,updated_at)
VALUES('admin','owner_v311',?,'admin',1,1,1,0,?,?);
INSERT INTO auth_admin(id,username,password_hash,created_at,updated_at)
VALUES(1,'owner_v311',?,?,?);
INSERT INTO libraries(id,name,provider_type,root_cid,enabled,created_at,updated_at)
VALUES('l_legacy115','旧版书库','115','987654',1,?,?);
INSERT INTO series(id,library_id,cid,name,relative_path,books_count,created_at,updated_at,seen_scan_id)
VALUES('s_aria','l_legacy115','111','ARIA','ARIA',1,?,?,'sc1');
INSERT INTO books(id,series_id,library_id,file_id,parent_cid,name,size,pick_code,number_sort,created_at,updated_at,seen_scan_id)
VALUES('b_aria_1','s_aria','l_legacy115','f1','111','Vol.01.cbz',1024,'pk1',1,?,?,'sc1');
INSERT INTO series_metadata(series_id,bgm_subject_id,scrape_status,scrape_confidence,title,title_lock,publisher,is_nsfw,updated_at)
VALUES('s_aria',539,'matched',0.99,'水星领航员',1,'Mag Garden',0,?);
INSERT INTO collections(rowid,id,user_id,name,ordered,created_at,updated_at)
VALUES(17,'col_fav','admin','治愈必看',1,?,?);
INSERT INTO collection_series(collection_id,series_id,position)
VALUES('col_fav','s_aria',0);
INSERT INTO book_read_progress(user_id,book_id,series_id,completed,page,read_date,updated_at)
VALUES('admin','b_aria_1','s_aria',0,12,?,?);
`, pwHash, now, now, now, now, now, now, now, now, now, now, now, now, now, now, now); err != nil {
		raw.Close()
		t.Fatalf("seed v3.1.1 rows: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw v3.1.1 db: %v", err)
	}

	// First upgrade Open.
	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open v3.1.1 upgrade failed: %v", err)
	}

	ctx := t.Context()
	for _, tbl := range []string{"metadata_repos", "metadata_sources", "library_metadata_sources"} {
		ok, err := tableExists(store.DB(), tbl)
		if err != nil || !ok {
			t.Fatalf("missing table %s after v3.1.1 upgrade: ok=%v err=%v", tbl, ok, err)
		}
	}
	for _, idx := range []string{"idx_lib_meta_sources_prio", "idx_series_metadata_ext"} {
		ok, err := indexExists(store.DB(), idx)
		if err != nil || !ok {
			t.Fatalf("missing index %s after v3.1.1 upgrade: ok=%v err=%v", idx, ok, err)
		}
	}
	for _, col := range []string{"ext_source_id", "ext_entry_id", "ext_cover_url", "ext_cover_path", "match_origin"} {
		ok, err := columnExists(store.DB(), "series_metadata", col)
		if err != nil || !ok {
			t.Fatalf("missing series_metadata.%s after upgrade: ok=%v err=%v", col, ok, err)
		}
	}

	// Verify existing v3.1.1 data remained completely intact.
	admin, ok, err := store.Admin(ctx)
	if err != nil || !ok || admin.Username != "owner_v311" {
		t.Fatalf("admin corrupted after upgrade: ok=%v err=%v admin=%+v", ok, err, admin)
	}
	lib, err := store.Library(ctx, "l_legacy115")
	if err != nil || lib.Name != "旧版书库" || lib.RootCID != "987654" {
		t.Fatalf("library corrupted after upgrade: err=%v lib=%+v", err, lib)
	}
	ser, err := store.SeriesByID(ctx, "s_aria")
	if err != nil || ser.Name != "ARIA" {
		t.Fatalf("series corrupted after upgrade: err=%v ser=%+v", err, ser)
	}
	meta, err := store.SeriesMetadataByID(ctx, ser)
	if err != nil || meta.BgmSubjectID == nil || *meta.BgmSubjectID != 539 ||
		meta.Title != "水星领航员" || !meta.TitleLock || meta.Publisher != "Mag Garden" ||
		meta.ExtSourceID != "" || meta.ExtEntryID != "" || meta.ExtCoverURL != "" || meta.ExtCoverPath != "" || meta.MatchOrigin != "manual" {
		t.Fatalf("series_metadata corrupted after upgrade: err=%v meta=%+v", err, meta)
	}
	var colRowID int64
	if err := store.DB().QueryRow(`SELECT rowid FROM collections WHERE id='col_fav'`).Scan(&colRowID); err != nil || colRowID != 17 {
		t.Fatalf("collections.rowid corrupted after upgrade: rowid=%d err=%v", colRowID, err)
	}
	col, err := store.CollectionByID(ctx, "col_fav")
	if err != nil || len(col.SeriesIDs) != 1 || col.SeriesIDs[0] != "s_aria" {
		t.Fatalf("collection_series corrupted after upgrade: err=%v col=%+v", err, col)
	}
	prog, hasProg, err := store.BookReadProgress(ctx, "b_aria_1")
	if err != nil || !hasProg || prog.Page == nil || *prog.Page != 12 {
		t.Fatalf("book_read_progress corrupted after upgrade: err=%v ok=%v prog=%+v", err, hasProg, prog)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close after first upgrade: %v", err)
	}

	// Second Open must be 100% idempotent and preserve everything.
	store2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("second Open on upgraded db failed: %v", err)
	}
	defer store2.Close()
	meta2, err := store2.SeriesMetadataByID(ctx, ser)
	if err != nil || meta2.BgmSubjectID == nil || *meta2.BgmSubjectID != 539 || meta2.Title != "水星领航员" || meta2.MatchOrigin != "manual" {
		t.Fatalf("series_metadata lost after second Open: err=%v meta=%+v", err, meta2)
	}
}

func TestMetadataRepoAndSourceCRUDAndCascadeDelete(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "repo_crud.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := t.Context()

	// Repo CRUD
	repo1, err := store.UpsertMetadataRepo(ctx, MetadataRepo{
		Name:        "社区镜像源",
		URL:         "https://example.com/repo1.json",
		RepoVersion: "2026.11",
		Description: "默认外部元数据仓库",
		Homepage:    "https://example.com",
		RawJSON:     `{"schema":"mangaalpha-repo-v1"}`,
		Enabled:     true,
	})
	if err != nil {
		t.Fatalf("UpsertMetadataRepo repo1: %v", err)
	}
	if repo1.ID == "" || repo1.SchemaVersion != "mangaalpha-repo-v1" || !repo1.Enabled {
		t.Fatalf("unexpected repo1: %+v", repo1)
	}

	// Upsert by same URL without ID updates existing record and preserves CreatedAt.
	repo1Updated, err := store.UpsertMetadataRepo(ctx, MetadataRepo{
		Name:         "社区镜像源（已更新）",
		URL:          "https://example.com/repo1.json",
		RepoVersion:  "2026.12",
		Enabled:      true,
		LastSyncedAt: "2026-11-25T10:00:00Z",
	})
	if err != nil {
		t.Fatalf("UpsertMetadataRepo update by url: %v", err)
	}
	if repo1Updated.ID != repo1.ID || repo1Updated.CreatedAt != repo1.CreatedAt || repo1Updated.RepoVersion != "2026.12" {
		t.Fatalf("expected repo1 updated preserving id/createdAt: before=%+v after=%+v", repo1, repo1Updated)
	}

	repo2, err := store.UpsertMetadataRepo(ctx, MetadataRepo{
		ID:      "repo_keep",
		Name:    "保留仓库",
		URL:     "https://example.com/repo2.json",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("UpsertMetadataRepo repo2: %v", err)
	}

	repos, err := store.ListMetadataRepos(ctx)
	if err != nil || len(repos) != 2 {
		t.Fatalf("ListMetadataRepos: len=%d err=%v", len(repos), err)
	}
	gotRepo, err := store.GetMetadataRepo(ctx, repo1.ID)
	if err != nil || gotRepo.Name != "社区镜像源（已更新）" {
		t.Fatalf("GetMetadataRepo: err=%v repo=%+v", err, gotRepo)
	}

	// Source CRUD
	srcA, err := store.UpsertMetadataSource(ctx, MetadataSource{
		ID:            "src_a",
		RepoID:        repo1.ID,
		SourceKey:     "alpha_sqlite",
		Kind:          "offline_sqlite",
		SchemaType:    "gallery_v1",
		Name:          "图库离线索引 A",
		DownloadURL:   "https://example.com/a.db.zst",
		Compression:   "zstd",
		OnlineEnabled: false,
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("UpsertMetadataSource srcA: %v", err)
	}
	if srcA.TagDisplayMode != "zh" || srcA.NSFWRule != "non_h_safe" || srcA.AutoUpdate != "manual" {
		t.Fatalf("unexpected source defaults: %+v", srcA)
	}

	srcB, err := store.UpsertMetadataSource(ctx, MetadataSource{
		ID:          "src_b",
		RepoID:      repo1.ID,
		SourceKey:   "tag_dict_zh",
		Kind:        "tag_dictionary",
		Name:        "中文标签词典",
		DownloadURL: "https://example.com/db.text.json.gz",
		Compression: "gzip",
		Enabled:     true,
	})
	if err != nil {
		t.Fatalf("UpsertMetadataSource srcB: %v", err)
	}

	srcKeep, err := store.UpsertMetadataSource(ctx, MetadataSource{
		ID:        "src_keep",
		RepoID:    repo2.ID,
		SourceKey: "keep_pack",
		Kind:      "mapping_pack",
		Name:      "社区映射包",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("UpsertMetadataSource srcKeep: %v", err)
	}

	allSrcs, err := store.ListMetadataSources(ctx, "")
	if err != nil || len(allSrcs) != 3 {
		t.Fatalf("ListMetadataSources all: len=%d err=%v", len(allSrcs), err)
	}
	dictSrcs, err := store.ListMetadataSources(ctx, "tag_dictionary")
	if err != nil || len(dictSrcs) != 1 || dictSrcs[0].ID != srcB.ID {
		t.Fatalf("ListMetadataSources tag_dictionary: %+v err=%v", dictSrcs, err)
	}

	// UpdateMetadataSourceConfig + validation
	if _, err := store.UpdateMetadataSourceConfig(ctx, srcA.ID, true, srcB.ID, "invalid_mode", "weekly", "", "", "", false); err == nil {
		t.Fatalf("expected invalid tagDisplayMode error")
	}
	updatedCfg, err := store.UpdateMetadataSourceConfig(
		ctx, srcA.ID, false, srcB.ID, "bilingual", "weekly", "/cache/a.db",
		"https://example.com/a-v2.db.zst", "https://api.example.com/v1", true,
	)
	if err != nil {
		t.Fatalf("UpdateMetadataSourceConfig: %v", err)
	}
	if updatedCfg.Enabled || updatedCfg.TagDictSourceID != srcB.ID || updatedCfg.TagDisplayMode != "bilingual" ||
		updatedCfg.AutoUpdate != "weekly" || updatedCfg.LocalPath != "/cache/a.db" ||
		updatedCfg.DownloadURL != "https://example.com/a-v2.db.zst" ||
		updatedCfg.OnlineAPIURL != "https://api.example.com/v1" || !updatedCfg.OnlineEnabled {
		t.Fatalf("unexpected updated config: %+v", updatedCfg)
	}

	// UpdateMetadataSourceSyncState: success then transient check error preserving Ready/EntryCount
	readyTrue := true
	entries := int64(123456)
	fileSize := int64(4096000)
	sidecar := "/cache/a.sidecar.db"
	etag := `"etag-v1"`
	sha := "abcdef0123456789"
	checkedAt := "2026-11-25T10:10:00Z"
	updatedAt := "2026-11-25T10:10:05Z"
	emptyErr := ""
	synced, err := store.UpdateMetadataSourceSyncState(ctx, srcA.ID, MetadataSourceSyncState{
		SidecarPath:   &sidecar,
		Ready:         &readyTrue,
		EntryCount:    &entries,
		FileSize:      &fileSize,
		RemoteETag:    &etag,
		LocalSHA256:   &sha,
		LastCheckedAt: &checkedAt,
		LastUpdatedAt: &updatedAt,
		LastError:     &emptyErr,
	})
	if err != nil {
		t.Fatalf("UpdateMetadataSourceSyncState ready: %v", err)
	}
	if !synced.Ready || synced.EntryCount != 123456 || synced.SidecarPath != sidecar || synced.RemoteETag != etag {
		t.Fatalf("unexpected sync state: %+v", synced)
	}

	transientErr := "timeout"
	checkedAt2 := "2026-11-25T11:00:00Z"
	afterErr, err := store.UpdateMetadataSourceSyncState(ctx, srcA.ID, MetadataSourceSyncState{
		LastCheckedAt: &checkedAt2,
		LastError:     &transientErr,
	})
	if err != nil {
		t.Fatalf("UpdateMetadataSourceSyncState partial error: %v", err)
	}
	if !afterErr.Ready || afterErr.EntryCount != 123456 || afterErr.LastError != "timeout" || afterErr.LastCheckedAt != checkedAt2 {
		t.Fatalf("partial sync state clobbered existing fields: %+v", afterErr)
	}

	// Attach all 3 sources to lib_1, then test DeleteMetadataSource & DeleteMetadataRepo cascade cleanup.
	if err := store.UpsertLibrary(ctx, Library{ID: "lib_1", Name: "Fixture", RootCID: "root", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetLibraryMetadataSources(ctx, "lib_1", []string{srcA.ID, srcB.ID, srcKeep.ID}); err != nil {
		t.Fatalf("SetLibraryMetadataSources: %v", err)
	}
	bgmID := int64(42)
	for _, fixture := range []struct {
		id, source string
		subjectID  *int64
	}{
		{"source-only", srcB.ID, nil},
		{"repo-only", srcA.ID, nil},
		{"repo-with-bangumi", srcA.ID, &bgmID},
		{"retained", srcKeep.ID, nil},
	} {
		now := nowText()
		if _, err := store.DB().ExecContext(ctx, `
INSERT INTO series(id,library_id,cid,name,relative_path,created_at,updated_at,seen_scan_id)
VALUES(?,'lib_1',?,?,?, ?,?,'scan')`, fixture.id, fixture.id, fixture.id, fixture.id, now, now); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveSeriesMetadata(ctx, SeriesMetadata{
			SeriesID: fixture.id, BgmSubjectID: fixture.subjectID, ExtSourceID: fixture.source, ExtEntryID: "entry",
			ExtCoverURL: "https://cover.example.test/image.png", ExtCoverPath: "cover.png", MatchOrigin: "manual",
			ScrapeStatus: ScrapeStatusMatched, ScrapeConfidence: 0.9,
		}); err != nil {
			t.Fatal(err)
		}
	}
	read := func(id string) SeriesMetadata {
		t.Helper()
		meta, err := store.SeriesMetadataByID(ctx, Series{ID: id})
		if err != nil {
			t.Fatal(err)
		}
		return meta
	}
	assertCleared := func(id string) SeriesMetadata {
		t.Helper()
		meta := read(id)
		if meta.ExtSourceID != "" || meta.ExtEntryID != "" || meta.ExtCoverURL != "" || meta.ExtCoverPath != "" {
			t.Fatalf("external binding remained after deletion: %+v", meta)
		}
		return meta
	}
	if err := store.DeleteMetadataSource(ctx, srcB.ID); err != nil {
		t.Fatalf("DeleteMetadataSource(srcB): %v", err)
	}
	if _, err := store.GetMetadataSource(ctx, srcB.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected ErrNoRows for deleted source, got %v", err)
	}
	chainAfterSrcDel, err := store.ListLibraryMetadataSources(ctx, "lib_1")
	if err != nil || len(chainAfterSrcDel) != 2 || chainAfterSrcDel[0].SourceID != srcA.ID || chainAfterSrcDel[1].SourceID != srcKeep.ID {
		t.Fatalf("expected srcB removed from library chain, got %+v err=%v", chainAfterSrcDel, err)
	}
	if meta := assertCleared("source-only"); meta.MatchOrigin != "" || meta.ScrapeStatus != ScrapeStatusNone || meta.ScrapeConfidence != 0 {
		t.Fatalf("source-only match state remained: %+v", meta)
	}

	// Deleting repo1 must cascade-delete srcA and its library_metadata_sources link, keeping repo2 & srcKeep.
	if err := store.DeleteMetadataRepo(ctx, repo1.ID); err != nil {
		t.Fatalf("DeleteMetadataRepo(repo1): %v", err)
	}
	if _, err := store.GetMetadataRepo(ctx, repo1.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected ErrNoRows for deleted repo, got %v", err)
	}
	if _, err := store.GetMetadataSource(ctx, srcA.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected srcA cascade deleted with repo1, got %v", err)
	}
	chainAfterRepoDel, err := store.ListLibraryMetadataSources(ctx, "lib_1")
	if err != nil || len(chainAfterRepoDel) != 1 || chainAfterRepoDel[0].SourceID != srcKeep.ID {
		t.Fatalf("expected only srcKeep remaining in chain after repo1 delete, got %+v err=%v", chainAfterRepoDel, err)
	}
	if meta := assertCleared("repo-only"); meta.MatchOrigin != "" || meta.ScrapeStatus != ScrapeStatusNone || meta.ScrapeConfidence != 0 {
		t.Fatalf("repo-only match state remained: %+v", meta)
	}
	if meta := assertCleared("repo-with-bangumi"); meta.BgmSubjectID == nil || *meta.BgmSubjectID != bgmID ||
		meta.MatchOrigin != "manual" || meta.ScrapeStatus != ScrapeStatusMatched || meta.ScrapeConfidence != 0.9 {
		t.Fatalf("Bangumi match state changed: %+v", meta)
	}
	if meta := read("retained"); meta.ExtSourceID != srcKeep.ID || meta.ExtEntryID != "entry" {
		t.Fatalf("unrelated binding changed: %+v", meta)
	}
	if err := store.DeleteMetadataRepo(ctx, "nonexistent"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected ErrNoRows deleting missing repo, got %v", err)
	}
	if err := store.DeleteMetadataSource(ctx, "nonexistent"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected ErrNoRows deleting missing source, got %v", err)
	}
}

func TestLibraryMetadataSourceChainOrderingAndDeduplication(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "chains.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := t.Context()

	for _, src := range []MetadataSource{
		{ID: "src_1", Kind: "offline_sqlite", Name: "源一", Enabled: true, Ready: true},
		{ID: "src_2", Kind: "offline_sqlite", Name: "源二", Enabled: true, Ready: false},
		{ID: "src_3", Kind: "bangumi_archive", Name: "Bangumi", Enabled: true, Ready: true},
	} {
		if _, err := store.UpsertMetadataSource(ctx, src); err != nil {
			t.Fatal(err)
		}
	}

	if err := store.SetLibraryMetadataSources(ctx, "lib_a", []string{"src_3", "src_1", "src_3", "", "  src_2  ", "src_1"}); err != nil {
		t.Fatalf("SetLibraryMetadataSources lib_a: %v", err)
	}
	if err := store.SetLibraryMetadataSources(ctx, "lib_b", []string{"src_2", "src_1"}); err != nil {
		t.Fatalf("SetLibraryMetadataSources lib_b: %v", err)
	}

	chainA, err := store.ListLibraryMetadataSources(ctx, "lib_a")
	if err != nil || len(chainA) != 3 {
		t.Fatalf("ListLibraryMetadataSources lib_a: len=%d err=%v", len(chainA), err)
	}
	wantOrder := []struct {
		id    string
		prio  int
		ready bool
	}{
		{"src_3", 1, true},
		{"src_1", 2, true},
		{"src_2", 3, false},
	}
	for i, w := range wantOrder {
		if chainA[i].SourceID != w.id || chainA[i].Priority != w.prio || !chainA[i].Enabled || chainA[i].SourceReady != w.ready {
			t.Fatalf("chainA[%d] = %+v, want id=%s prio=%d ready=%v", i, chainA[i], w.id, w.prio, w.ready)
		}
	}

	allChains, err := store.ListAllLibraryMetadataChains(ctx)
	if err != nil || len(allChains) != 2 || len(allChains["lib_a"]) != 3 || len(allChains["lib_b"]) != 2 {
		t.Fatalf("ListAllLibraryMetadataChains: %+v err=%v", allChains, err)
	}
	if allChains["lib_b"][0].SourceID != "src_2" || allChains["lib_b"][0].Priority != 1 ||
		allChains["lib_b"][1].SourceID != "src_1" || allChains["lib_b"][1].Priority != 2 {
		t.Fatalf("unexpected lib_b chain: %+v", allChains["lib_b"])
	}

	// Replace lib_a atomically with a single source, then clear.
	if err := store.SetLibraryMetadataSources(ctx, "lib_a", []string{"src_2"}); err != nil {
		t.Fatal(err)
	}
	chainA, err = store.ListLibraryMetadataSources(ctx, "lib_a")
	if err != nil || len(chainA) != 1 || chainA[0].SourceID != "src_2" || chainA[0].Priority != 1 {
		t.Fatalf("expected replaced chain [src_2@1], got %+v err=%v", chainA, err)
	}
	if err := store.SetLibraryMetadataSources(ctx, "lib_a", nil); err != nil {
		t.Fatal(err)
	}
	chainA, err = store.ListLibraryMetadataSources(ctx, "lib_a")
	if err != nil || len(chainA) != 0 {
		t.Fatalf("expected empty chain after clear, got %+v err=%v", chainA, err)
	}
}

func TestSeriesMetadataExternalFieldsRoundTrip(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "ext_roundtrip.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := t.Context()

	if err := store.UpsertLibrary(ctx, Library{
		ID: "lib_ext", Name: "外源测试库", ProviderType: ProviderLocal,
		RootCID: "local:/ext", RootPath: "/ext", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	now := nowText()
	if _, err := store.DB().ExecContext(ctx, `
INSERT INTO series(id,library_id,cid,name,relative_path,one_shot,created_at,updated_at,seen_scan_id) VALUES
('s_ext_1','lib_ext','c1','Series One','Ext/Series One',0,?,?,'sc1'),
('s_ext_2','lib_ext','c2','Series Two','Ext/Series Two',0,?,?,'sc1')`, now, now, now, now); err != nil {
		t.Fatal(err)
	}

	s1, err := store.SeriesByID(ctx, "s_ext_1")
	if err != nil {
		t.Fatal(err)
	}
	s2, err := store.SeriesByID(ctx, "s_ext_2")
	if err != nil {
		t.Fatal(err)
	}

	// 1. ApplyScrapedMetadata with external source fields (without clobbering BgmSubjectID when SubjectID==0).
	applied, err := store.ApplyScrapedMetadata(ctx, s1, ScrapedMetadata{
		ExtSourceID: "src_gallery",
		ExtEntryID:  "987654",
		ExtCoverURL: "https://cdn.example.com/thumb/987654.jpg",
		MatchOrigin: "auto",
		Confidence:  0.95,
		Title:       "测试外源作品",
		TitleSort:   "测试外源作品",
		Authors:     []Author{{Name: "画师A", Role: "artist"}},
		Tags:        []string{"全彩"},
	})
	if err != nil {
		t.Fatalf("ApplyScrapedMetadata ext: %v", err)
	}
	if applied.BgmSubjectID != nil {
		t.Fatalf("expected BgmSubjectID=nil for external-only match, got %v", *applied.BgmSubjectID)
	}
	if applied.ExtSourceID != "src_gallery" || applied.ExtEntryID != "987654" ||
		applied.ExtCoverURL != "https://cdn.example.com/thumb/987654.jpg" ||
		applied.MatchOrigin != "auto" || applied.ScrapeStatus != ScrapeStatusMatched {
		t.Fatalf("unexpected external metadata after ApplyScrapedMetadata: %+v", applied)
	}

	// 2. SetSeriesExtCoverPath & batch SeriesMetadatas readback.
	if err := store.SetSeriesExtCoverPath(ctx, s1.ID, "src_gallery/987654.jpg"); err != nil {
		t.Fatalf("SetSeriesExtCoverPath: %v", err)
	}

	// 3. SaveSeriesMetadata directly on s2.
	if err := store.SaveSeriesMetadata(ctx, SeriesMetadata{
		SeriesID:     s2.ID,
		ExtSourceID:  "src_other",
		ExtEntryID:   "entry_2",
		ExtCoverURL:  "https://cdn.example.com/2.png",
		ExtCoverPath: "src_other/entry_2.png",
		MatchOrigin:  "manual",
		ScrapeStatus: ScrapeStatusMatched,
		Title:        "手动外源作品",
		TitleSort:    "手动外源作品",
	}); err != nil {
		t.Fatalf("SaveSeriesMetadata s2: %v", err)
	}

	batch, err := store.SeriesMetadatas(ctx, []Series{s1, s2})
	if err != nil || len(batch) != 2 {
		t.Fatalf("SeriesMetadatas: len=%d err=%v", len(batch), err)
	}
	if batch[s1.ID].ExtCoverPath != "src_gallery/987654.jpg" || batch[s1.ID].ExtSourceID != "src_gallery" {
		t.Fatalf("batch[s1] mismatch: %+v", batch[s1.ID])
	}
	if batch[s2.ID].ExtSourceID != "src_other" || batch[s2.ID].ExtEntryID != "entry_2" ||
		batch[s2.ID].ExtCoverPath != "src_other/entry_2.png" || batch[s2.ID].MatchOrigin != "manual" {
		t.Fatalf("batch[s2] mismatch: %+v", batch[s2.ID])
	}

	// Verify cover locations stay internal while binding identifiers remain public.
	rawJSON, err := json.Marshal(batch[s1.ID])
	if err != nil {
		t.Fatalf("json.Marshal SeriesMetadata: %v", err)
	}
	jsonText := string(rawJSON)
	for _, want := range []string{`"extSourceId":"src_gallery"`, `"extEntryId":"987654"`, `"matchOrigin":"auto"`} {
		if !strings.Contains(jsonText, want) {
			t.Fatalf("expected JSON to contain %s, got %s", want, jsonText)
		}
	}
	for _, private := range []string{"extCoverUrl", "extCoverPath", batch[s1.ID].ExtCoverURL, batch[s1.ID].ExtCoverPath} {
		if strings.Contains(jsonText, private) {
			t.Fatalf("external cover location leaked into JSON: %s", jsonText)
		}
	}

	// PatchSeriesMetadata updating ExtCoverURL clears stale ExtCoverPath.
	newCover := "https://cdn.example.com/thumb/987654-v2.jpg"
	newOrigin := "agent"
	patched, err := store.PatchSeriesMetadata(ctx, s1, SeriesMetadataPatch{
		ExtCoverURL: &newCover,
		MatchOrigin: &newOrigin,
	})
	if err != nil {
		t.Fatalf("PatchSeriesMetadata: %v", err)
	}
	if patched.ExtCoverURL != newCover || patched.ExtCoverPath != "" || patched.MatchOrigin != "agent" {
		t.Fatalf("expected ExtCoverPath cleared when ExtCoverURL changed: %+v", patched)
	}

	// ClearSeriesExternalBinding clears external fields cleanly.
	cleared, err := store.ClearSeriesExternalBinding(ctx, s1)
	if err != nil {
		t.Fatalf("ClearSeriesExternalBinding: %v", err)
	}
	if cleared.ExtSourceID != "" || cleared.ExtEntryID != "" || cleared.ExtCoverURL != "" || cleared.ExtCoverPath != "" || cleared.MatchOrigin != "" || cleared.ScrapeStatus != ScrapeStatusNone {
		t.Fatalf("expected external binding cleared, got %+v", cleared)
	}
	bgmID := int64(42)
	for _, existing := range []SeriesMetadata{
		{SeriesID: s1.ID, BgmSubjectID: &bgmID},
		{SeriesID: s1.ID, ExtSourceID: "legacy", ExtEntryID: "entry"},
		{SeriesID: s1.ID, BgmSubjectID: &bgmID, MatchOrigin: "manual"},
		{SeriesID: s1.ID, ExtSourceID: "agent-source", ExtEntryID: "entry", MatchOrigin: "agent"},
	} {
		if err := store.SaveSeriesMetadata(ctx, existing); err != nil {
			t.Fatal(err)
		}
		_, err := store.ApplyScrapedMetadata(WithAutomaticMetadata(ctx), s1, ScrapedMetadata{SubjectID: 100, MatchOrigin: "auto"})
		if !errors.Is(err, ErrMetadataChanged) {
			t.Errorf("automatic write to binding %+v: got %v, want ErrMetadataChanged", existing, err)
		}
	}
}

func TestUnionInheritsExternalMetadataFieldsByLayerPriority(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "union_ext.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := t.Context()

	for _, lib := range []Library{
		{ID: "lib_top", Name: "高优先级层", ProviderType: ProviderLocal, RootCID: "local:/top", RootPath: "/top", Enabled: true},
		{ID: "lib_bot", Name: "低优先级层", ProviderType: Provider115, RootCID: "cid_bot", Enabled: true},
		{ID: "lib_sep", Name: "独立对比库", ProviderType: ProviderLocal, RootCID: "local:/sep", RootPath: "/sep", Enabled: true},
	} {
		if err := store.UpsertLibrary(ctx, lib); err != nil {
			t.Fatal(err)
		}
	}

	now := nowText()
	if _, err := store.DB().ExecContext(ctx, `
INSERT INTO series(id,library_id,cid,name,relative_path,one_shot,created_at,updated_at,seen_scan_id) VALUES
('s_top_both','lib_top','Cat/Both','Both','Top/Cat/Both',0,?,?,'s1'),
('s_bot_both','lib_bot','101','Both','Bot/Cat/Both',0,?,?,'s2'),
('s_top_fallback','lib_top','Cat/Fallback','Fallback','Top/Cat/Fallback',0,?,?,'s1'),
('s_bot_fallback','lib_bot','102','Fallback','Bot/Cat/Fallback',0,?,?,'s2'),
('s_sep_same_ext','lib_sep','Cat/Sep','Sep','Sep/Cat/Sep',0,?,?,'s3')`,
		now, now, now, now, now, now, now, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `
INSERT INTO books(id,series_id,library_id,file_id,parent_cid,name,size,pick_code,number_sort,created_at,updated_at,seen_scan_id) VALUES
('b_tb','s_top_both','lib_top','Cat/Both/01.cbz','Cat/Both','01.cbz',1000,'',1,?,?,'s1'),
('b_bb','s_bot_both','lib_bot','f1','101','01.cbz',1000,'pk1',1,?,?,'s2'),
('b_tf','s_top_fallback','lib_top','Cat/Fallback/01.cbz','Cat/Fallback','01.cbz',1000,'',1,?,?,'s1'),
('b_bf','s_bot_fallback','lib_bot','f2','102','01.cbz',1000,'pk2',1,?,?,'s2'),
('b_sep','s_sep_same_ext','lib_sep','Cat/Sep/01.cbz','Cat/Sep','01.cbz',1000,'',1,?,?,'s3')`,
		now, now, now, now, now, now, now, now, now, now); err != nil {
		t.Fatal(err)
	}

	// Both layers have external metadata on "Both" -> higher layer (lib_top) must win!
	if err := store.SaveSeriesMetadata(ctx, SeriesMetadata{
		SeriesID:     "s_top_both",
		ExtSourceID:  "src_top",
		ExtEntryID:   "top_100",
		ExtCoverURL:  "https://top.example/100.jpg",
		ExtCoverPath: "src_top/top_100.jpg",
		MatchOrigin:  "manual",
		ScrapeStatus: ScrapeStatusMatched,
		Title:        "上层标题",
		TitleSort:    "上层标题",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSeriesMetadata(ctx, SeriesMetadata{
		SeriesID:     "s_bot_both",
		ExtSourceID:  "src_bot",
		ExtEntryID:   "bot_999",
		ExtCoverURL:  "https://bot.example/999.jpg",
		ExtCoverPath: "src_bot/bot_999.jpg",
		MatchOrigin:  "auto",
		ScrapeStatus: ScrapeStatusMatched,
		Title:        "下层标题",
		TitleSort:    "下层标题",
	}); err != nil {
		t.Fatal(err)
	}

	// For "Fallback", only lib_bot has external metadata -> union must inherit from lib_bot.
	if err := store.SaveSeriesMetadata(ctx, SeriesMetadata{
		SeriesID:     "s_bot_fallback",
		ExtSourceID:  "src_bot",
		ExtEntryID:   "bot_200",
		ExtCoverURL:  "https://bot.example/200.jpg",
		ExtCoverPath: "src_bot/bot_200.jpg",
		MatchOrigin:  "agent",
		ScrapeStatus: ScrapeStatusMatched,
		Title:        "下层补全标题",
		TitleSort:    "下层补全标题",
	}); err != nil {
		t.Fatal(err)
	}

	// Standalone series in lib_sep shares the EXACT same ext_source_id/ext_entry_id as union's "Both" (no bgm_subject_id).
	if err := store.SaveSeriesMetadata(ctx, SeriesMetadata{
		SeriesID:     "s_sep_same_ext",
		ExtSourceID:  "src_top",
		ExtEntryID:   "top_100",
		ExtCoverURL:  "https://top.example/100.jpg",
		MatchOrigin:  "auto",
		ScrapeStatus: ScrapeStatusMatched,
		Title:        "独立对比作品",
		TitleSort:    "独立对比作品",
	}); err != nil {
		t.Fatal(err)
	}

	unionLib, stats, err := store.CreateUnionLibrary(ctx, "外源联合库", true, []string{"lib_top", "lib_bot"})
	if err != nil {
		t.Fatalf("CreateUnionLibrary: %v", err)
	}
	if stats.SeriesCount != 2 || stats.InheritedMetadata != 2 {
		t.Fatalf("unexpected union stats: %+v", stats)
	}

	uSeriesList, total, err := store.SeriesPage(ctx, SeriesQuery{LibraryIDs: []string{unionLib.ID}, Size: 20})
	if err != nil || total != 2 || len(uSeriesList) != 2 {
		t.Fatalf("union SeriesPage: total=%d len=%d err=%v", total, len(uSeriesList), err)
	}
	uMetaMap, err := store.SeriesMetadatas(ctx, uSeriesList)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]SeriesMetadata{}
	for _, s := range uSeriesList {
		byName[s.Name] = uMetaMap[s.ID]
	}

	bothMeta := byName["Both"]
	if bothMeta.ExtSourceID != "src_top" || bothMeta.ExtEntryID != "top_100" ||
		bothMeta.ExtCoverURL != "https://top.example/100.jpg" || bothMeta.ExtCoverPath != "src_top/top_100.jpg" ||
		bothMeta.MatchOrigin != "manual" || bothMeta.Title != "上层标题" {
		t.Fatalf("expected union 'Both' to inherit top layer external fields, got %+v", bothMeta)
	}

	fallbackMeta := byName["Fallback"]
	if fallbackMeta.ExtSourceID != "src_bot" || fallbackMeta.ExtEntryID != "bot_200" ||
		fallbackMeta.ExtCoverURL != "https://bot.example/200.jpg" || fallbackMeta.ExtCoverPath != "src_bot/bot_200.jpg" ||
		fallbackMeta.MatchOrigin != "agent" || fallbackMeta.Title != "下层补全标题" {
		t.Fatalf("expected union 'Fallback' to inherit lower layer external fields, got %+v", fallbackMeta)
	}

	// Verify GroupBangumi still groups ONLY by positive integer bgm_subject_id (NOT ext_source_id/ext_entry_id).
	grouped, groupedTotal, err := store.SeriesPage(ctx, SeriesQuery{GroupBangumi: true, Size: 20})
	if err != nil || groupedTotal != 3 || len(grouped) != 3 {
		t.Fatalf("expected GroupBangumi NOT to merge series sharing ext_source_id/ext_entry_id without bgm_subject_id: total=%d len=%d err=%v", groupedTotal, len(grouped), err)
	}
	exported, err := store.ListSeriesForMappingExport(ctx, MappingExportFilter{})
	if err != nil || len(exported) != 3 {
		t.Fatalf("default export included union members: %+v, err = %v", exported, err)
	}
	for _, row := range exported {
		if row.LibraryID != unionLib.ID && row.LibraryID != "lib_sep" {
			t.Fatalf("default export included member library: %+v", row)
		}
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE series_metadata SET is_nsfw=1 WHERE series_id='s_bot_both'`); err != nil {
		t.Fatal(err)
	}
	unionSeries := Series{ID: bothMeta.SeriesID, Name: "Both"}
	extCleared, err := store.ClearSeriesExternalBinding(ctx, unionSeries)
	if err != nil || extCleared.ExtEntryID != "" || !extCleared.IsNSFW {
		t.Fatalf("union series after external unbind = %+v, err = %v", extCleared, err)
	}
	if _, err := store.ApplyScrapedMetadata(ctx, unionSeries, ScrapedMetadata{SubjectID: 42, MatchOrigin: "manual"}); err != nil {
		t.Fatal(err)
	}
	bgmCleared, err := store.ClearSeriesBangumiBinding(ctx, unionSeries)
	if err != nil || bgmCleared.BgmSubjectID != nil || !bgmCleared.IsNSFW {
		t.Fatalf("union series after Bangumi unbind = %+v, err = %v", bgmCleared, err)
	}
}

func TestListSeriesForMappingExportFilteringAndNSFWDefault(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "export.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := t.Context()

	for _, lib := range []Library{
		{ID: "lib_a", Name: "库A", ProviderType: ProviderLocal, RootCID: "local:/a", RootPath: "/a", Enabled: true},
		{ID: "lib_b", Name: "库B", ProviderType: ProviderLocal, RootCID: "local:/b", RootPath: "/b", Enabled: true},
	} {
		if err := store.UpsertLibrary(ctx, lib); err != nil {
			t.Fatal(err)
		}
	}

	now := nowText()
	if _, err := store.DB().ExecContext(ctx, `
INSERT INTO series(id,library_id,cid,name,relative_path,one_shot,created_at,updated_at,seen_scan_id) VALUES
('s_bgm_auto','lib_a','c1','[DirA] AutoWork','A/AutoWork',0,?,?,'sc1'),
('s_ext_manual','lib_a','c2','[DirB] ManualExt','A/ManualExt',0,?,?,'sc1'),
('s_ext_agent','lib_b','c3','[DirC] AgentExt','B/AgentExt',0,?,?,'sc2'),
('s_nsfw_manual','lib_b','c4','[DirD] AdultWork','B/AdultWork',0,?,?,'sc2'),
('s_unmatched','lib_a','c5','[DirE] Unmatched','A/Unmatched',0,?,?,'sc1')`,
		now, now, now, now, now, now, now, now, now, now); err != nil {
		t.Fatal(err)
	}

	var bgm100 int64 = 100
	if err := store.SaveSeriesMetadata(ctx, SeriesMetadata{
		SeriesID:     "s_bgm_auto",
		BgmSubjectID: &bgm100,
		MatchOrigin:  "auto",
		ScrapeStatus: ScrapeStatusMatched,
		Title:        "自动Bangumi作品",
		Authors:      []Author{{Name: "作者甲", Role: "writer"}},
		IsNSFW:       false,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSeriesMetadata(ctx, SeriesMetadata{
		SeriesID:     "s_ext_manual",
		ExtSourceID:  "src_1",
		ExtEntryID:   "ext_11",
		MatchOrigin:  "manual",
		ScrapeStatus: ScrapeStatusMatched,
		Title:        "手动外源作品",
		Authors:      []Author{{Name: "画师乙", Role: "artist"}},
		IsNSFW:       false,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSeriesMetadata(ctx, SeriesMetadata{
		SeriesID:     "s_ext_agent",
		ExtSourceID:  "src_2",
		ExtEntryID:   "ext_22",
		MatchOrigin:  "agent",
		ScrapeStatus: ScrapeStatusMatched,
		Title:        "Agent外源作品",
		IsNSFW:       false,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSeriesMetadata(ctx, SeriesMetadata{
		SeriesID:     "s_nsfw_manual",
		ExtSourceID:  "src_2",
		ExtEntryID:   "ext_99",
		MatchOrigin:  "manual",
		ScrapeStatus: ScrapeStatusMatched,
		Title:        "成人作品",
		IsNSFW:       true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSeriesMetadata(ctx, SeriesMetadata{
		SeriesID:     "s_unmatched",
		ScrapeStatus: ScrapeStatusUnmatched,
		Title:        "未命中作品",
	}); err != nil {
		t.Fatal(err)
	}

	// 1. Default filter excludes NSFW and unmatched.
	rows, err := store.ListSeriesForMappingExport(ctx, MappingExportFilter{})
	if err != nil {
		t.Fatalf("ListSeriesForMappingExport default: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 non-NSFW matched rows, got %d: %+v", len(rows), rows)
	}
	for _, r := range rows {
		if r.IsNSFW || r.SeriesID == "s_nsfw_manual" || r.SeriesID == "s_unmatched" {
			t.Fatalf("unexpected row in default export: %+v", r)
		}
		if r.SeriesID == "s_bgm_auto" {
			if r.FolderName != "[DirA] AutoWork" || r.Title != "自动Bangumi作品" ||
				len(r.Authors) != 1 || r.Authors[0].Name != "作者甲" ||
				r.BgmSubjectID == nil || *r.BgmSubjectID != 100 || r.MatchOrigin != "auto" {
				t.Fatalf("unexpected s_bgm_auto row: %+v", r)
			}
		}
	}

	// 2. IncludeNSFW=true includes NSFW matched row.
	allMatched, err := store.ListSeriesForMappingExport(ctx, MappingExportFilter{IncludeNSFW: true})
	if err != nil || len(allMatched) != 4 {
		t.Fatalf("expected 4 matched rows with IncludeNSFW=true, got %d err=%v", len(allMatched), err)
	}

	// 3. Origins filter (case-insensitive) + default NSFW exclusion.
	byOrigin, err := store.ListSeriesForMappingExport(ctx, MappingExportFilter{
		Origins: []string{"manual", "AGENT"},
	})
	if err != nil || len(byOrigin) != 2 {
		t.Fatalf("expected 2 rows for origins [manual, agent] excluding NSFW, got %d err=%v (%+v)", len(byOrigin), err, byOrigin)
	}

	// 4. LibraryIDs filter + Limit.
	byLib, err := store.ListSeriesForMappingExport(ctx, MappingExportFilter{
		LibraryIDs:  []string{"lib_b"},
		IncludeNSFW: true,
		Limit:       1,
	})
	if err != nil || len(byLib) != 1 || byLib[0].LibraryID != "lib_b" {
		t.Fatalf("expected 1 row in lib_b with Limit=1, got %+v err=%v", byLib, err)
	}

	// 5. Non-admin UserScope cannot bypass NSFW or unauthorized libraries (fail-closed).
	restrictedCtx := WithUserScope(ctx, UserScope{
		UserID:            "usr_1",
		IsAdmin:           false,
		AllLibraries:      false,
		AllowedLibraryIDs: []string{"lib_b"},
		AllowNSFW:         false,
		ShowNSFW:          false,
	})
	scopedRows, err := store.ListSeriesForMappingExport(restrictedCtx, MappingExportFilter{IncludeNSFW: true})
	if err != nil || len(scopedRows) != 1 || scopedRows[0].SeriesID != "s_ext_agent" {
		t.Fatalf("expected UserScope to restrict to lib_b non-NSFW, got %+v err=%v", scopedRows, err)
	}

	emptyLibCtx := WithUserScope(ctx, UserScope{
		UserID:            "usr_empty",
		IsAdmin:           false,
		AllLibraries:      false,
		AllowedLibraryIDs: nil,
	})
	emptyRows, err := store.ListSeriesForMappingExport(emptyLibCtx, MappingExportFilter{})
	if err != nil || len(emptyRows) != 0 {
		t.Fatalf("expected fail-closed empty slice for user with no allowed libraries, got %+v err=%v", emptyRows, err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE libraries SET enabled=0 WHERE id='lib_b'`); err != nil {
		t.Fatal(err)
	}
	rows, err = store.ListSeriesForMappingExport(ctx, MappingExportFilter{})
	if err != nil || len(rows) != 2 {
		t.Fatalf("default export with hidden library: %+v, err = %v", rows, err)
	}
	for _, row := range rows {
		if row.LibraryID != "lib_a" {
			t.Fatalf("default export included hidden library: %+v", row)
		}
	}
	rows, err = store.ListSeriesForMappingExport(ctx, MappingExportFilter{LibraryIDs: []string{"lib_b"}})
	if err != nil || len(rows) != 1 || rows[0].LibraryID != "lib_b" {
		t.Fatalf("explicit export of hidden library: %+v, err = %v", rows, err)
	}
}
