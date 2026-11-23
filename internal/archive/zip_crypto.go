package archive

import (
	"archive/zip"
	"context"
	"crypto/aes"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"hash/crc32"

	"golang.org/x/crypto/pbkdf2"

	"github.com/xJogger/fake-komga-115/internal/database"
)

const (
	zipMethodWinZipAES = 99
	zipExtraWinZipAES  = 0x9901

	encSchemeZipCrypto = "zipcrypto"
	encSchemeAES128    = "aes128"
	encSchemeAES192    = "aes192"
	encSchemeAES256    = "aes256"
	encSchemeRAR       = "rar"
)

type archivePasswordContextKey struct{}

type archivePasswordContext struct {
	explicitPassword string
	hasExplicit      bool
	logicalBook      *database.Book
}

// WithExplicitArchivePassword attaches a candidate password to ctx so ListPages/ReadPage
// validate ONLY that password (used by the unlock verification flow before persisting).
func WithExplicitArchivePassword(ctx context.Context, password string) context.Context {
	cur, _ := ctx.Value(archivePasswordContextKey{}).(archivePasswordContext)
	cur.explicitPassword = password
	cur.hasExplicit = true
	return context.WithValue(ctx, archivePasswordContextKey{}, cur)
}

func withLogicalBookContext(ctx context.Context, logicalBook database.Book) context.Context {
	cur, _ := ctx.Value(archivePasswordContextKey{}).(archivePasswordContext)
	cp := logicalBook
	cur.logicalBook = &cp
	return context.WithValue(ctx, archivePasswordContextKey{}, cur)
}

func explicitArchivePasswordFromContext(ctx context.Context) (string, bool) {
	if cur, ok := ctx.Value(archivePasswordContextKey{}).(archivePasswordContext); ok && cur.hasExplicit {
		return cur.explicitPassword, true
	}
	return "", false
}

func logicalBookFromContext(ctx context.Context) (database.Book, bool) {
	if cur, ok := ctx.Value(archivePasswordContextKey{}).(archivePasswordContext); ok && cur.logicalBook != nil {
		return *cur.logicalBook, true
	}
	return database.Book{}, false
}

func resolveArchivePasswordCandidates(ctx context.Context, store *database.Store, book database.Book) ([]database.ArchivePasswordCandidate, bool, error) {
	if pw, ok := explicitArchivePasswordFromContext(ctx); ok {
		if pw == "" {
			return nil, true, nil
		}
		return []database.ArchivePasswordCandidate{{Password: pw, Source: "explicit"}}, true, nil
	}
	if store == nil {
		return nil, false, nil
	}
	books := []database.Book{book}
	if logical, ok := logicalBookFromContext(ctx); ok && logical.ID != "" && logical.ID != book.ID {
		books = append([]database.Book{logical}, books...)
	}
	cands, err := store.ArchivePasswordCandidatesForBook(ctx, books...)
	return cands, false, err
}

func maybeAutoBindArchivePassword(ctx context.Context, store *database.Store, book database.Book, cand database.ArchivePasswordCandidate) {
	if store == nil || cand.Password == "" || cand.Source == "direct" || cand.Source == "explicit" {
		return
	}
	logical, hasLogical := logicalBookFromContext(ctx)
	if hasLogical && logical.ID != "" && logical.ID != book.ID {
		_ = store.SaveArchivePasswordBinding(ctx, logical, cand.Password, book)
		return
	}
	_ = store.SaveArchivePasswordBinding(ctx, book, cand.Password)
}

// --- ZIP Encryption Parsing & Decryption (ZipCrypto + WinZip AES-128/192/256) ---

type winZipAESInfo struct {
	AEVersion  uint16
	Strength   byte   // 1=128, 2=192, 3=256
	RealMethod uint16 // zip.Store or zip.Deflate
}

