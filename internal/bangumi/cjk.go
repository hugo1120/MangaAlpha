package bangumi

import (
	"github.com/xJogger/fake-komga-115/internal/cjk"
)

// The Traditional/Simplified/Shinjitai folding tables and the matching-key
// builders live in internal/cjk so the catalog search path and the scraper
// agree on what counts as the same character. These thin wrappers keep the
// scraper-local vocabulary (used across scorer.go/parser.go) stable.

// ToSimplified converts Traditional Chinese and common Japanese Shinjitai runes
// to Simplified Chinese, and converts fullwidth ASCII characters to halfwidth.
func ToSimplified(input string) string { return cjk.ToSimplified(input) }

// NormalizeMatchKey produces a compact, case-folded, punctuation-stripped key
// suitable for exact dictionary/SQLite matching across Traditional/Simplified/Japanese variants.
func NormalizeMatchKey(input string) string { return cjk.NormalizeMatchKey(input) }

// exactTitleKey keeps a title's cross separator equivalent to a literal x.
// The older punctuation-stripped key is also retained for ordinary searches.
func exactTitleKey(title string) string { return cjk.ExactTitleKey(title) }

func crossTitleKey(title string) string { return cjk.CrossTitleKey(title) }
