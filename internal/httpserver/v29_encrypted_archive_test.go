package httpserver

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"context"
	"crypto/aes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/pbkdf2"

	"github.com/xJogger/fake-komga-115/internal/archive"
	"github.com/xJogger/fake-komga-115/internal/cache"
	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/maintenance"
	"github.com/xJogger/fake-komga-115/internal/oneonefive"
	"github.com/xJogger/fake-komga-115/internal/scanner"
	"github.com/xJogger/fake-komga-115/internal/thumbnail"
	"github.com/xJogger/fake-komga-115/internal/webdav"
)

func writeEncryptedCBZForHTTPTest(t *testing.T, dest, password string, pageCount int, useAES bool) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dest, err)
	}
	var buf bytes.Buffer
	type rec struct {
		name                            string
		flags, method, modTime, modDate uint16
		crc, compSize, uncompSize, lOff uint32
		extra                           []byte
	}
	var records []rec
	for i := 1; i <= pageCount; i++ {
		name := fmt.Sprintf("%03d.png", i)
		plain := makeTestPNGBytes(t, uint8(i*50), 90, 200)
		origCRC := crc32.ChecksumIEEE(plain)
		lOff := uint32(buf.Len())
		var cb bytes.Buffer
		fw, _ := flate.NewWriter(&cb, flate.DefaultCompression)
		_, _ = fw.Write(plain)
		_ = fw.Close()
		deflated := cb.Bytes()

		var (
			flags      uint16 = 0x0001
			method     uint16
			headerCRC  uint32
			extra      []byte
			ciphertext []byte
		)
		if useAES {
			method = 99
			headerCRC = 0
			extra = make([]byte, 11)
			binary.LittleEndian.PutUint16(extra[0:2], 0x9901)
			binary.LittleEndian.PutUint16(extra[2:4], 7)
			binary.LittleEndian.PutUint16(extra[4:6], 2)
			extra[6] = 'A'
			extra[7] = 'E'
			extra[8] = 3
			binary.LittleEndian.PutUint16(extra[9:11], zip.Deflate)

			salt := make([]byte, 16)
			_, _ = rand.Read(salt)
			derived := pbkdf2.Key([]byte(password), salt, 1000, 66, sha1.New)
			block, _ := aes.NewCipher(derived[:32])
			var ctr, ks [aes.BlockSize]byte
			ctr[0] = 1
			enc := make([]byte, len(deflated))
			for off := 0; off < len(deflated); off += aes.BlockSize {
				block.Encrypt(ks[:], ctr[:])
				for k := 0; k < aes.BlockSize; k++ {
					ctr[k]++
					if ctr[k] != 0 {
						break
					}
				}
				end := min(off+aes.BlockSize, len(deflated))
				for j := off; j < end; j++ {
					enc[j] = deflated[j] ^ ks[j-off]
				}
			}
			mac := hmac.New(sha1.New, derived[32:64])
			_, _ = mac.Write(enc)
			var payload bytes.Buffer
			payload.Write(salt)
			payload.Write(derived[64:66])
			payload.Write(enc)
			payload.Write(mac.Sum(nil)[:10])
			ciphertext = payload.Bytes()
		} else {
			method = zip.Deflate
			headerCRC = origCRC
			k0, k1, k2 := uint32(0x12345678), uint32(0x23456789), uint32(0x34567890)
			upd := func(b byte) {
				k0 = crc32.IEEETable[(k0^uint32(b))&0xff] ^ (k0 >> 8)
				k1 = (k1+(k0&0xff))*134775813 + 1
				k2 = crc32.IEEETable[(k2^uint32(byte(k1>>24)))&0xff] ^ (k2 >> 8)
			}
			ksb := func() byte {
				tmp := (k2 & 0xffff) | 2
				return byte((tmp * (tmp ^ 1)) >> 8)
			}
			for k := 0; k < len(password); k++ {
				upd(password[k])
			}
			hdr := make([]byte, 12)
			_, _ = rand.Read(hdr[:11])
			hdr[11] = byte(origCRC >> 24)
			ciphertext = make([]byte, 0, 12+len(deflated))
			for _, b := range hdr {
				c := b ^ ksb()
				upd(b)
				ciphertext = append(ciphertext, c)
			}
			for _, b := range deflated {
				c := b ^ ksb()
				upd(b)
				ciphertext = append(ciphertext, c)
			}
		}

		lh := make([]byte, 30)
		binary.LittleEndian.PutUint32(lh[0:4], 0x04034b50)
		binary.LittleEndian.PutUint16(lh[4:6], 51)
		binary.LittleEndian.PutUint16(lh[6:8], flags)
		binary.LittleEndian.PutUint16(lh[8:10], method)
		binary.LittleEndian.PutUint16(lh[10:12], 0x4a21)
		binary.LittleEndian.PutUint16(lh[12:14], 0x5777)
		binary.LittleEndian.PutUint32(lh[14:18], headerCRC)
		binary.LittleEndian.PutUint32(lh[18:22], uint32(len(ciphertext)))
		binary.LittleEndian.PutUint32(lh[22:26], uint32(len(plain)))
		binary.LittleEndian.PutUint16(lh[26:28], uint16(len(name)))
		binary.LittleEndian.PutUint16(lh[28:30], uint16(len(extra)))
		buf.Write(lh)
		buf.WriteString(name)
		buf.Write(extra)
		buf.Write(ciphertext)

		records = append(records, rec{
			name: name, flags: flags, method: method, modTime: 0x4a21, modDate: 0x5777,
			crc: headerCRC, compSize: uint32(len(ciphertext)), uncompSize: uint32(len(plain)),
			lOff: lOff, extra: extra,
		})
	}
	cdOff := uint32(buf.Len())
	for _, r := range records {
		ch := make([]byte, 46)
		binary.LittleEndian.PutUint32(ch[0:4], 0x02014b50)
		binary.LittleEndian.PutUint16(ch[4:6], 51)
		binary.LittleEndian.PutUint16(ch[6:8], 51)
		binary.LittleEndian.PutUint16(ch[8:10], r.flags)
		binary.LittleEndian.PutUint16(ch[10:12], r.method)
		binary.LittleEndian.PutUint16(ch[12:14], r.modTime)
		binary.LittleEndian.PutUint16(ch[14:16], r.modDate)
		binary.LittleEndian.PutUint32(ch[16:20], r.crc)
		binary.LittleEndian.PutUint32(ch[20:24], r.compSize)
		binary.LittleEndian.PutUint32(ch[24:28], r.uncompSize)
		binary.LittleEndian.PutUint16(ch[28:30], uint16(len(r.name)))
		binary.LittleEndian.PutUint16(ch[30:32], uint16(len(r.extra)))
		binary.LittleEndian.PutUint32(ch[42:46], r.lOff)
		buf.Write(ch)
		buf.WriteString(r.name)
		buf.Write(r.extra)
	}
	cdSize := uint32(buf.Len()) - cdOff
	eocd := make([]byte, 22)
	binary.LittleEndian.PutUint32(eocd[0:4], 0x06054b50)
	binary.LittleEndian.PutUint16(eocd[8:10], uint16(len(records)))
	binary.LittleEndian.PutUint16(eocd[10:12], uint16(len(records)))
	binary.LittleEndian.PutUint32(eocd[12:16], cdSize)
	binary.LittleEndian.PutUint32(eocd[16:20], cdOff)
	buf.Write(eocd)
	if err := os.WriteFile(dest, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write %s: %v", dest, err)
	}
}