func parseWinZipAESExtra(extra []byte) (winZipAESInfo, bool) {
	for i := 0; i+4 <= len(extra); {
		headerID := binary.LittleEndian.Uint16(extra[i : i+2])
		dataSize := int(binary.LittleEndian.Uint16(extra[i+2 : i+4]))
		i += 4
		if i+dataSize > len(extra) {
			break
		}
		if headerID == zipExtraWinZipAES && dataSize >= 7 {
			body := extra[i : i+dataSize]
			aeVer := binary.LittleEndian.Uint16(body[0:2])
			if (aeVer == 1 || aeVer == 2) && body[2] == 'A' && body[3] == 'E' {
				strength := body[4]
				realMethod := binary.LittleEndian.Uint16(body[5:7])
				if strength >= 1 && strength <= 3 {
					return winZipAESInfo{
						AEVersion:  aeVer,
						Strength:   strength,
						RealMethod: realMethod,
					}, true
				}
			}
		}
		i += dataSize
	}
	return winZipAESInfo{}, false
}

func winZipAESScheme(strength byte) string {
	switch strength {
	case 1:
		return encSchemeAES128
	case 2:
		return encSchemeAES192
	case 3:
		return encSchemeAES256
	default:
		return ""
	}
}

func winZipAESParams(scheme string) (keyLen, saltLen int, ok bool) {
	switch scheme {
	case encSchemeAES128:
		return 16, 8, true
	case encSchemeAES192:
		return 24, 12, true
	case encSchemeAES256:
		return 32, 16, true
	default:
		return 0, 0, false
	}
}

func zipCryptoCheckByte(file *zip.File) byte {
	// Per APPNOTE.TXT 6.1.5: if bit 3 (data descriptor) is set, the high-order byte
	// of the last mod file time is used instead of the high-order byte of CRC-32.
	if file.Flags&0x8 != 0 {
		return byte(file.ModifiedTime >> 8)
	}
	return byte(file.CRC32 >> 24)
}

func inspectZIPEntryCrypto(file *zip.File) (encrypted bool, scheme string, realMethod uint16, checkByte byte, aeVer uint16, err error) {
	if file.Flags&1 == 0 && file.Method != zipMethodWinZipAES {
		if file.Method != zip.Store && file.Method != zip.Deflate {
			return false, "", 0, 0, 0, ErrUnsupportedZIP
		}
		return false, "", file.Method, 0, 0, nil
	}
	if file.Method == zipMethodWinZipAES {
		info, ok := parseWinZipAESExtra(file.Extra)
		if !ok || (info.RealMethod != zip.Store && info.RealMethod != zip.Deflate) {
			return true, "", 0, 0, 0, ErrUnsupportedZIP
		}
		return true, winZipAESScheme(info.Strength), info.RealMethod, 0, info.AEVersion, nil
	}
	if file.Method != zip.Store && file.Method != zip.Deflate {
		return true, "", 0, 0, 0, ErrUnsupportedZIP
	}
	return true, encSchemeZipCrypto, file.Method, zipCryptoCheckByte(file), 0, nil
}

// decryptZipCrypto decrypts a Traditional PKWARE encrypted ZIP entry payload.
func decryptZipCrypto(ciphertext []byte, password string, checkByte byte) ([]byte, error) {
	if len(ciphertext) < 12 {
		return nil, ErrInvalidZIP
	}
	k0 := uint32(0x12345678)
	k1 := uint32(0x23456789)
	k2 := uint32(0x34567890)
	updateKeys := func(b byte) {
		k0 = crc32UpdateByte(k0, b)
		k1 = (k1+(k0&0xff))*134775813 + 1
		k2 = crc32UpdateByte(k2, byte(k1>>24))
	}
	decryptByte := func() byte {
		tmp := (k2 & 0xffff) | 2
		return byte((tmp * (tmp ^ 1)) >> 8)
	}

	for i := 0; i < len(password); i++ {
		updateKeys(password[i])
	}
	var lastHeaderByte byte
	for i := 0; i < 12; i++ {
		plain := ciphertext[i] ^ decryptByte()
		updateKeys(plain)
		lastHeaderByte = plain
	}
	if lastHeaderByte != checkByte {
		return nil, ErrArchivePasswordInvalid
	}

	plain := make([]byte, len(ciphertext)-12)
	for i, c := range ciphertext[12:] {
		p := c ^ decryptByte()
		updateKeys(p)
		plain[i] = p
	}
	return plain, nil
}

