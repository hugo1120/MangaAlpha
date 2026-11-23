package archive

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
	"errors"
	"hash/crc32"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/pbkdf2"

	"github.com/xJogger/fake-komga-115/internal/cache"
	"github.com/xJogger/fake-komga-115/internal/database"
)

// buildEncryptedTestZIP constructs a genuine spec-compliant ZIP archive with either
// ZipCrypto (Traditional PKWARE) or WinZip AES-128/256 (AE-1 / AE-2) encrypted entries.
func buildEncryptedTestZIP(
	t *testing.T,
	entries map[string][]byte,
	password string,
	mode string, // "zipcrypto-store", "zipcrypto-deflate", "aes256-ae2", "aes128-ae1"
) []byte {
	t.Helper()
	var buf bytes.Buffer
	type cdRecord struct {
		name             string
		flags            uint16
		method           uint16
		modTime          uint16
		modDate          uint16
		crc              uint32
		compressedSize   uint32
		uncompressedSize uint32
		extra            []byte
		localOffset      uint32
	}
	var records []cdRecord

	for name, plain := range entries {
		localOff := uint32(buf.Len())
		origCRC := crc32.ChecksumIEEE(plain)
		modTime := uint16(0x4a21)
		modDate := uint16(0x5777)

		var (
			flags      uint16 = 0x0001 // encrypted
			method     uint16
			headerCRC  uint32
			extra      []byte
			ciphertext []byte
		)

		switch mode {
		case "zipcrypto-store", "zipcrypto-deflate":
			payload := plain
			method = zip.Store
			if mode == "zipcrypto-deflate" {
				var cb bytes.Buffer
				fw, err := flate.NewWriter(&cb, flate.DefaultCompression)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = fw.Write(plain)
				_ = fw.Close()
				payload = cb.Bytes()
				method = zip.Deflate
			}
			headerCRC = origCRC
			ciphertext = encryptZipCryptoForTest(payload, password, byte(origCRC>>24))

		case "aes256-ae2", "aes128-ae1":
			var cb bytes.Buffer
			fw, err := flate.NewWriter(&cb, flate.DefaultCompression)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = fw.Write(plain)
			_ = fw.Close()
			payload := cb.Bytes()

			method = zipMethodWinZipAES
			strength := byte(3)
			aeVer := uint16(2)
			keyLen, saltLen := 32, 16
			headerCRC = 0 // AE-2 spec sets CRC to 0
			if mode == "aes128-ae1" {
				strength = 1
				aeVer = 1
				keyLen, saltLen = 16, 8
				headerCRC = origCRC
			}
			extra = make([]byte, 11)
			binary.LittleEndian.PutUint16(extra[0:2], zipExtraWinZipAES)
			binary.LittleEndian.PutUint16(extra[2:4], 7)
			binary.LittleEndian.PutUint16(extra[4:6], aeVer)
			extra[6] = 'A'
			extra[7] = 'E'
			extra[8] = strength
			binary.LittleEndian.PutUint16(extra[9:11], zip.Deflate)
			ciphertext = encryptWinZipAESForTest(t, payload, password, keyLen, saltLen)
		default:
			t.Fatalf("unknown mode %q", mode)
		}

		// Local File Header
		lh := make([]byte, 30)
		binary.LittleEndian.PutUint32(lh[0:4], 0x04034b50)
		binary.LittleEndian.PutUint16(lh[4:6], 51)
		binary.LittleEndian.PutUint16(lh[6:8], flags)
		binary.LittleEndian.PutUint16(lh[8:10], method)
		binary.LittleEndian.PutUint16(lh[10:12], modTime)
		binary.LittleEndian.PutUint16(lh[12:14], modDate)
		binary.LittleEndian.PutUint32(lh[14:18], headerCRC)
		binary.LittleEndian.PutUint32(lh[18:22], uint32(len(ciphertext)))
		binary.LittleEndian.PutUint32(lh[22:26], uint32(len(plain)))
		binary.LittleEndian.PutUint16(lh[26:28], uint16(len(name)))
		binary.LittleEndian.PutUint16(lh[28:30], uint16(len(extra)))
		buf.Write(lh)
		buf.WriteString(name)
		buf.Write(extra)
		buf.Write(ciphertext)

		records = append(records, cdRecord{
			name: name, flags: flags, method: method, modTime: modTime, modDate: modDate,
			crc: headerCRC, compressedSize: uint32(len(ciphertext)), uncompressedSize: uint32(len(plain)),
			extra: extra, localOffset: localOff,
		})
	}

	cdOffset := uint32(buf.Len())
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
		binary.LittleEndian.PutUint32(ch[20:24], r.compressedSize)
		binary.LittleEndian.PutUint32(ch[24:28], r.uncompressedSize)
		binary.LittleEndian.PutUint16(ch[28:30], uint16(len(r.name)))
		binary.LittleEndian.PutUint16(ch[30:32], uint16(len(r.extra)))
		binary.LittleEndian.PutUint32(ch[42:46], r.localOffset)
		buf.Write(ch)
		buf.WriteString(r.name)
		buf.Write(r.extra)
	}
	cdSize := uint32(buf.Len()) - cdOffset

	eocd := make([]byte, 22)
	binary.LittleEndian.PutUint32(eocd[0:4], 0x06054b50)
	binary.LittleEndian.PutUint16(eocd[8:10], uint16(len(records)))
	binary.LittleEndian.PutUint16(eocd[10:12], uint16(len(records)))
	binary.LittleEndian.PutUint32(eocd[12:16], cdSize)
	binary.LittleEndian.PutUint32(eocd[16:20], cdOffset)
	buf.Write(eocd)
	return buf.Bytes()
}

