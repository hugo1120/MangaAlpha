package extmeta

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"regexp"
	"strings"
	"unicode"

	"github.com/xJogger/fake-komga-115/internal/cjk"
)

const (
	// MappingSchemaV1 identifies the sanitized community mapping NDJSON record schema.
	MappingSchemaV1 = "mangaalpha_mapping_v1"

	// Allowed TargetSource values for MappingRecord.
	TargetSourceBangumi   = "bangumi"
	TargetSourceGalleryV1 = "gallery_v1"
	TargetSourceMetaV1    = "mangaalpha_meta_v1"

	maxMappingsStreamBytes int64 = 64 << 20
)

var (
	winDrivePathRe = regexp.MustCompile(`(?i)(?:^|[\s"'=])(?:[a-z]:\\|[a-z]:/|\\\\[a-z0-9._-]+\\)`)
	unixPathRe     = regexp.MustCompile(`(?i)(?:^|[\s"'=])/(?:home|users|root|mnt|media|share|volumes?|storage|sdcard|var|etc|tmp|opt|srv|data)(?:/|$)`)
	urlSchemeRe    = regexp.MustCompile(`(?i)(?:https?|ftp|davs?|smb|file|socks5h?|wss?)://|magnet:\?`)
	jwtLikeRe      = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{4,}\b`)
)

// MappingRecord represents one line in a mangaalpha_mapping_v1 NDJSON mapping pack.
type MappingRecord struct {
	Schema       string `json:"schema"`
	Key          string `json:"key"`
	RawKey       string `json:"rawKey,omitempty"`
	Author       string `json:"author,omitempty"`
	TargetSource string `json:"targetSource"`
	TargetID     string `json:"targetId"`
	IsNSFW       bool   `json:"isNsfw,omitempty"`
	Origin       string `json:"origin,omitempty"`
}

type mappingRecordWire struct {
	Schema          string `json:"schema"`
	Key             string `json:"key"`
	RawKey          string `json:"rawKey,omitempty"`
	RawKeySnake     string `json:"raw_key,omitempty"`
	Author          string `json:"author,omitempty"`
	TargetSource    string `json:"targetSource"`
	TargetSourceSnk string `json:"target_source,omitempty"`
	TargetID        string `json:"targetId"`
	TargetIDSnake   string `json:"target_id,omitempty"`
	IsNSFW          *bool  `json:"isNsfw,omitempty"`
	IsNSFWSnake     *bool  `json:"is_nsfw,omitempty"`
	Origin          string `json:"origin,omitempty"`
}

// UnmarshalJSON decodes a MappingRecord accepting both camelCase and snake_case field names.
func (m *MappingRecord) UnmarshalJSON(data []byte) error {
	var w mappingRecordWire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	m.Schema = w.Schema
	m.Key = w.Key
	m.RawKey = firstNonEmpty(w.RawKey, w.RawKeySnake)
	m.Author = w.Author
	m.TargetSource = firstNonEmpty(w.TargetSource, w.TargetSourceSnk)
	m.TargetID = firstNonEmpty(w.TargetID, w.TargetIDSnake)
	if w.IsNSFW != nil {
		m.IsNSFW = *w.IsNSFW
	} else if w.IsNSFWSnake != nil {
		m.IsNSFW = *w.IsNSFWSnake
	}
	m.Origin = w.Origin
	return nil
}

// ExportMappingsOptions controls filtering when sanitizing and exporting mappings.
type ExportMappingsOptions struct {
	IncludeNSFW    bool
	AllowedOrigins []string
}

// ParseMappingsNDJSON parses plain .ndjson or gzipped .ndjson.gz streams, skipping blank,
// malformed, or privacy-sensitive lines and deduplicating by (normKey, normAuthor, targetSource).
func ParseMappingsNDJSON(r io.Reader) ([]MappingRecord, error) {
	if r == nil {
		return nil, errors.New("mappings reader must not be nil")
	}

	br := bufio.NewReaderSize(r, 64<<10)
	magic, err := br.Peek(2)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmtErrorWrap("peek mappings stream", err)
	}

	var src io.Reader = br
	if len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, gErr := gzip.NewReader(br)
		if gErr != nil {
			return nil, fmtErrorWrap("open gzip mappings", gErr)
		}
		defer gz.Close()
		src = gz
	}

	limited := io.LimitReader(src, maxMappingsStreamBytes+1)
	lineReader := bufio.NewReaderSize(limited, 64<<10)

	seen := make(map[string]struct{})
	var out []MappingRecord
	var totalBytes int64

	for {
		line, readErr := lineReader.ReadBytes('\n')
		totalBytes += int64(len(line))
		if totalBytes > maxMappingsStreamBytes {
			return nil, errors.New("mappings stream exceeds size limit")
		}

		trimmed := bytes.TrimSpace(line)
		trimmed = bytes.TrimPrefix(trimmed, []byte("\xEF\xBB\xBF"))
		if len(trimmed) > 0 {
			var rec MappingRecord
			if json.Unmarshal(trimmed, &rec) == nil {
				if clean, ok := sanitizeMappingRecord(rec, true); ok {
					dk := mappingDedupKey(clean)
					if _, dup := seen[dk]; !dup {
						seen[dk] = struct{}{}
						out = append(out, clean)
					}
				}
			}
		}

		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}

	return out, nil
}

// MappingIndex supports fast priority-based lookup over sanitized community mapping records.
type MappingIndex struct {
	records     []MappingRecord
	byKey       map[string][]int // key: targetSource + "\x00" + normKey
	byRawKey    map[string][]int // key: targetSource + "\x00" + normRawKey
	byKeyAny    map[string][]int // key: normKey
	byRawKeyAny map[string][]int // key: normRawKey
}

// NewMappingIndex builds an immutable lookup index from mapping records.
func NewMappingIndex(records []MappingRecord) *MappingIndex {
	idx := &MappingIndex{
		records:     make([]MappingRecord, 0, len(records)),
		byKey:       make(map[string][]int, len(records)),
		byRawKey:    make(map[string][]int, len(records)),
		byKeyAny:    make(map[string][]int, len(records)),
		byRawKeyAny: make(map[string][]int, len(records)),
	}

	seen := make(map[string]struct{}, len(records))
	for _, r := range records {
		clean, ok := sanitizeMappingRecord(r, r.Schema != "")
		if !ok {
			continue
		}
		dk := mappingDedupKey(clean)
		if _, dup := seen[dk]; dup {
			continue
		}
		seen[dk] = struct{}{}

		pos := len(idx.records)
		idx.records = append(idx.records, clean)

		nk := cjk.ExactTitleKey(clean.Key)
		if nk != "" {
			idx.byKey[clean.TargetSource+"\x00"+nk] = append(idx.byKey[clean.TargetSource+"\x00"+nk], pos)
			idx.byKeyAny[nk] = append(idx.byKeyAny[nk], pos)
		}

		if clean.RawKey != "" {
			nrkSearch := cjk.SearchKey(clean.RawKey)
			nrkExact := cjk.ExactTitleKey(clean.RawKey)
			if nrkSearch != "" {
				idx.byRawKey[clean.TargetSource+"\x00"+nrkSearch] = append(idx.byRawKey[clean.TargetSource+"\x00"+nrkSearch], pos)
				idx.byRawKeyAny[nrkSearch] = append(idx.byRawKeyAny[nrkSearch], pos)
			}
			if nrkExact != "" && nrkExact != nrkSearch {
				idx.byRawKey[clean.TargetSource+"\x00"+nrkExact] = append(idx.byRawKey[clean.TargetSource+"\x00"+nrkExact], pos)
				idx.byRawKeyAny[nrkExact] = append(idx.byRawKeyAny[nrkExact], pos)
			}
		}
	}
	return idx
}

// Len returns the number of indexed mapping records.
func (idx *MappingIndex) Len() int {
	if idx == nil {
		return 0
	}
	return len(idx.records)
}

// Lookup searches the index in strict priority order:
// 1) (normKey + normAuthor)
// 2) (normKey without author conflict)
// 3) (normRawKey + normAuthor)
// 4) (normRawKey without author conflict)
func (idx *MappingIndex) Lookup(normKey, normRawKey, normAuthor, targetSource string) (MappingRecord, bool) {
	if idx == nil || len(idx.records) == 0 {
		return MappingRecord{}, false
	}

	nk := cjk.ExactTitleKey(normKey)
	nrkSearch := cjk.SearchKey(normRawKey)
	nrkExact := cjk.ExactTitleKey(normRawKey)
	na := cjk.NormalizeMatchKey(normAuthor)
	ts := strings.TrimSpace(targetSource)

	lookupBucket := func(bySrc map[string][]int, byAny map[string][]int, k string) []int {
		if k == "" {
			return nil
		}
		if ts != "" {
			return bySrc[ts+"\x00"+k]
		}
		return byAny[k]
	}

	// Tier 1: (normKey + normAuthor)
	if nk != "" && na != "" {
		if rec, ok := idx.matchWithAuthor(lookupBucket(idx.byKey, idx.byKeyAny, nk), na); ok {
			return rec, true
		}
	}

	// Tier 2: (normKey without author conflict)
	if nk != "" {
		if rec, ok := idx.matchWithoutAuthorConflict(lookupBucket(idx.byKey, idx.byKeyAny, nk), na); ok {
			return rec, true
		}
	}

	// Tier 3: (normRawKey + normAuthor)
	if (nrkSearch != "" || nrkExact != "") && na != "" {
		for _, k := range dedupeNonEmpty(nrkSearch, nrkExact) {
			if rec, ok := idx.matchWithAuthor(lookupBucket(idx.byRawKey, idx.byRawKeyAny, k), na); ok {
				return rec, true
			}
		}
	}

	// Tier 4: (normRawKey without author conflict)
	if nrkSearch != "" || nrkExact != "" {
		for _, k := range dedupeNonEmpty(nrkSearch, nrkExact) {
			if rec, ok := idx.matchWithoutAuthorConflict(lookupBucket(idx.byRawKey, idx.byRawKeyAny, k), na); ok {
				return rec, true
			}
		}
	}

	return MappingRecord{}, false
}

func (idx *MappingIndex) matchWithAuthor(positions []int, normAuthor string) (MappingRecord, bool) {
	if len(positions) == 0 || normAuthor == "" {
		return MappingRecord{}, false
	}
	var matched []MappingRecord
	for _, pos := range positions {
		rec := idx.records[pos]
		if cjk.NormalizeMatchKey(rec.Author) == normAuthor {
			matched = append(matched, rec)
		}
	}
	return pickUniqueTarget(matched)
}

func (idx *MappingIndex) matchWithoutAuthorConflict(positions []int, normAuthor string) (MappingRecord, bool) {
	if len(positions) == 0 {
		return MappingRecord{}, false
	}
	if normAuthor != "" {
		// Caller specified an author, and Tier 1 already checked exact author match.
		// Only authorless records do not conflict with caller's author.
		var authorless []MappingRecord
		for _, pos := range positions {
			rec := idx.records[pos]
			if cjk.NormalizeMatchKey(rec.Author) == "" {
				authorless = append(authorless, rec)
			}
		}
		return pickUniqueTarget(authorless)
	}

	// Caller has no author: prefer an explicit authorless record if unambiguous;
	// otherwise allow if all records point to the exact same target (no conflict).
	var authorless []MappingRecord
	all := make([]MappingRecord, 0, len(positions))
	for _, pos := range positions {
		rec := idx.records[pos]
		all = append(all, rec)
		if cjk.NormalizeMatchKey(rec.Author) == "" {
			authorless = append(authorless, rec)
		}
	}
	if len(authorless) > 0 {
		return pickUniqueTarget(authorless)
	}
	return pickUniqueTarget(all)
}

func pickUniqueTarget(candidates []MappingRecord) (MappingRecord, bool) {
	if len(candidates) == 0 {
		return MappingRecord{}, false
	}
	first := candidates[0]
	for _, c := range candidates[1:] {
		if c.TargetSource != first.TargetSource || c.TargetID != first.TargetID {
			return MappingRecord{}, false
		}
	}
	return first, true
}

// SanitizeAndExportMappings validates, filters, deduplicates, and formats MappingRecords
// as clean NDJSON bytes safe for public community sharing.
func SanitizeAndExportMappings(records []MappingRecord, opts ExportMappingsOptions) ([]byte, error) {
	var allowedOrigins map[string]struct{}
	if len(opts.AllowedOrigins) > 0 {
		allowedOrigins = make(map[string]struct{}, len(opts.AllowedOrigins))
		for _, o := range opts.AllowedOrigins {
			norm := strings.ToLower(strings.TrimSpace(o))
			if norm != "" {
				allowedOrigins[norm] = struct{}{}
			}
		}
	}

	seen := make(map[string]struct{}, len(records))
	var buf bytes.Buffer

	for _, rec := range records {
		if rec.IsNSFW && !opts.IncludeNSFW {
			continue
		}
		if len(allowedOrigins) > 0 {
			orig := strings.ToLower(strings.TrimSpace(rec.Origin))
			if _, ok := allowedOrigins[orig]; !ok {
				continue
			}
		}
		clean, ok := sanitizeMappingRecord(rec, false)
		if !ok {
			continue
		}
		dk := mappingDedupKey(clean)
		if _, dup := seen[dk]; dup {
			continue
		}
		seen[dk] = struct{}{}

		line, err := json.Marshal(clean)
		if err != nil {
			return nil, err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}

	return buf.Bytes(), nil
}

func sanitizeMappingRecord(rec MappingRecord, requireSchema bool) (MappingRecord, bool) {
	schema := strings.TrimSpace(rec.Schema)
	if requireSchema && schema != MappingSchemaV1 {
		return MappingRecord{}, false
	}
	if schema == "" {
		schema = MappingSchemaV1
	} else if schema != MappingSchemaV1 {
		return MappingRecord{}, false
	}

	targetSource := strings.TrimSpace(rec.TargetSource)
	if !isValidTargetSource(targetSource) {
		return MappingRecord{}, false
	}

	clean := MappingRecord{
		Schema:       schema,
		Key:          strings.TrimSpace(rec.Key),
		RawKey:       strings.TrimSpace(rec.RawKey),
		Author:       strings.TrimSpace(rec.Author),
		TargetSource: targetSource,
		TargetID:     strings.TrimSpace(rec.TargetID),
		IsNSFW:       rec.IsNSFW,
		Origin:       strings.TrimSpace(rec.Origin),
	}

	if clean.Key == "" || clean.TargetID == "" {
		return MappingRecord{}, false
	}
	if cjk.ExactTitleKey(clean.Key) == "" {
		return MappingRecord{}, false
	}

	for _, val := range []string{clean.Key, clean.RawKey, clean.Author, clean.TargetID, clean.Origin} {
		if hasControlChars(val) || containsSensitiveOrPrivateValue(val) {
			return MappingRecord{}, false
		}
	}

	return clean, true
}

func isValidTargetSource(src string) bool {
	switch src {
	case TargetSourceBangumi, TargetSourceGalleryV1, TargetSourceMetaV1:
		return true
	default:
		return false
	}
}

func mappingDedupKey(rec MappingRecord) string {
	return cjk.ExactTitleKey(rec.Key) + "\x00" +
		cjk.NormalizeMatchKey(rec.Author) + "\x00" +
		rec.TargetSource
}

func hasControlChars(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func containsSensitiveOrPrivateValue(s string) bool {
	if s == "" {
		return false
	}
	if urlSchemeRe.MatchString(s) || winDrivePathRe.MatchString(s) || unixPathRe.MatchString(s) || jwtLikeRe.MatchString(s) {
		return true
	}

	lower := strings.ToLower(s)
	for _, marker := range []string{
		"fk115_",
		"bearer ",
		"authorization:",
		"access_token",
		"refresh_token",
		"downurl",
		"password=",
		"passwd=",
		"secret=",
		"api_key=",
		"ghp_",
		"github_pat_",
		"uid=",
		"cid=",
		"seid=",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}

	if containsIPv4Literal(s) || containsIPv6Literal(s) {
		return true
	}
	return false
}

func containsIPv4Literal(s string) bool {
	// Scan contiguous [0-9.] spans and validate with netip.ParseAddr.
	n := len(s)
	for i := 0; i < n; {
		if s[i] < '0' || s[i] > '9' {
			i++
			continue
		}
		j := i
		dots := 0
		for j < n && ((s[j] >= '0' && s[j] <= '9') || s[j] == '.') {
			if s[j] == '.' {
				dots++
			}
			j++
		}
		if dots == 3 {
			token := s[i:j]
			if addr, err := netip.ParseAddr(token); err == nil && addr.Is4() {
				// Ensure boundary isn't immediately followed by another digit/dot/letter of a version string like v1.2.3.4
				prevOK := i == 0 || !isAlphaNum(s[i-1])
				nextOK := j == n || !isAlphaNum(s[j])
				if prevOK && nextOK {
					return true
				}
			}
		}
		i = j
	}
	return false
}

func containsIPv6Literal(s string) bool {
	// Check bracketed IPv6 or tokens containing "::" that parse as valid IPv6 addresses.
	for _, field := range strings.FieldsFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || r == ',' || r == ';' || r == '"' || r == '\'' || r == '[' || r == ']'
	}) {
		if !strings.Contains(field, "::") {
			continue
		}
		if addr, err := netip.ParseAddr(field); err == nil && addr.Is6() {
			return true
		}
	}
	return false
}

func isAlphaNum(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func dedupeNonEmpty(vals ...string) []string {
	var out []string
	for _, v := range vals {
		if v == "" {
			continue
		}
		dup := false
		for _, existing := range out {
			if existing == v {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, v)
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func fmtErrorWrap(prefix string, err error) error {
	return errors.New(prefix + ": " + err.Error())
}