func crc32UpdateByte(crc uint32, b byte) uint32 {
	return crc32.IEEETable[(crc^uint32(b))&0xff] ^ (crc >> 8)
}

// decryptWinZipAES decrypts a WinZip AE-1 / AE-2 (AES-128/192/256 CTR + HMAC-SHA1-80) payload.
func decryptWinZipAES(ciphertext []byte, password, scheme string) ([]byte, error) {
	keyLen, saltLen, ok := winZipAESParams(scheme)
	if !ok {
		return nil, ErrUnsupportedZIP
	}
	const (
		pwVerifyLen = 2
		authCodeLen = 10
	)
	if len(ciphertext) < saltLen+pwVerifyLen+authCodeLen {
		return nil, ErrInvalidZIP
	}
	salt := ciphertext[:saltLen]
	pwVerify := ciphertext[saltLen : saltLen+pwVerifyLen]
	encData := ciphertext[saltLen+pwVerifyLen : len(ciphertext)-authCodeLen]
	authCode := ciphertext[len(ciphertext)-authCodeLen:]

	derived := pbkdf2.Key([]byte(password), salt, 1000, 2*keyLen+pwVerifyLen, sha1.New)
	aesKey := derived[:keyLen]
	hmacKey := derived[keyLen : 2*keyLen]
	expectedVerify := derived[2*keyLen : 2*keyLen+pwVerifyLen]

	if subtle.ConstantTimeCompare(pwVerify, expectedVerify) != 1 {
		return nil, ErrArchivePasswordInvalid
	}
	mac := hmac.New(sha1.New, hmacKey)
	_, _ = mac.Write(encData)
	sum := mac.Sum(nil)
	if subtle.ConstantTimeCompare(authCode, sum[:authCodeLen]) != 1 {
		return nil, ErrArchivePasswordInvalid
	}

	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return nil, err
	}
	// WinZip AES uses CTR mode with a 128-bit little-endian counter starting at 1.
	var counter [aes.BlockSize]byte
	var keystream [aes.BlockSize]byte
	counter[0] = 1
	plain := make([]byte, len(encData))
	for off := 0; off < len(encData); off += aes.BlockSize {
		block.Encrypt(keystream[:], counter[:])
		// Increment 128-bit counter in little-endian order.
		for i := 0; i < aes.BlockSize; i++ {
			counter[i]++
			if counter[i] != 0 {
				break
			}
		}
		end := off + aes.BlockSize
		if end > len(encData) {
			end = len(encData)
		}
		for j := off; j < end; j++ {
			plain[j] = encData[j] ^ keystream[j-off]
		}
	}
	return plain, nil
}

func decryptZIPEntryPayload(ciphertext []byte, entry PageEntry, password string) ([]byte, error) {
	if !entry.Encrypted {
		return ciphertext, nil
	}
	switch entry.EncScheme {
	case encSchemeZipCrypto:
		return decryptZipCrypto(ciphertext, password, entry.EncCheckByte)
	case encSchemeAES128, encSchemeAES192, encSchemeAES256:
		return decryptWinZipAES(ciphertext, password, entry.EncScheme)
	default:
		return nil, ErrUnsupportedZIP
	}
}

func isArchivePasswordError(err error) bool {
	return errors.Is(err, ErrArchivePasswordRequired) || errors.Is(err, ErrArchivePasswordInvalid)
}