func encryptZipCryptoForTest(plain []byte, password string, checkByte byte) []byte {
	k0 := uint32(0x12345678)
	k1 := uint32(0x23456789)
	k2 := uint32(0x34567890)
	updateKeys := func(b byte) {
		k0 = crc32UpdateByte(k0, b)
		k1 = (k1+(k0&0xff))*134775813 + 1
		k2 = crc32UpdateByte(k2, byte(k1>>24))
	}
	keystreamByte := func() byte {
		tmp := (k2 & 0xffff) | 2
		return byte((tmp * (tmp ^ 1)) >> 8)
	}
	for i := 0; i < len(password); i++ {
		updateKeys(password[i])
	}
	header := make([]byte, 12)
	_, _ = rand.Read(header[:11])
	header[11] = checkByte

	out := make([]byte, 0, 12+len(plain))
	for _, b := range header {
		ks := keystreamByte()
		updateKeys(b)
		out = append(out, b^ks)
	}
	for _, b := range plain {
		ks := keystreamByte()
		updateKeys(b)
		out = append(out, b^ks)
	}
	return out
}

func encryptWinZipAESForTest(t *testing.T, plain []byte, password string, keyLen, saltLen int) []byte {
	t.Helper()
	salt := make([]byte, saltLen)
	_, _ = rand.Read(salt)
	derived := pbkdf2.Key([]byte(password), salt, 1000, 2*keyLen+2, sha1.New)
	aesKey := derived[:keyLen]
	hmacKey := derived[keyLen : 2*keyLen]
	pwVerify := derived[2*keyLen : 2*keyLen+2]

	block, err := aes.NewCipher(aesKey)
	if err != nil {
		t.Fatal(err)
	}
	var counter [aes.BlockSize]byte
	var keystream [aes.BlockSize]byte
	counter[0] = 1
	enc := make([]byte, len(plain))
	for off := 0; off < len(plain); off += aes.BlockSize {
		block.Encrypt(keystream[:], counter[:])
		for i := 0; i < aes.BlockSize; i++ {
			counter[i]++
			if counter[i] != 0 {
				break
			}
		}
		end := off + aes.BlockSize
		if end > len(plain) {
			end = len(plain)
		}
		for j := off; j < end; j++ {
			enc[j] = plain[j] ^ keystream[j-off]
		}
	}
	mac := hmac.New(sha1.New, hmacKey)
	_, _ = mac.Write(enc)
	authCode := mac.Sum(nil)[:10]

	var out bytes.Buffer
	out.Write(salt)
	out.Write(pwVerify)
	out.Write(enc)
	out.Write(authCode)
	return out.Bytes()
}

