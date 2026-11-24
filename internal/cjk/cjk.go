// Package cjk normalizes CJK text so that Traditional Chinese, Simplified
// Chinese, HK/TW variants, Japanese Shinjitai, Kana spellings and fullwidth
// ASCII forms of the same name collapse onto a single comparable key.
//
// It is shared by the Bangumi scraper (title/author matching) and the SQLite
// catalog search index so both layers agree on character equivalence.
package cjk

import (
	"strings"
	"unicode"
)

// FTSSeparator is an obscure Unicode letter outside Latin/CJK/Greek/Cyrillic
// (Glagolitic Capital Letter Azu, U+2C00) placed between distinct multi-value
// entries (tags, authors, alternate titles) in FTS5 columns so unicode61 treats
// it as a position-occupying barrier token while user queries can never match it.
const FTSSeparator = "\u2C00"
const ftsSeparatorRune = '\u2C00'

// tradToSimpMap is the flattened, idempotent lookup built from tradRunes and
// simpRunes (generated in table_data.go) at init time.
var tradToSimpMap map[rune]rune

func init() {
	t := []rune(tradRunes)
	s := []rune(simpRunes)
	tradToSimpMap = make(map[rune]rune, len(t))
	for i := 0; i < len(t) && i < len(s); i++ {
		if t[i] != s[i] {
			tradToSimpMap[t[i]] = s[i]
		}
	}
}

// foldRune applies fullwidth ASCII and Traditional/Shinjitai/variant folding
// to a single rune without modifying kana or iteration marks.
func foldRune(r rune) rune {
	if r >= 0xFF01 && r <= 0xFF5E {
		r -= 0xFEE0
	}
	if mapped, ok := tradToSimpMap[r]; ok {
		return mapped
	}
	return r
}

// kanaFold normalizes Japanese kana spellings for search and matching keys:
//   - Drops prolonged sound / wave-dash marks (ー, 〜, ～)
//   - Converts Katakana (ァ–ヶ) into Hiragana (ぁ–ゖ) so katakana and hiragana
//     spellings match symmetrically
//   - Maps small kana to their full-size hiragana form and folds archaic/voiced
//     variants (ゐ→い, ゑ→え, を/ヲ→お, ゔ/ヴ→う)
//
// The mapping is idempotent: folding an already-folded rune returns it unchanged.
func kanaFold(r rune) (rune, bool) {
	switch r {
	case '\u30FC', '\u301C', '\uFF5E':
		return 0, true // drop ー / 〜 / ～
	}
	// Map Katakana block (ァ..ヶ) to Hiragana (ぁ..ゖ) first for full symmetry.
	converted := false
	if r >= '\u30A1' && r <= '\u30F6' {
		r -= 0x60
		converted = true
	}
	switch r {
	case '\u3041': // ぁ
		return '\u3042', true
	case '\u3043': // ぃ
		return '\u3044', true
	case '\u3045': // ぅ
		return '\u3046', true
	case '\u3047': // ぇ
		return '\u3048', true
	case '\u3049': // ぉ
		return '\u304A', true
	case '\u3063': // っ
		return '\u3064', true
	case '\u3083': // ゃ
		return '\u3084', true
	case '\u3085': // ゅ
		return '\u3086', true
	case '\u3087': // ょ
		return '\u3088', true
	case '\u308E': // ゎ
		return '\u308F', true
	case '\u3094': // ゔ (and ヴ after shift)
		return '\u3046', true
	case '\u3090': // ゐ
		return '\u3044', true
	case '\u3091': // ゑ
		return '\u3048', true
	case '\u3092': // を (and ヲ after shift)
		return '\u304A', true
	case '\u3095': // ゕ (and ヵ)
		return '\u304B', true
	case '\u3096': // ゖ (and ヶ)
		return '\u3051', true
	}
	return r, converted
}