func TestV29EncryptedArchivesUnlockAutoRetryAndAdminClear(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := database.Open(filepath.Join(dir, "v29.db"))
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

	ctx := context.Background()
	if _, err := store.ClaimAdmin(ctx, "admin", "secret1234"); err != nil {
		t.Fatalf("ClaimAdmin: %v", err)
	}

	// Standard multi-volume library + One-Shot library with shared folder passwords
	seriesRoot := filepath.Join(dir, "manga")
	writeEncryptedCBZForHTTPTest(t, filepath.Join(seriesRoot, "LockedSaga", "Vol.01.cbz"), "pw-alpha", 2, true)
	writeEncryptedCBZForHTTPTest(t, filepath.Join(seriesRoot, "LockedSaga", "Vol.02.cbz"), "pw-alpha", 2, false)

	oneShotRoot := filepath.Join(dir, "oneshots")
	writeEncryptedCBZForHTTPTest(t, filepath.Join(oneShotRoot, "GroupA", "OneShot1.cbz"), "pw-oneshot", 2, true)
	writeEncryptedCBZForHTTPTest(t, filepath.Join(oneShotRoot, "GroupA", "OneShot2.cbz"), "pw-oneshot", 2, true)

	for _, lib := range []database.Library{
		{ID: "lib_series", Name: "主书库", ProviderType: database.ProviderLocal, RootCID: "local:" + seriesRoot, RootPath: seriesRoot, Enabled: true},
		{ID: "lib_os", Name: "短篇库", ProviderType: database.ProviderLocal, RootCID: "local:" + oneShotRoot, RootPath: oneShotRoot, Enabled: true, OneShot: true},
	} {
		if err := store.UpsertLibrary(ctx, lib); err != nil {
			t.Fatalf("UpsertLibrary %s: %v", lib.ID, err)
		}
		run, err := scanMgr.StartLibrary(ctx, lib.ID, "manual")
		if err != nil {
			t.Fatalf("StartLibrary %s: %v", lib.ID, err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			runs, _ := scanMgr.Runs(ctx, 10)
			done := false
			for _, r := range runs {
				if r.ID == run.ID && (r.Status == "success" || r.Status == "failed") {
					if r.Status != "success" {
						t.Fatalf("scan %s failed: %s", lib.ID, r.Error)
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

	books, _, err := store.BooksPage(ctx, database.BookQuery{Unpaged: true})
	if err != nil || len(books) != 4 {
		t.Fatalf("expected 4 books, got %d err=%v", len(books), err)
	}
	byName := map[string]database.Book{}
	for _, b := range books {
		byName[b.Name] = b
	}
	vol1 := byName["Vol.01.cbz"]
	vol2 := byName["Vol.02.cbz"]
	os1 := byName["OneShot1.cbz"]
	os2 := byName["OneShot2.cbz"]

	doReq := func(method, target string, body any, authed bool) *httptest.ResponseRecorder {
		var rdr io.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			rdr = bytes.NewReader(raw)
		}
		req := httptest.NewRequest(method, target, rdr)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if authed {
			req.SetBasicAuth("admin", "secret1234")
		}
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	// Unauthenticated admin password list must be blocked
	if rec := doReq(http.MethodGet, "/admin/api/archive-passwords", nil, false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth /admin/api/archive-passwords expected 401, got %d", rec.Code)
	}

	// 1. Opening locked Vol.01 returns 422 ARCHIVE_PASSWORD_REQUIRED
	rec := doReq(http.MethodGet, "/api/v1/books/"+vol1.ID+"/pages", nil, true)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "ARCHIVE_PASSWORD_REQUIRED") {
		t.Fatalf("locked vol1 expected ARCHIVE_PASSWORD_REQUIRED, got %d %s", rec.Code, rec.Body.String())
	}

	// 2. Unlock with wrong password returns 422 ARCHIVE_PASSWORD_INVALID and saves nothing
	rec = doReq(http.MethodPost, "/admin/api/books/"+vol1.ID+"/unlock", map[string]string{"password": "wrong"}, true)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "ARCHIVE_PASSWORD_INVALID") {
		t.Fatalf("wrong password expected ARCHIVE_PASSWORD_INVALID, got %d %s", rec.Code, rec.Body.String())
	}

	// 3. Unlock Vol.01 with correct password -> 200 OK
	rec = doReq(http.MethodPost, "/admin/api/books/"+vol1.ID+"/unlock", map[string]string{"password": "pw-alpha"}, true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"pageCount":2`) {
		t.Fatalf("unlock vol1 failed: %d %s", rec.Code, rec.Body.String())
	}

	// 4. Vol.01 pages + page 1 image now succeed and decode as valid PNG
	if rec := doReq(http.MethodGet, "/api/v1/books/"+vol1.ID+"/pages", nil, true); rec.Code != http.StatusOK {
		t.Fatalf("vol1 pages after unlock expected 200, got %d %s", rec.Code, rec.Body.String())
	}
	imgRec := doReq(http.MethodGet, "/api/v1/books/"+vol1.ID+"/pages/1", nil, true)
	if imgRec.Code != http.StatusOK || imgRec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("vol1 page 1 failed: %d ct=%s", imgRec.Code, imgRec.Header().Get("Content-Type"))
	}
	if _, err := png.Decode(bytes.NewReader(imgRec.Body.Bytes())); err != nil {
		t.Fatalf("vol1 page 1 invalid PNG: %v", err)
	}

	// 5. Same-folder multi-volume auto-retry: Vol.02 opens WITHOUT prompting and auto-binds!
	if rec := doReq(http.MethodGet, "/api/v1/books/"+vol2.ID+"/pages", nil, true); rec.Code != http.StatusOK {
		t.Fatalf("vol2 same-folder auto-retry failed: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doReq(http.MethodGet, "/api/v1/books/"+vol2.ID+"/pages/2", nil, true); rec.Code != http.StatusOK {
		t.Fatalf("vol2 page 2 after auto-retry failed: %d %s", rec.Code, rec.Body.String())
	}

	// 6. Same-folder One-Shot auto-retry: unlock OneShot1 -> OneShot2 (different series_id, same folder!) auto-unlocks!
	if rec := doReq(http.MethodPost, "/admin/api/books/"+os1.ID+"/unlock", map[string]string{"password": "pw-oneshot"}, true); rec.Code != http.StatusOK {
		t.Fatalf("unlock oneshot1 failed: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doReq(http.MethodGet, "/api/v1/books/"+os2.ID+"/pages", nil, true); rec.Code != http.StatusOK {
		t.Fatalf("oneshot2 same-folder auto-retry failed: %d %s", rec.Code, rec.Body.String())
	}

	// 7. OPDS endpoint reads unlocked book transparently too!
	if rec := doReq(http.MethodGet, "/opds/v1.2/books/"+vol1.ID, nil, true); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `pse:count="2"`) {
		t.Fatalf("OPDS book detail on unlocked archive failed: %d %s", rec.Code, rec.Body.String())
	}

	// 8. Admin password list shows 2 grouped passwords, 4 bindings total
	listRec := doReq(http.MethodGet, "/admin/api/archive-passwords", nil, true)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list archive passwords failed: %d %s", listRec.Code, listRec.Body.String())
	}
	var listResp struct {
		Groups         []database.ArchivePasswordGroup `json:"groups"`
		TotalPasswords int                             `json:"totalPasswords"`
		TotalBindings  int                             `json:"totalBindings"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode archive passwords: %v", err)
	}
	if listResp.TotalPasswords != 2 || listResp.TotalBindings != 4 {
		t.Fatalf("expected 2 password groups & 4 bindings, got %+v", listResp)
	}
	var alphaGroupID string
	for _, g := range listResp.Groups {
		if g.Password == "pw-alpha" {
			alphaGroupID = g.ID
			if len(g.Bindings) != 2 {
				t.Fatalf("expected pw-alpha to bind 2 books, got %d", len(g.Bindings))
			}
		}
	}
	if alphaGroupID == "" {
		t.Fatalf("missing pw-alpha group in %+v", listResp.Groups)
	}

	// 9. Delete pw-alpha group -> BOTH Vol.01 & Vol.02 re-lock immediately (and page cache is purged!)
	if rec := doReq(http.MethodDelete, "/admin/api/archive-passwords/"+alphaGroupID, nil, true); rec.Code != http.StatusOK {
		t.Fatalf("delete pw-alpha group failed: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doReq(http.MethodGet, "/api/v1/books/"+vol1.ID+"/pages/1", nil, true); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "ARCHIVE_PASSWORD_REQUIRED") {
		t.Fatalf("vol1 page 1 MUST re-lock after password group delete (no stale cache leak): got %d %s", rec.Code, rec.Body.String())
	}

	// 10. Clear all remaining passwords -> OneShot1 & OneShot2 re-lock and list becomes empty!
	if rec := doReq(http.MethodDelete, "/admin/api/archive-passwords", nil, true); rec.Code != http.StatusOK {
		t.Fatalf("clear all archive passwords failed: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doReq(http.MethodGet, "/api/v1/books/"+os1.ID+"/pages", nil, true); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "ARCHIVE_PASSWORD_REQUIRED") {
		t.Fatalf("oneshot1 MUST re-lock after clear all: got %d %s", rec.Code, rec.Body.String())
	}
	listRec2 := doReq(http.MethodGet, "/admin/api/archive-passwords", nil, true)
	if !strings.Contains(listRec2.Body.String(), `"totalPasswords":0`) {
		t.Fatalf("expected 0 passwords after clear all, got %s", listRec2.Body.String())
	}
}