func TestEncryptedZIPSchemesEndToEnd(t *testing.T) {
	modes := []string{"zipcrypto-store", "zipcrypto-deflate", "aes256-ae2", "aes128-ae1"}
	page1Bytes := []byte("\x89PNG\r\n\x1a\nFAKE_PNG_PAGE_1_DATA_1234567890")
	page2Bytes := []byte("\x89PNG\r\n\x1a\nFAKE_PNG_PAGE_2_DATA_ABCDEFGHIJ")

	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			store, err := database.Open(filepath.Join(dir, "enc.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()

			cacheMgr, err := cache.New(store, filepath.Join(dir, "cache"))
			if err != nil {
				t.Fatal(err)
			}
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			svc := NewService(store, nil, cacheMgr, logger)
			defer svc.Close()

			zipBytes := buildEncryptedTestZIP(t, map[string][]byte{
				"001.png": page1Bytes,
				"002.png": page2Bytes,
			}, "manga-secret-42", mode)
			zipPath := filepath.Join(dir, "Vol.01.cbz")
			if err := os.WriteFile(zipPath, zipBytes, 0o600); err != nil {
				t.Fatal(err)
			}

			ctx := context.Background()
			if err := store.UpsertLibrary(ctx, database.Library{
				ID: "lib1", Name: "Local", ProviderType: database.ProviderLocal,
				RootCID: "local:" + dir, RootPath: dir, Enabled: true,
			}); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC().Format(time.RFC3339Nano)
			_, _ = store.DB().Exec(`INSERT INTO series(id,library_id,cid,name,relative_path,created_at,updated_at,seen_scan_id) VALUES('s1','lib1','dir1','Series','Series',?,?,'scan')`, now, now)
			_, _ = store.DB().Exec(`INSERT INTO books(id,series_id,library_id,file_id,parent_cid,name,size,pick_code,sha1,file_created_at,file_modified_at,number_sort,created_at,updated_at,seen_scan_id) VALUES('b1','s1','lib1','Vol.01.cbz','dir1','Vol.01.cbz',?,'local','',?,?,1,?,?,'scan')`, len(zipBytes), now, now, now, now)
			book, err := store.BookByID(ctx, "b1")
			if err != nil {
				t.Fatal(err)
			}

			// 1. Without password -> ErrArchivePasswordRequired
			if _, err := svc.ListPages(ctx, book); !errors.Is(err, ErrArchivePasswordRequired) {
				t.Fatalf("expected ErrArchivePasswordRequired, got %v", err)
			}
			// 2. Wrong password -> ErrArchivePasswordInvalid
			if _, err := svc.VerifyAndBindBookPassword(ctx, book, "wrong-password"); !errors.Is(err, ErrArchivePasswordInvalid) {
				t.Fatalf("expected ErrArchivePasswordInvalid, got %v", err)
			}
			// 3. Correct password -> succeeds and binds
			count, err := svc.VerifyAndBindBookPassword(ctx, book, "manga-secret-42")
			if err != nil || count != 2 {
				t.Fatalf("VerifyAndBindBookPassword: count=%d err=%v", count, err)
			}
			// 4. Subsequent ListPages + ReadPage work transparently
			p1, err := svc.ReadPage(ctx, book, 1)
			if err != nil || !bytes.Equal(p1.Data, page1Bytes) {
				t.Fatalf("ReadPage(1): err=%v data=%q", err, p1.Data)
			}
			p2, err := svc.ReadPage(ctx, book, 2)
			if err != nil || !bytes.Equal(p2.Data, page2Bytes) {
				t.Fatalf("ReadPage(2): err=%v data=%q", err, p2.Data)
			}
			// 5. Clear password + purge page cache -> re-locks immediately!
			affected, err := store.ClearAllArchivePasswords(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := cacheMgr.DeleteBookPages(ctx, affected); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.ReadPage(ctx, book, 1); !errors.Is(err, ErrArchivePasswordRequired) {
				t.Fatalf("expected re-locked book after clear, got %v", err)
			}
		})
	}
}
