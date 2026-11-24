package cjk

import "testing"

func TestCJKTableAlignment(t *testing.T) {
	if len([]rune(tradRunes)) != len([]rune(simpRunes)) {
		t.Fatalf("tradRunes (%d) != simpRunes (%d)", len([]rune(tradRunes)), len([]rune(simpRunes)))
	}
}

func TestToSimplifiedFoldsCJKVariants(t *testing.T) {
	tests := map[string]string{
		"灌籃高手":   "灌篮高手",
		"灌篮高手":   "灌篮高手",
		"髙":      "高",
		"\uFA11": "崎",
		"嵜":      "崎",
		"日々":     "日日",
		"髙々":     "高高",
		"ＡＢＣ":    "ABC",
	}
	for input, want := range tests {
		if got := ToSimplified(input); got != want {
			t.Errorf("ToSimplified(%q)=%q, want %q", input, got, want)
		}
	}
}

func TestSearchKeyIsIdempotent(t *testing.T) {
	for _, input := range []string{"灌籃高手", "日々のカルテ", "CLOUD×COMPASS", "  ＡＢＣ  "} {
		once := SearchKey(input)
		if again := SearchKey(once); again != once {
			t.Errorf("SearchKey not idempotent for %q: %q -> %q", input, once, again)
		}
	}
}

func TestNormalizeMatchKeyTreatsIterationMarkAsRepeat(t *testing.T) {
	if a, b := NormalizeMatchKey("日々"), NormalizeMatchKey("日日"); a != b {
		t.Fatalf("iteration mark not folded: %q != %q", a, b)
	}
	if a, b := NormalizeMatchKey("灌籃高手"), NormalizeMatchKey("灌篮高手"); a != b {
		t.Fatalf("traditional/simplified keys differ: %q != %q", a, b)
	}
}
