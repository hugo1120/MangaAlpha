package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestV30LegacyUpgradePreservesAllAdminData(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "legacy-v29.db")

	// Construct an authentic pre-v3.0 (v2.9) database schema & sample data.
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	now := nowText()
	pwHash, err := hashPassword("AdminPass123!")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	legacyDDL := `
PRAGMA foreign_keys = ON;
CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE auth_admin (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	username TEXT NOT NULL,
	password_hash TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE auth_sessions (
	token_hash TEXT PRIMARY KEY,
	username TEXT NOT NULL,
	remember INTEGER NOT NULL DEFAULT 0,
	user_agent TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	expires_at TEXT NOT NULL,
	last_seen_at TEXT NOT NULL
);
CREATE TABLE auth_api_keys (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	key_prefix TEXT NOT NULL,
	key_hash TEXT NOT NULL UNIQUE,
	created_at TEXT NOT NULL,
	last_used_at TEXT
);
CREATE TABLE libraries (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	provider_type TEXT NOT NULL DEFAULT 'local',
	root_cid TEXT NOT NULL DEFAULT '',
	root_path TEXT NOT NULL DEFAULT '',
	webdav_account_id TEXT NOT NULL DEFAULT '',
	enabled INTEGER NOT NULL DEFAULT 1,
	one_shot INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE series (
	id TEXT PRIMARY KEY,
	library_id TEXT NOT NULL,
	cid TEXT NOT NULL,
	name TEXT NOT NULL,
	relative_path TEXT NOT NULL,
	books_count INTEGER NOT NULL DEFAULT 0,
	one_shot INTEGER NOT NULL DEFAULT 0,
	file_modified_at TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	seen_scan_id TEXT NOT NULL DEFAULT '',
	FOREIGN KEY(library_id) REFERENCES libraries(id) ON DELETE CASCADE
);
CREATE TABLE books (
	id TEXT PRIMARY KEY,
	series_id TEXT NOT NULL,
	library_id TEXT NOT NULL,
	file_id TEXT NOT NULL,
	parent_cid TEXT NOT NULL DEFAULT '',
	pick_code TEXT NOT NULL,
	sha1 TEXT NOT NULL,
	name TEXT NOT NULL,
	size INTEGER NOT NULL,
	number_sort REAL NOT NULL,
	file_created_at TEXT,
	file_modified_at TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	seen_scan_id TEXT NOT NULL DEFAULT '',
	FOREIGN KEY(series_id) REFERENCES series(id) ON DELETE CASCADE,
	FOREIGN KEY(library_id) REFERENCES libraries(id) ON DELETE CASCADE
);
CREATE TABLE book_read_progress (
	book_id TEXT PRIMARY KEY,
	series_id TEXT NOT NULL,
	completed INTEGER NOT NULL DEFAULT 0,
	page INTEGER,
	read_date TEXT,
	updated_at TEXT NOT NULL,
	FOREIGN KEY(book_id) REFERENCES books(id) ON DELETE CASCADE,
	FOREIGN KEY(series_id) REFERENCES series(id) ON DELETE CASCADE
);
CREATE TABLE book_page_progress (
	book_id TEXT PRIMARY KEY,
	series_id TEXT NOT NULL,
	last_loaded_page INTEGER NOT NULL,
	max_loaded_page INTEGER NOT NULL,
	page_count INTEGER NOT NULL,
	updated_at TEXT NOT NULL,
	FOREIGN KEY(book_id) REFERENCES books(id) ON DELETE CASCADE,
	FOREIGN KEY(series_id) REFERENCES series(id) ON DELETE CASCADE
);
CREATE TABLE book_read_progress_tombstones (
	book_id TEXT PRIMARY KEY,
	series_id TEXT NOT NULL,
	deleted_at TEXT NOT NULL,
	FOREIGN KEY(book_id) REFERENCES books(id) ON DELETE CASCADE,
	FOREIGN KEY(series_id) REFERENCES series(id) ON DELETE CASCADE
);
CREATE TABLE collections (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL UNIQUE COLLATE NOCASE,
	ordered INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE collection_series (
	collection_id TEXT NOT NULL,
	series_id TEXT NOT NULL,
	position INTEGER NOT NULL DEFAULT 0,
	added_at TEXT NOT NULL,
	PRIMARY KEY(collection_id, series_id),
	FOREIGN KEY(collection_id) REFERENCES collections(id) ON DELETE CASCADE,
	FOREIGN KEY(series_id) REFERENCES series(id) ON DELETE CASCADE
);
CREATE TABLE readlists (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL UNIQUE COLLATE NOCASE,
	summary TEXT NOT NULL DEFAULT '',
	ordered INTEGER NOT NULL DEFAULT 1,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE readlist_books (
	readlist_id TEXT NOT NULL,
	book_id TEXT NOT NULL,
	position INTEGER NOT NULL DEFAULT 0,
	added_at TEXT NOT NULL,
	PRIMARY KEY(readlist_id, book_id),
	FOREIGN KEY(readlist_id) REFERENCES readlists(id) ON DELETE CASCADE,
	FOREIGN KEY(book_id) REFERENCES books(id) ON DELETE CASCADE
);
CREATE TABLE client_settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL,
	allow_unauthenticated INTEGER NOT NULL DEFAULT 0,
	updated_at TEXT NOT NULL
);
CREATE TABLE bangumi_tracker_records (
	bgm_subject_id INTEGER PRIMARY KEY,
	series_id TEXT NOT NULL DEFAULT '',
	series_title TEXT NOT NULL DEFAULT '',
	remote_type INTEGER NOT NULL DEFAULT 0,
	remote_vol_status INTEGER NOT NULL DEFAULT 0,
	remote_private INTEGER NOT NULL DEFAULT 0,
	status TEXT NOT NULL DEFAULT '',
	last_error TEXT NOT NULL DEFAULT '',
	last_synced_at TEXT,
	updated_at TEXT NOT NULL
);
`
	if _, err := rawDB.ExecContext(ctx, legacyDDL); err != nil {
		t.Fatalf("create legacy ddl: %v", err)
	}

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := rawDB.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO auth_admin(id,username,password_hash,created_at,updated_at) VALUES(1,'master',?,?,?)`, pwHash, now, now)
	sessToken := "legacy-session-token-123456"
	mustExec(`INSERT INTO auth_sessions(token_hash,username,user_agent,created_at,expires_at,last_seen_at) VALUES(?,'master','Mozilla',?,?,?)`,
		sha256Hex(sessToken), now, "9999-12-31T23:59:59Z", now)
	rawAPIKey := "fk115_legacyapikeyvalue"
	mustExec(`INSERT INTO auth_api_keys(id,name,key_prefix,key_hash,created_at) VALUES('key1','OldClient','fk115_lega',?,?)`,
		sha256Hex(rawAPIKey), now)
	mustExec(`INSERT INTO settings(key,value,updated_at) VALUES('show_nsfw','true',?)`, now)
	mustExec(`INSERT INTO settings(key,value,updated_at) VALUES('bangumi_tracker_enabled','true',?)`, now)
	mustExec(`INSERT INTO settings(key,value,updated_at) VALUES('bangumi_tracker_auth_mode','pat',?)`, now)
	mustExec(`INSERT INTO settings(key,value,updated_at) VALUES('bangumi_tracker_token','bgm_legacy_pat_token',?)`, now)
	mustExec(`INSERT INTO settings(key,value,updated_at) VALUES('bangumi_tracker_username','alpha_bgm',?)`, now)
	mustExec(`INSERT INTO settings(key,value,updated_at) VALUES('bangumi_tracker_nickname','Alpha',?)`, now)
	mustExec(`INSERT INTO settings(key,value,updated_at) VALUES('bangumi_tracker_nsfw_mode','private',?)`, now)
	mustExec(`INSERT INTO settings(key,value,updated_at) VALUES('bangumi_tracker_private_default','true',?)`, now)

	mustExec(`INSERT INTO libraries(id,name,provider_type,root_cid,root_path,enabled,one_shot,created_at,updated_at) VALUES('lib1','Main','local','local:/tmp','/tmp',1,0,?,?)`, now, now)
	mustExec(`INSERT INTO series(id,library_id,cid,name,relative_path,books_count,one_shot,created_at,updated_at,seen_scan_id) VALUES('s1','lib1','c1','Yokohama','Yokohama',2,0,?,?,'scan1')`, now, now)
	mustExec(`INSERT INTO books(id,series_id,library_id,file_id,parent_cid,pick_code,sha1,name,size,number_sort,created_at,updated_at,seen_scan_id) VALUES('b1','s1','lib1','f1','c1','p1','sha1','Vol01.cbz',1000,1,?,?,'scan1')`, now, now)
	mustExec(`INSERT INTO books(id,series_id,library_id,file_id,parent_cid,pick_code,sha1,name,size,number_sort,created_at,updated_at,seen_scan_id) VALUES('b2','s1','lib1','f2','c1','p2','sha2','Vol02.cbz',1000,2,?,?,'scan1')`, now, now)
	mustExec(`INSERT INTO book_read_progress(book_id,series_id,completed,page,read_date,updated_at) VALUES('b1','s1',1,24,?,?)`, now, now)
	mustExec(`INSERT INTO book_page_progress(book_id,series_id,last_loaded_page,max_loaded_page,page_count,updated_at) VALUES('b2','s1',7,9,28,?)`, now)
	mustExec(`INSERT INTO book_read_progress_tombstones(book_id,series_id,deleted_at) VALUES('b2','s1',?)`, now)
	mustExec(`INSERT INTO collections(id,name,ordered,created_at,updated_at) VALUES('col1','Favorites',1,?,?)`, now, now)
	mustExec(`INSERT INTO collection_series(collection_id,series_id,position,added_at) VALUES('col1','s1',0,?)`, now)
	mustExec(`INSERT INTO readlists(id,name,summary,ordered,created_at,updated_at) VALUES('rl1','Weekend','Good reads',1,?,?)`, now, now)
	mustExec(`INSERT INTO readlist_books(readlist_id,book_id,position,added_at) VALUES('rl1','b1',0,?)`, now)
	mustExec(`INSERT INTO client_settings(key,value,allow_unauthenticated,updated_at) VALUES('webui.reader.globalMeta','{"k":"v"}',0,?)`, now)
	mustExec(`INSERT INTO bangumi_tracker_records(bgm_subject_id,series_id,series_title,remote_type,remote_vol_status,remote_private,status,last_error,last_synced_at,updated_at) VALUES(1234,'s1','Yokohama',3,1,0,'synced','',?,?)`, now, now)
	_ = rawDB.Close()

	// Now open with v3.0 Store — triggers ensureMultiUserMigration!
	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open v3 store on legacy db: %v", err)
	}
	defer store.Close()

	admin, err := store.UserByID(ctx, PrimaryAdminUserID)
	if err != nil {
		t.Fatalf("UserByID(admin): %v", err)
	}
	if admin.Username != "master" || !admin.IsAdmin() || !admin.Enabled || !admin.AllLibraries || !admin.AllowNSFW || !admin.ShowNSFW {
		t.Fatalf("unexpected migrated admin user: %+v", admin)
	}
	if u, ok, err := store.VerifyUserPassword(ctx, "master", "AdminPass123!"); err != nil || !ok || u.ID != PrimaryAdminUserID {
		t.Fatalf("legacy admin password failed after migration: ok=%v err=%v", ok, err)
	}
	if sess, ok, err := store.ValidateSession(ctx, sessToken); err != nil || !ok || sess.UserID != PrimaryAdminUserID {
		t.Fatalf("legacy session invalid after migration: ok=%v err=%v sess=%+v", ok, err, sess)
	}
	if keyRec, ok, err := store.ValidateAPIKey(ctx, rawAPIKey); err != nil || !ok || keyRec.UserID != PrimaryAdminUserID {
		t.Fatalf("legacy API key invalid after migration: ok=%v err=%v key=%+v", ok, err, keyRec)
	}

	adminCtx := WithUserScope(ctx, admin.Scope())
	prog, ok, err := store.BookReadProgress(adminCtx, "b1")
	if err != nil || !ok || !prog.Completed || prog.Page == nil || *prog.Page != 24 {
		t.Fatalf("legacy read progress lost: ok=%v err=%v prog=%+v", ok, err, prog)
	}
	pageProg, ok, err := store.BookPageProgress(adminCtx, "b2")
	if err != nil || !ok || pageProg.MaxLoadedPage != 9 {
		t.Fatalf("legacy page progress lost: ok=%v err=%v prog=%+v", ok, err, pageProg)
	}
	col, err := store.CollectionByID(adminCtx, "col1")
	if err != nil || col.Name != "Favorites" || len(col.SeriesIDs) != 1 || col.SeriesIDs[0] != "s1" {
		t.Fatalf("legacy collection or members lost (FK cascade check!): err=%v col=%+v", err, col)
	}
	rl, err := store.ReadListByID(adminCtx, "rl1")
	if err != nil || rl.Name != "Weekend" || len(rl.BookIDs) != 1 || rl.BookIDs[0] != "b1" {
		t.Fatalf("legacy readlist or members lost (FK cascade check!): err=%v rl=%+v", err, rl)
	}
	cs, err := store.ClientSettings(adminCtx)
	if err != nil || cs["webui.reader.globalMeta"].Value != `{"k":"v"}` {
		t.Fatalf("legacy client settings lost: err=%v cs=%+v", err, cs)
	}
	trCfg, err := store.UserBangumiTrackerConfig(adminCtx, PrimaryAdminUserID)
	if err != nil || !trCfg.Enabled || trCfg.AccessToken != "bgm_legacy_pat_token" || trCfg.BgmUsername != "alpha_bgm" || trCfg.NSFWMode != "private" || !trCfg.PrivateDefault {
		t.Fatalf("legacy bangumi tracker config lost: err=%v cfg=%+v", err, trCfg)
	}
	trRec, found, err := store.BangumiTrackerRecordBySubject(adminCtx, 1234)
	if err != nil || !found || trRec.Status != "synced" || trRec.RemoteVolStatus != 1 {
		t.Fatalf("legacy bangumi tracker record lost: found=%v err=%v rec=%+v", found, err, trRec)
	}
}

func TestV30MultiUserIsolationAndLifecycle(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "multi.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	if _, err := store.ClaimAdmin(ctx, "boss", "BossPass123!"); err != nil {
		t.Fatalf("ClaimAdmin: %v", err)
	}
	admin, err := store.UserByID(ctx, PrimaryAdminUserID)
	if err != nil {
		t.Fatalf("UserByID(admin): %v", err)
	}

	now := time.Now().UTC()
	for _, lib := range []Library{
		{ID: "lib1", Name: "AllAges", ProviderType: ProviderLocal, RootCID: "local:/a", RootPath: "/a", Enabled: true},
		{ID: "lib2", Name: "PrivateLib", ProviderType: ProviderLocal, RootCID: "local:/b", RootPath: "/b", Enabled: true},
	} {
		if err := store.UpsertLibrary(ctx, lib); err != nil {
			t.Fatalf("UpsertLibrary(%s): %v", lib.ID, err)
		}
	}
	// Seed lib1 (normal + nsfw series) and lib2 (normal series)
	nowStr := now.Format(time.RFC3339)
	seedExec := func(q string, args ...any) {
		t.Helper()
		if _, err := store.DB().ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	seedExec(`INSERT INTO series(id,library_id,cid,name,relative_path,books_count,one_shot,created_at,updated_at,seen_scan_id) VALUES('s1','lib1','c1','Alpha','Alpha',2,0,?,?,'scan1')`, nowStr, nowStr)
	seedExec(`INSERT INTO series(id,library_id,cid,name,relative_path,books_count,one_shot,created_at,updated_at,seen_scan_id) VALUES('s1_nsfw','lib1','c1n','AdultOnly','AdultOnly',1,0,?,?,'scan1')`, nowStr, nowStr)
	seedExec(`INSERT INTO series(id,library_id,cid,name,relative_path,books_count,one_shot,created_at,updated_at,seen_scan_id) VALUES('s2','lib2','c2','SecretManga','SecretManga',1,0,?,?,'scan1')`, nowStr, nowStr)
	seedExec(`INSERT INTO books(id,series_id,library_id,file_id,parent_cid,pick_code,sha1,name,size,number_sort,created_at,updated_at,seen_scan_id) VALUES('b1_1','s1','lib1','f1','c1','p1','h1','Vol01.cbz',100,1,?,?,'scan1')`, nowStr, nowStr)
	seedExec(`INSERT INTO books(id,series_id,library_id,file_id,parent_cid,pick_code,sha1,name,size,number_sort,created_at,updated_at,seen_scan_id) VALUES('b1_2','s1','lib1','f2','c1','p2','h2','Vol02.cbz',100,2,?,?,'scan1')`, nowStr, nowStr)
	seedExec(`INSERT INTO books(id,series_id,library_id,file_id,parent_cid,pick_code,sha1,name,size,number_sort,created_at,updated_at,seen_scan_id) VALUES('b1_n','s1_nsfw','lib1','fn','c1n','pn','hn','N01.cbz',100,1,?,?,'scan1')`, nowStr, nowStr)
	seedExec(`INSERT INTO books(id,series_id,library_id,file_id,parent_cid,pick_code,sha1,name,size,number_sort,created_at,updated_at,seen_scan_id) VALUES('b2_1','s2','lib2','f21','c2','p21','h21','Sec01.cbz',100,1,?,?,'scan1')`, nowStr, nowStr)
	isTrue := true
	if _, err := store.PatchSeriesMetadata(ctx, Series{ID: "s1_nsfw", LibraryID: "lib1", Name: "AdultOnly"}, SeriesMetadataPatch{IsNSFW: &isTrue}); err != nil {
		t.Fatalf("mark s1_nsfw: %v", err)
	}

	// Create User A (only lib1, allowNSFW=false) and User B (all libs, allowNSFW=true)
	userA, err := store.CreateUser(ctx, CreateUserInput{
		Username:          "alice",
		Password:          "AlicePass123!",
		AllLibraries:      false,
		AllowedLibraryIDs: []string{"lib1"},
		AllowNSFW:         false,
	})
	if err != nil {
		t.Fatalf("CreateUser(alice): %v", err)
	}
	userB, err := store.CreateUser(ctx, CreateUserInput{
		Username:     "bob",
		Password:     "BobPass123!",
		AllLibraries: true,
		AllowNSFW:    true,
	})
	if err != nil {
		t.Fatalf("CreateUser(bob): %v", err)
	}

	ctxAdmin := WithUserScope(ctx, admin.Scope())
	ctxA := WithUserScope(ctx, userA.Scope())
	ctxB := WithUserScope(ctx, userB.Scope())

	// Library visibility check
	libsA, _ := store.LibrariesBasic(ctxA, true)
	if len(libsA) != 1 || libsA[0].ID != "lib1" {
		t.Fatalf("alice libraries mismatch: %+v", libsA)
	}
	if _, err := store.SeriesByID(ctxA, "s2"); err != sql.ErrNoRows {
		t.Fatalf("alice must not see lib2 series s2, got err=%v", err)
	}
	if _, err := store.BookByID(ctxA, "b2_1"); err != sql.ErrNoRows {
		t.Fatalf("alice must not see lib2 book b2_1, got err=%v", err)
	}
	// NSFW isolation: alice cannot enable; bob defaults to false then enables
	if _, err := store.SetUserShowNSFW(ctx, userA.ID, true); err != ErrNSFWNotAllowed {
		t.Fatalf("expected ErrNSFWNotAllowed for alice, got %v", err)
	}
	if _, err := store.SeriesByID(ctxB, "s1_nsfw"); err != sql.ErrNoRows {
		t.Fatalf("bob should hide nsfw before toggling on")
	}
	if _, err := store.SetUserShowNSFW(ctx, userB.ID, true); err != nil {
		t.Fatalf("bob SetUserShowNSFW(true): %v", err)
	}
	userB, _ = store.UserByID(ctx, userB.ID)
	ctxB = WithUserScope(ctx, userB.Scope())
	if _, err := store.SeriesByID(ctxB, "s1_nsfw"); err != nil {
		t.Fatalf("bob should see s1_nsfw after toggling on: %v", err)
	}
	if _, err := store.SeriesByID(ctxAdmin, "s1_nsfw"); err != sql.ErrNoRows {
		t.Fatalf("admin should still hide nsfw independently of bob")
	}

	// Reading progress + OnDeck + Collections + ReadLists + ClientSettings isolation
	b1, err := store.BookByID(ctxA, "b1_1")
	if err != nil {
		t.Fatalf("BookByID(b1_1): %v", err)
	}
	doneTrue := true
	pg5 := 5
	if err := store.UpdateBookReadProgress(ctxA, b1, &doneTrue, nil); err != nil {
		t.Fatalf("alice complete b1_1: %v", err)
	}
	if err := store.UpdateBookReadProgress(ctxB, b1, nil, &pg5); err != nil {
		t.Fatalf("bob page 5 b1_1: %v", err)
	}

	pA, okA, _ := store.BookReadProgress(ctxA, "b1_1")
	pB, okB, _ := store.BookReadProgress(ctxB, "b1_1")
	_, okAdmin, _ := store.BookReadProgress(ctxAdmin, "b1_1")
	if !okA || !pA.Completed || !okB || pB.Completed || pB.Page == nil || *pB.Page != 5 || okAdmin {
		t.Fatalf("progress leaked across users: A=(%v,%+v) B=(%v,%+v) admin=%v", okA, pA, okB, pB, okAdmin)
	}

	// OnDeck: alice completed b1_1 so b1_2 is on deck; bob is in-progress so on deck is empty.
	odA, _, _ := store.OnDeckBooksPage(ctxA, 0, 10)
	odB, _, _ := store.OnDeckBooksPage(ctxB, 0, 10)
	if len(odA) != 1 || odA[0].ID != "b1_2" || len(odB) != 0 {
		t.Fatalf("OnDeck isolation failed: A=%+v B=%+v", odA, odB)
	}

	// Same collection name allowed for different users!
	colA, err := store.CreateCollection(ctxA, "MyFav", false, []string{"s1"})
	if err != nil {
		t.Fatalf("alice CreateCollection: %v", err)
	}
	colB, err := store.CreateCollection(ctxB, "MyFav", false, []string{"s2"})
	if err != nil {
		t.Fatalf("bob CreateCollection with same name: %v", err)
	}
	if _, err := store.CollectionByID(ctxA, colB.ID); err != sql.ErrNoRows {
		t.Fatalf("alice must not see bob's collection")
	}
	if err := store.DeleteCollection(ctxB, colA.ID); err != sql.ErrNoRows {
		t.Fatalf("bob must not delete alice's collection")
	}

	// Client settings isolation
	_ = store.UpsertClientSetting(ctxA, "webui.theme", "dark", false)
	_ = store.UpsertClientSetting(ctxB, "webui.theme", "light", false)
	csA, _ := store.ClientSettings(ctxA)
	csB, _ := store.ClientSettings(ctxB)
	if csA["webui.theme"].Value != "dark" || csB["webui.theme"].Value != "light" {
		t.Fatalf("client settings leaked: A=%+v B=%+v", csA, csB)
	}

	// Disable alice -> sessions revoked, API key blocked, data preserved
	sessA, _, _ := store.CreateSessionForUser(ctx, userA.ID, userA.Username, true, "UA")
	keyA, _, _ := store.CreateAPIKeyForUser(ctx, userA.ID, "AliceKey")
	f := false
	if _, err := store.UpdateUser(ctx, userA.ID, UpdateUserInput{Enabled: &f}); err != nil {
		t.Fatalf("disable alice: %v", err)
	}
	if _, ok, _ := store.ValidateSession(ctx, sessA); ok {
		t.Fatalf("disabled user session must be invalidated")
	}
	if _, ok, _ := store.ValidateAPIKey(ctx, keyA); ok {
		t.Fatalf("disabled user API key must be rejected")
	}
	if _, ok, _ := store.VerifyUserPassword(ctx, "alice", "AlicePass123!"); ok {
		t.Fatalf("disabled user password login must be rejected")
	}
	// Re-enable alice -> data preserved!
	tr := true
	if _, err := store.UpdateUser(ctx, userA.ID, UpdateUserInput{Enabled: &tr}); err != nil {
		t.Fatalf("re-enable alice: %v", err)
	}
	if pCheck, ok, _ := store.BookReadProgress(ctxA, "b1_1"); !ok || !pCheck.Completed {
		t.Fatalf("alice progress should survive disable/re-enable")
	}

	// Delete alice -> wipes all alice private rows without harming bob or catalog
	if err := store.DeleteUser(ctx, userA.ID); err != nil {
		t.Fatalf("DeleteUser(alice): %v", err)
	}
	if _, err := store.UserByID(ctx, userA.ID); err != sql.ErrNoRows {
		t.Fatalf("alice should be gone")
	}
	if _, ok, _ := store.BookReadProgress(ctxA, "b1_1"); ok {
		t.Fatalf("alice read progress should be wiped on delete")
	}
	if pCheckB, ok, _ := store.BookReadProgress(ctxB, "b1_1"); !ok || pCheckB.Page == nil || *pCheckB.Page != 5 {
		t.Fatalf("bob progress should remain intact after deleting alice")
	}
	if _, err := store.SeriesByID(ctxAdmin, "s1"); err != nil {
		t.Fatalf("shared catalog series s1 must remain intact")
	}
}
