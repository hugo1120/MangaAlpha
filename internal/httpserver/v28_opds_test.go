package httpserver

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/xJogger/fake-komga-115/internal/archive"
	"github.com/xJogger/fake-komga-115/internal/cache"
	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/maintenance"
	"github.com/xJogger/fake-komga-115/internal/oneonefive"
	"github.com/xJogger/fake-komga-115/internal/scanner"
	"github.com/xJogger/fake-komga-115/internal/thumbnail"
	"github.com/xJogger/fake-komga-115/internal/webdav"
)

func makeTestPNGBytes(t *testing.T, r, g, b uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 16, 24))
	c := color.RGBA{R: r, G: g, B: b, A: 255}
	for y := 0; y < 24; y++ {
		for x := 0; x < 16; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func writeTestCBZ(t *testing.T, dest string, pageCount int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dest, err)
	}
	f, err := os.Create(dest)
	if err != nil {
		t.Fatalf("create %s: %v", dest, err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for i := 1; i <= pageCount; i++ {
		w, err := zw.Create(fmt.Sprintf("%03d.png", i))
		if err != nil {
			t.Fatalf("zip create: %v", err)
		}
		if _, err := w.Write(makeTestPNGBytes(t, uint8(i*40), 120, 180)); err != nil {
			t.Fatalf("zip write: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
}

func assertValidXML(t *testing.T, body string) {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(body))
	for {
		_, err := dec.Token()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatalf("invalid XML: %v\nbody:\n%s", err, body)
		}
	}
}

func TestV28OPDSDualEntrypointsForKOReaderAndKomga(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := database.Open(filepath.Join(dir, "v28.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	cacheMgr, _ := cache.New(store, filepath.Join(dir, "cache"))
	client := oneonefive.New(store, logger)
	wdMgr := webdav.NewManager()
	scanMgr := scanner.NewWithWebDAV(store, client, wdMgr, logger)
	defer scanMgr.Close()
	archSvc := archive.NewServiceWithWebDAV(store, client, wdMgr, cacheMgr, logger)
	defer archSvc.Close()
	thumbSvc, _ := thumbnail.New(store, filepath.Join(dir, "thumbs"), logger)
	coverMgr := thumbnail.NewBatchManager(store, archSvc, thumbSvc, logger)
	defer coverMgr.Close()
	maintMgr := maintenance.New(store, archSvc, thumbSvc, logger)
	defer maintMgr.Close()

	srv := New(store, client, scanMgr, cacheMgr, archSvc, thumbSvc, coverMgr, maintMgr, RuntimeInfo{DataDir: dir}, logger).
		WithServices(wdMgr, nil)

	// 1. Setup admin credentials
	ctx := context.Background()
	if _, err := store.ClaimAdmin(ctx, "reader", "secret123"); err != nil {
		t.Fatalf("ClaimAdmin: %v", err)
	}
	rawKey, _, err := store.CreateAPIKey(ctx, "KOReader Test")
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}

	// 2. Create real local CBZ archives across visible + hidden + NSFW series
	libRoot := filepath.Join(dir, "manga")
	writeTestCBZ(t, filepath.Join(libRoot, "AlphaQuest", "Vol.01.cbz"), 3)
	writeTestCBZ(t, filepath.Join(libRoot, "AlphaQuest", "Vol.02.cbz"), 2)
	writeTestCBZ(t, filepath.Join(libRoot, "SpicySecret", "Vol.01.cbz"), 2)

	hiddenRoot := filepath.Join(dir, "hidden")
	writeTestCBZ(t, filepath.Join(hiddenRoot, "HiddenSeries", "Vol.01.cbz"), 2)

	doReq := func(method, target, user, pass, apiKey string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, nil)
		if user != "" {
			req.SetBasicAuth(user, pass)
		}
		if apiKey != "" {
			req.Header.Set("X-API-Key", apiKey)
		}
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}
	doBasic := func(target string) *httptest.ResponseRecorder {
		return doReq(http.MethodGet, target, "reader", "secret123", "")
	}

	// Register visible and hidden local libraries and scan them
	visLib := database.Library{
		ID:           "lib_vis",
		Name:         "主书库",
		ProviderType: database.ProviderLocal,
		RootCID:      "local:" + libRoot,
		RootPath:     libRoot,
		Enabled:      true,
	}
	hidLib := database.Library{
		ID:           "lib_hid",
		Name:         "隐藏库",
		ProviderType: database.ProviderLocal,
		RootCID:      "local:" + hiddenRoot,
		RootPath:     hiddenRoot,
		Enabled:      true,
	}
	if err := store.UpsertLibrary(ctx, visLib); err != nil {
		t.Fatalf("UpsertLibrary vis: %v", err)
	}
	if err := store.UpsertLibrary(ctx, hidLib); err != nil {
		t.Fatalf("UpsertLibrary hid: %v", err)
	}
	for _, libID := range []string{visLib.ID, hidLib.ID} {
		run, err := scanMgr.StartLibrary(ctx, libID, "manual")
		if err != nil {
			t.Fatalf("StartLibrary %s: %v", libID, err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			runs, _ := scanMgr.Runs(ctx, 10)
			done := false
			for _, r := range runs {
				if r.ID == run.ID && (r.Status == "success" || r.Status == "failed") {
					if r.Status != "success" {
						t.Fatalf("scan %s failed: %s", libID, r.Error)
					}
					done = true
					break
				}
			}
			if done {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	// Hide hidden library after scan
	hidLib.Enabled = false
	if err := store.UpsertLibrary(ctx, hidLib); err != nil {
		t.Fatalf("hide library: %v", err)
	}

	// Lookup scanned series and decorate metadata (title, genre, publisher, author, NSFW flag)
	allSeries, _, err := store.SeriesPage(ctx, database.SeriesQuery{IncludeHidden: true, NSFWMode: "all", Size: 50})
	if err != nil || len(allSeries) != 3 {
		t.Fatalf("expected 3 scanned series, got %d (err=%v)", len(allSeries), err)
	}
	var alphaSeries, nsfwSeries, hiddenSeries database.Series
	for _, s := range allSeries {
		switch s.Name {
		case "AlphaQuest":
			alphaSeries = s
		case "SpicySecret":
			nsfwSeries = s
		case "HiddenSeries":
			hiddenSeries = s
		}
	}
	if alphaSeries.ID == "" || nsfwSeries.ID == "" || hiddenSeries.ID == "" {
		t.Fatalf("missing scanned series: %+v", allSeries)
	}
	title := "阿尔法物语"
	pub := "讲谈社"
	status := "ONGOING"
	genres := []string{"奇幻"}
	authors := []database.Author{{Name: "测试作者", Role: "writer"}}
	if _, err := store.PatchSeriesMetadata(ctx, alphaSeries, database.SeriesMetadataPatch{
		Title:     &title,
		Publisher: &pub,
		Status:    &status,
		Genres:    &genres,
		Authors:   &authors,
	}); err != nil {
		t.Fatalf("patch alpha metadata: %v", err)
	}
	isNSFW := true
	if _, err := store.PatchSeriesMetadata(ctx, nsfwSeries, database.SeriesMetadataPatch{
		IsNSFW: &isNSFW,
	}); err != nil {
		t.Fatalf("patch nsfw metadata: %v", err)
	}

	col, err := store.CreateCollection(ctx, "必看合集", false, []string{alphaSeries.ID})
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	alphaBooks, _, err := store.BooksPage(ctx, database.BookQuery{SeriesID: alphaSeries.ID, Unpaged: true})
	if err != nil || len(alphaBooks) != 2 {
		t.Fatalf("expected 2 books for alphaSeries, got %d (err=%v)", len(alphaBooks), err)
	}
	rl, err := store.CreateReadList(ctx, "首卷精选", "第一卷们", true, []string{alphaBooks[0].ID})
	if err != nil {
		t.Fatalf("CreateReadList: %v", err)
	}

	// =========================================================================
	// Part A: Authentication & 401 Basic Challenge (NO 302 redirect!)
	// =========================================================================
	for _, p := range []string{"/api/opds/v1.2", "/opds/v1.2/catalog"} {
		unauth := doReq(http.MethodGet, p, "", "", "")
		if unauth.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s: expected 401, got %d (loc=%s)", p, unauth.Code, unauth.Header().Get("Location"))
		}
		if !strings.Contains(unauth.Header().Get("WWW-Authenticate"), "Basic") {
			t.Fatalf("unauthenticated %s: missing WWW-Authenticate Basic header", p)
		}
	}
	// API Key auth also works
	if rec := doReq(http.MethodGet, "/api/opds/v1.2", "", "", rawKey); rec.Code != http.StatusOK {
		t.Fatalf("API key auth /api/opds/v1.2 expected 200, got %d", rec.Code)
	}
	// Auth status exposes version only when authenticated (no unauthenticated version fingerprint)
	if unauthSt := doReq(http.MethodGet, "/admin/api/auth/status", "", "", ""); unauthSt.Code != http.StatusOK || strings.Contains(unauthSt.Body.String(), `"version"`) {
		t.Fatalf("unauthenticated /admin/api/auth/status must not expose version: code=%d body=%s", unauthSt.Code, unauthSt.Body.String())
	}
	if authSt := doBasic("/admin/api/auth/status"); authSt.Code != http.StatusOK || !strings.Contains(authSt.Body.String(), `"version"`) {
		t.Fatalf("authenticated /admin/api/auth/status expected version field: code=%d body=%s", authSt.Code, authSt.Body.String())
	}

	// =========================================================================
	// Part B: Suwayomi OPDS v1.2 (/api/opds/v1.2) for opdsforcomic.koplugin
	// =========================================================================
	for _, rootPath := range []string{"/api/opds/v1.2", "/api/opds/v1.2/"} {
		rec := doBasic(rootPath)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s expected 200, got %d", rootPath, rec.Code)
		}
		body := rec.Body.String()
		assertValidXML(t, body)
		if !strings.Contains(body, `type="application/opensearchdescription+xml" href="/api/opds/v1.2/search"`) {
			t.Fatalf("root missing opensearch link: %s", body)
		}
	}
	if rec := doBasic("/api/opds/v1.2/search"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `template="/api/opds/v1.2/library/series?query={searchTerms}"`) {
		t.Fatalf("opensearch failed: code=%d body=%s", rec.Code, rec.Body.String())
	} else {
		assertValidXML(t, rec.Body.String())
	}

	// Series listing excludes hidden and NSFW series
	seriesRec := doBasic("/api/opds/v1.2/library/series")
	if seriesRec.Code != http.StatusOK {
		t.Fatalf("library/series code=%d", seriesRec.Code)
	}
	seriesXML := seriesRec.Body.String()
	assertValidXML(t, seriesXML)
	if !strings.Contains(seriesXML, "阿尔法物语") {
		t.Fatalf("expected AlphaQuest translated title in feed: %s", seriesXML)
	}
	if strings.Contains(seriesXML, "SpicySecret") || strings.Contains(seriesXML, "HiddenSeries") {
		t.Fatalf("NSFW or hidden series leaked in OPDS feed: %s", seriesXML)
	}
	if strings.Contains(seriesXML, "http://opds-spec.org/acquisition") {
		t.Fatalf("OPDS feed must not expose whole-book download acquisition link")
	}

	// Check sources, categories, genres, statuses, updates, search
	for _, p := range []string{
		"/api/opds/v1.2/library/sources",
		"/api/opds/v1.2/library/categories",
		"/api/opds/v1.2/library/genres",
		"/api/opds/v1.2/library/statuses",
		"/api/opds/v1.2/library/series?query=%E9%98%BF%E5%B0%94%E6%B3%95",
		"/api/opds/v1.2/genre/%E5%A5%87%E5%B9%BB",
		"/api/opds/v1.2/status/ONGOING",
		"/api/opds/v1.2/library-updates",
	} {
		rec := doBasic(p)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s expected 200, got %d: %s", p, rec.Code, rec.Body.String())
		}
		assertValidXML(t, rec.Body.String())
	}

	// Extract numeric series ID from /api/opds/v1.2/series/{num}/chapters link
	reChapters := regexp.MustCompile(`/api/opds/v1\.2/series/(\d+)/chapters`)
	m := reChapters.FindStringSubmatch(seriesXML)
	if len(m) != 2 {
		t.Fatalf("could not find numeric series chapters link in %s", seriesXML)
	}
	seriesNum := m[1]

	// Verify NSFW & hidden series return 404 even by direct numeric rowid
	rowMap, _ := store.SeriesRowIDs(ctx, []string{nsfwSeries.ID, hiddenSeries.ID})
	for _, blockedID := range []int64{rowMap[nsfwSeries.ID], rowMap[hiddenSeries.ID]} {
		if rec := doBasic(fmt.Sprintf("/api/opds/v1.2/series/%d/chapters", blockedID)); rec.Code != http.StatusNotFound {
			t.Fatalf("blocked series rowid %d expected 404, got %d", blockedID, rec.Code)
		}
	}

	// Fetch chapters list: MUST NOT eagerly index archives!
	chapListRec := doBasic(fmt.Sprintf("/api/opds/v1.2/series/%s/chapters", seriesNum))
	if chapListRec.Code != http.StatusOK {
		t.Fatalf("chapters feed code=%d", chapListRec.Code)
	}
	chapListXML := chapListRec.Body.String()
	assertValidXML(t, chapListXML)
	if !strings.Contains(chapListXML, fmt.Sprintf(`rel="self" type="application/atom+xml;profile=opds-catalog;kind=acquisition" href="/api/opds/v1.2/series/%s/chapters"`, seriesNum)) {
		t.Fatalf("chapters feed missing self link required by opdsforcomic chapter nav: %s", chapListXML)
	}
	if strings.Contains(chapListXML, "opds-pse/stream") {
		t.Fatalf("Suwayomi chapters list must NOT contain direct stream link so opdsforcomic fast path activates")
	}

	var indexedCount int
	_ = store.DB().QueryRowContext(ctx, `SELECT count(*) FROM zip_indexes`).Scan(&indexedCount)
	if indexedCount != 0 {
		t.Fatalf("expected 0 zip_indexes after listing chapters (zero eager indexing), got %d", indexedCount)
	}

	// Extract chapter metadata URLs (matching opdsforcomic's exact /api/opds/v1.2/series/%d+/chapter/%d+/metadata regex)
	reMeta := regexp.MustCompile(`/api/opds/v1\.2/series/(\d+)/chapter/(\d+)/metadata`)
	metaMatches := reMeta.FindAllStringSubmatch(chapListXML, -1)
	if len(metaMatches) != 2 {
		t.Fatalf("expected 2 chapter metadata links, got %d in:\n%s", len(metaMatches), chapListXML)
	}
	ch1Num := metaMatches[0][2]

	// Tap chapter 1 metadata: lazy-indexes ONLY book 1!
	ch1MetaPath := fmt.Sprintf("/api/opds/v1.2/series/%s/chapter/%s/metadata", seriesNum, ch1Num)
	metaRec := doBasic(ch1MetaPath)
	if metaRec.Code != http.StatusOK {
		t.Fatalf("chapter 1 metadata code=%d body=%s", metaRec.Code, metaRec.Body.String())
	}
	metaXML := metaRec.Body.String()
	assertValidXML(t, metaXML)
	if !strings.Contains(metaXML, `rel="http://vaemendis.net/opds-pse/stream"`) ||
		!strings.Contains(metaXML, `pse:count="3"`) ||
		!strings.Contains(metaXML, fmt.Sprintf(`/api/opds/v1.2/series/%s/chapter/%s/page/{pageNumber}?updateProgress=true`, seriesNum, ch1Num)) {
		t.Fatalf("chapter 1 metadata missing expected OPDS-PSE stream link:\n%s", metaXML)
	}
	if strings.Contains(metaXML, "pse:lastRead=") {
		t.Fatalf("unread chapter must omit pse:lastRead:\n%s", metaXML)
	}
	if strings.Contains(metaXML, "http://opds-spec.org/acquisition") {
		t.Fatalf("chapter metadata must NOT expose whole-book acquisition download link")
	}
	_ = store.DB().QueryRowContext(ctx, `SELECT count(*) FROM zip_indexes`).Scan(&indexedCount)
	if indexedCount != 1 {
		t.Fatalf("expected exactly 1 zip_index after opening chapter 1, got %d", indexedCount)
	}

	// Prefetch last page (index 2 = page 3) with updateProgress=false: MUST NOT write read or page progress!
	prefetchRec := doBasic(fmt.Sprintf("/api/opds/v1.2/series/%s/chapter/%s/page/2?updateProgress=false", seriesNum, ch1Num))
	if prefetchRec.Code != http.StatusOK || prefetchRec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("prefetch page failed: code=%d ct=%s", prefetchRec.Code, prefetchRec.Header().Get("Content-Type"))
	}
	if prefetchRec.Header().Get("Content-Length") == "" {
		t.Fatalf("page image missing Content-Length header")
	}
	if _, hasProg, _ := store.BookReadProgress(ctx, alphaBooks[0].ID); hasProg {
		t.Fatalf("prefetch with updateProgress=false must NOT write book_read_progress")
	}
	if _, hasPageProg, _ := store.BookPageProgress(ctx, alphaBooks[0].ID); hasPageProg {
		t.Fatalf("prefetch with updateProgress=false must NOT write book_page_progress")
	}

	// Test ?convert=jpeg on PNG page
	jpegRec := doBasic(fmt.Sprintf("/api/opds/v1.2/series/%s/chapter/%s/page/0?updateProgress=false&convert=jpeg", seriesNum, ch1Num))
	if jpegRec.Code != http.StatusOK || jpegRec.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("convert=jpeg failed: code=%d ct=%s", jpegRec.Code, jpegRec.Header().Get("Content-Type"))
	}
	if _, err := jpeg.Decode(bytes.NewReader(jpegRec.Body.Bytes())); err != nil {
		t.Fatalf("convert=jpeg returned invalid JPEG: %v", err)
	}

	// Real page turn to 0-based index 1 (= 1-based page 2 of 3) with updateProgress=true
	p2Rec := doBasic(fmt.Sprintf("/api/opds/v1.2/series/%s/chapter/%s/page/1?updateProgress=true", seriesNum, ch1Num))
	if p2Rec.Code != http.StatusOK {
		t.Fatalf("page 1 read failed: code=%d", p2Rec.Code)
	}
	prog, hasProg, _ := store.BookReadProgress(ctx, alphaBooks[0].ID)
	if !hasProg || prog.Completed || prog.Page == nil || *prog.Page != 2 {
		t.Fatalf("expected in-progress page 2, got has=%v prog=%+v", hasProg, prog)
	}

	// Re-fetch Suwayomi metadata: must emit 0-based pse:lastRead="1" (so plugin +1 lands on page 2!)
	metaRec2 := doBasic(ch1MetaPath)
	if !strings.Contains(metaRec2.Body.String(), `pse:lastRead="1"`) {
		t.Fatalf("expected 0-based pse:lastRead=\"1\" on Suwayomi metadata, got:\n%s", metaRec2.Body.String())
	}

	// History feed lists in-progress chapter with [◐] prefix and direct metadata URL
	histRec := doBasic("/api/opds/v1.2/history")
	if histRec.Code != http.StatusOK {
		t.Fatalf("history code=%d", histRec.Code)
	}
	histXML := histRec.Body.String()
	assertValidXML(t, histXML)
	if !strings.Contains(histXML, "[◐] 阿尔法物语: Vol.01.cbz") || !strings.Contains(histXML, ch1MetaPath) {
		t.Fatalf("history feed missing expected chapter entry:\n%s", histXML)
	}

	// Check Komga OPDS /series/{id} while book 1 is in-progress (page 2) & book 2 is unindexed
	kSeriesRec := doBasic("/opds/v1.2/series/" + alphaSeries.ID)
	if kSeriesRec.Code != http.StatusOK {
		t.Fatalf("komga OPDS series code=%d", kSeriesRec.Code)
	}
	kSeriesXML := kSeriesRec.Body.String()
	assertValidXML(t, kSeriesXML)
	// Book 1 is indexed: direct PSE stream link with 1-based pse:lastRead="2"
	if !strings.Contains(kSeriesXML, `pse:count="3"`) || !strings.Contains(kSeriesXML, `pse:lastRead="2"`) {
		t.Fatalf("komga OPDS series expected 1-based pse:lastRead=\"2\" for indexed book 1:\n%s", kSeriesXML)
	}
	// Book 2 is NOT indexed: subsection link to /opds/v1.2/books/{book2ID}, zero eager indexing!
	if !strings.Contains(kSeriesXML, `href="/opds/v1.2/books/`+alphaBooks[1].ID+`"`) {
		t.Fatalf("komga OPDS series expected subsection link for unindexed book 2:\n%s", kSeriesXML)
	}
	_ = store.DB().QueryRowContext(ctx, `SELECT count(*) FROM zip_indexes`).Scan(&indexedCount)
	if indexedCount != 1 {
		t.Fatalf("komga OPDS series list must NOT eagerly index book 2; zip_indexes=%d", indexedCount)
	}

	// Turn to last page of book 1 (0-based index 2 = 1-based page 3/3) with updateProgress=true -> completed!
	if rec := doBasic(fmt.Sprintf("/api/opds/v1.2/series/%s/chapter/%s/page/2?updateProgress=true", seriesNum, ch1Num)); rec.Code != http.StatusOK {
		t.Fatalf("last page read code=%d", rec.Code)
	}
	prog, hasProg, _ = store.BookReadProgress(ctx, alphaBooks[0].ID)
	if !hasProg || !prog.Completed || prog.Page == nil || *prog.Page != 3 {
		t.Fatalf("expected completed book 1 at page 3, got has=%v prog=%+v", hasProg, prog)
	}
	// Re-reading page 0 on an already-completed book must NOT downgrade completed back to false
	if rec := doBasic(fmt.Sprintf("/api/opds/v1.2/series/%s/chapter/%s/page/0?updateProgress=true", seriesNum, ch1Num)); rec.Code != http.StatusOK {
		t.Fatalf("re-read page 0 code=%d", rec.Code)
	}
	prog, _, _ = store.BookReadProgress(ctx, alphaBooks[0].ID)
	if !prog.Completed {
		t.Fatalf("re-reading earlier page must NOT downgrade completed status")
	}
	// Completed book omits pse:lastRead so reopening starts from page 1
	metaRecDone := doBasic(ch1MetaPath)
	if strings.Contains(metaRecDone.Body.String(), "pse:lastRead=") {
		t.Fatalf("completed chapter must omit pse:lastRead:\n%s", metaRecDone.Body.String())
	}

	// =========================================================================
	// Part C: Standard Komga OPDS v1.2 (/opds/v1.2/catalog, On Deck, Keep Reading, lazy book detail)
	// =========================================================================
	for _, p := range []string{
		"/opds/v1.2",
		"/opds/v1.2/catalog",
		"/opds/v1.2/search",
		"/opds/v1.2/series",
		"/opds/v1.2/series?search=%E9%98%BF%E5%B0%94%E6%B3%95",
		"/opds/v1.2/series/latest",
		"/opds/v1.2/books/latest",
		"/opds/v1.2/libraries",
		"/opds/v1.2/libraries/" + visLib.ID,
		"/opds/v1.2/collections",
		"/opds/v1.2/collections/" + col.ID,
		"/opds/v1.2/readlists",
		"/opds/v1.2/readlists/" + rl.ID,
		"/opds/v1.2/publishers",
		"/opds/v1.2/keep-reading",
	} {
		rec := doBasic(p)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s expected 200, got %d: %s", p, rec.Code, rec.Body.String())
		}
		assertValidXML(t, rec.Body.String())
	}

	// On Deck should now surface Book 2 (Book 1 completed, Book 2 unread!)
	onDeckRec := doBasic("/opds/v1.2/on-deck")
	if onDeckRec.Code != http.StatusOK {
		t.Fatalf("on-deck code=%d", onDeckRec.Code)
	}
	assertValidXML(t, onDeckRec.Body.String())
	if !strings.Contains(onDeckRec.Body.String(), "Vol.02.cbz") {
		t.Fatalf("expected Vol.02.cbz in on-deck feed:\n%s", onDeckRec.Body.String())
	}

	// Open unindexed Book 2 via /opds/v1.2/books/{id}: lazy-indexes Book 2 on demand!
	b2DetailRec := doBasic("/opds/v1.2/books/" + alphaBooks[1].ID)
	if b2DetailRec.Code != http.StatusOK {
		t.Fatalf("komga OPDS book 2 detail code=%d body=%s", b2DetailRec.Code, b2DetailRec.Body.String())
	}
	b2XML := b2DetailRec.Body.String()
	assertValidXML(t, b2XML)
	if !strings.Contains(b2XML, `pse:count="2"`) || !strings.Contains(b2XML, `/opds/v1.2/books/`+alphaBooks[1].ID+`/pages/{pageNumber}?zero_based=true&amp;updateProgress=true`) {
		t.Fatalf("book 2 detail missing expected OPDS-PSE link:\n%s", b2XML)
	}
	if strings.Contains(b2XML, "http://opds-spec.org/acquisition") {
		t.Fatalf("komga OPDS book detail must NOT expose whole-book download link")
	}

	// Read page 0 of Book 2 via Komga OPDS -> moves Book 2 into Keep Reading (1-based page 1) & out of On Deck
	if rec := doBasic("/opds/v1.2/books/" + alphaBooks[1].ID + "/pages/0?zero_based=true&updateProgress=true"); rec.Code != http.StatusOK {
		t.Fatalf("komga OPDS book 2 page 0 code=%d", rec.Code)
	}
	krRec := doBasic("/opds/v1.2/keep-reading")
	if !strings.Contains(krRec.Body.String(), "Vol.02.cbz") || !strings.Contains(krRec.Body.String(), `pse:lastRead="1"`) {
		t.Fatalf("expected Vol.02.cbz with 1-based pse:lastRead=\"1\" in keep-reading:\n%s", krRec.Body.String())
	}
	onDeckRec2 := doBasic("/opds/v1.2/on-deck")
	if strings.Contains(onDeckRec2.Body.String(), "Vol.02.cbz") {
		t.Fatalf("in-progress series should leave on-deck:\n%s", onDeckRec2.Body.String())
	}

	// Thumbnails (series & book reuse series cover cleanly)
	for _, thumbPath := range []string{
		fmt.Sprintf("/api/opds/v1.2/series/%s/thumbnail", seriesNum),
		"/opds/v1.2/series/" + alphaSeries.ID + "/thumbnail",
		"/opds/v1.2/books/" + alphaBooks[0].ID + "/thumbnail",
		"/opds/v1.2/books/" + alphaBooks[0].ID + "/thumbnail/small",
	} {
		rec := doBasic(thumbPath)
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "image/") {
			t.Fatalf("thumbnail %s failed: code=%d ct=%s", thumbPath, rec.Code, rec.Header().Get("Content-Type"))
		}
	}

	// Hidden library returns 404 on Komga OPDS
	if rec := doBasic("/opds/v1.2/libraries/" + hidLib.ID); rec.Code != http.StatusNotFound {
		t.Fatalf("hidden library expected 404, got %d", rec.Code)
	}
}