// ToSimplified converts Traditional Chinese, HK/TW variants, and Japanese
// Shinjitai runes to Simplified Chinese and converts fullwidth ASCII/space to
// halfwidth. It preserves kana and 々 so it remains safe for display titles.
func ToSimplified(input string) string {
	if input == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(input))
	for _, r := range input {
		if r == '\u3000' {
			b.WriteByte(' ')
			continue
		}
		b.WriteRune(foldRune(r))
	}
	return b.String()
}

// foldMatchString applies ToSimplified, expands the Japanese iteration mark 々
// onto the preceding Han ideograph (so 日々 == 日日), and folds kana variants.
// ASCII letter case is preserved so callers (such as FTS CamelCase indexing)
// can inspect case boundaries before lowercasing.
func foldMatchString(input string) string {
	if input == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(input))
	var last rune
	for _, r := range input {
		if r == '\u3000' {
			b.WriteByte(' ')
			last = ' '
			continue
		}
		if r == '\u3005' { // 々
			if unicode.Is(unicode.Han, last) {
				b.WriteRune(last)
			}
			continue
		}
		if folded, handled := kanaFold(r); handled {
			if folded == 0 {
				continue
			}
			r = folded
		} else {
			r = foldRune(r)
		}
		b.WriteRune(r)
		last = r
	}
	return b.String()
}

// SimplifyRune normalizes a single rune for matching. Returns 0 when the rune
// is dropped (e.g. prolonged sound mark ー).
func SimplifyRune(r rune) rune {
	if folded, handled := kanaFold(r); handled {
		if folded == 0 {
			return 0
		}
		return folded
	}
	return foldRune(r)
}

// NormalizeMatchKey produces a compact, case-folded, punctuation-stripped key
// suitable for exact dictionary/SQLite matching across Traditional/Simplified/Japanese variants.
func NormalizeMatchKey(input string) string {
	folded := foldMatchString(strings.TrimSpace(input))
	if folded == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(folded))
	for _, r := range folded {
		r = unicode.ToLower(r)
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// SearchKey normalizes text for case-insensitive, variant-folded substring and
// equality matching while keeping spaces and non-quote punctuation.
//
// The folding is idempotent: SearchKey(SearchKey(x)) == SearchKey(x).
func SearchKey(input string) string {
	folded := foldMatchString(strings.TrimSpace(input))
	if folded == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(folded))
	pendingSpace := false
	for _, r := range folded {
		r = unicode.ToLower(r)
		switch {
		case unicode.IsSpace(r):
			pendingSpace = b.Len() > 0
			continue
		case r == '"' || r == '\'' || r == '‘' || r == '’' || r == '“' || r == '”':
			continue
		case r == '\x00':
			continue
		}
		if pendingSpace {
			b.WriteByte(' ')
			pendingSpace = false
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

func isCJKScript(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r)
}

type ftsToken struct {
	text  string
	isCJK bool
}

// tokenizeForFTS turns text into unicode61-safe tokens:
//   - Every Han/Kana/Hangul rune becomes its own single-rune token so FTS5 phrase
//     queries match any CJK substring of length >= 1.
//   - Contiguous non-CJK letter runs and digit runs are separated cleanly (so
//     "Vol01" and "Vol 01" produce identical tokens ["vol", "01"]).
//   - When collectCamelParts is true (index side), CamelCase letter words such
//     as "SecretManga" also yield split sub-parts ("secret", "manga").
func tokenizeForFTS(input string, collectCamelParts bool) ([]ftsToken, [][]string) {
	folded := foldMatchString(strings.TrimSpace(input))
	if folded == "" {
		return nil, nil
	}
	runes := []rune(folded)
	n := len(runes)
	tokens := make([]ftsToken, 0, n)
	var camelGroups [][]string

	for i := 0; i < n; {
		r := runes[i]
		if r == ftsSeparatorRune {
			i++
			continue
		}
		if isCJKScript(r) {
			tokens = append(tokens, ftsToken{text: string(r), isCJK: true})
			i++
			continue
		}
		if unicode.IsDigit(r) {
			start := i
			for i < n && unicode.IsDigit(runes[i]) {
				i++
			}
			tokens = append(tokens, ftsToken{text: string(runes[start:i]), isCJK: false})
			continue
		}
		if unicode.IsLetter(r) {
			start := i
			for i < n && unicode.IsLetter(runes[i]) && !isCJKScript(runes[i]) && runes[i] != ftsSeparatorRune {
				i++
			}
			wordRunes := runes[start:i]
			lower := strings.ToLower(string(wordRunes))
			tokens = append(tokens, ftsToken{text: lower, isCJK: false})
			if collectCamelParts && len(wordRunes) >= 2 {
				if parts := splitCamelWord(wordRunes); len(parts) > 1 {
					camelGroups = append(camelGroups, parts)
				}
			}
			continue
		}
		i++
	}
	return tokens, camelGroups
}

func splitCamelWord(runes []rune) []string {
	var parts []string
	start := 0
	for i := 1; i < len(runes); i++ {
		prev := runes[i-1]
		curr := runes[i]
		if unicode.IsLower(prev) && unicode.IsUpper(curr) {
			parts = append(parts, strings.ToLower(string(runes[start:i])))
			start = i
		}
	}
	if start == 0 {
		return nil
	}
	parts = append(parts, strings.ToLower(string(runes[start:])))
	return parts
}

// FTSIndexText formats a single text value for insertion into an FTS5 column
// using the unicode61 tokenizer.
func FTSIndexText(input string) string {
	tokens, camelGroups := tokenizeForFTS(input, true)
	if len(tokens) == 0 {
		return ""
	}
	var b strings.Builder
	for i, tok := range tokens {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(tok.text)
	}
	for _, grp := range camelGroups {
		b.WriteByte(' ')
		b.WriteString(FTSSeparator)
		for _, p := range grp {
			b.WriteByte(' ')
			b.WriteString(p)
		}
	}
	return b.String()
}

// FTSJoinValues formats a slice of distinct metadata values (such as tags,
// genres, authors, or alternate titles) into one FTS5 column separated by
// FTSSeparator so phrase matches never cross entry boundaries.
func FTSJoinValues(values []string) string {
	if len(values) == 0 {
		return ""
	}
	var b strings.Builder
	written := 0
	for _, v := range values {
		txt := FTSIndexText(v)
		if txt == "" {
			continue
		}
		if written > 0 {
			b.WriteByte(' ')
			b.WriteString(FTSSeparator)
			b.WriteByte(' ')
		}
		b.WriteString(txt)
		written++
	}
	return b.String()
}

// FTSQueryPhrase converts a user search term into a safe SQLite FTS5 MATCH
// phrase expression (e.g. `"灌" + "篮"` or `"water" + "plan"*`). Returns ""
// when the input contains no indexable letters or digits (e.g. punctuation only).
func FTSQueryPhrase(input string) string {
	tokens, _ := tokenizeForFTS(input, false)
	if len(tokens) == 0 {
		return ""
	}
	var b strings.Builder
	for i, tok := range tokens {
		if i > 0 {
			b.WriteString(" + ")
		}
		b.WriteByte('"')
		b.WriteString(tok.text)
		b.WriteByte('"')
		// Prefix match on the trailing non-CJK token preserves LIKE-like prefix behavior
		// for Latin words and numbers (e.g. "Chron" -> "Chronicle", "Vol01" -> "Vol010").
		if i == len(tokens)-1 && !tok.isCJK {
			b.WriteByte('*')
		}
	}
	return b.String()
}

var crossTitleReplacer = strings.NewReplacer("×", "x", "✕", "x", "╳", "x")

// CrossTitleKey folds the common cross separators ×✕╳ onto a literal x before
// normalizing, so "CLOUD×COMPASS" and "CLOUDxCOMPASS" produce the same key.
func CrossTitleKey(title string) string {
	if !strings.ContainsAny(title, "×✕╳") {
		return ""
	}
	return NormalizeMatchKey(crossTitleReplacer.Replace(title))
}

// ExactTitleKey is NormalizeMatchKey with cross-separator folding applied.
func ExactTitleKey(title string) string {
	if key := CrossTitleKey(title); key != "" {
		return key
	}
	return NormalizeMatchKey(title)
}
