package cjk

import "testing"

func TestCJKTableAlignmentAndIdempotency(t *testing.T) {
	tr := []rune(tradRunes)
	sr := []rune(simpRunes)
	if len(tr) != len(sr) {
		t.Fatalf("tradRunes (%d) != simpRunes (%d)", len(tr), len(sr))
	}
	if len(tr) < 4000 {
		t.Fatalf("expected full OpenCC-derived table (>=4000), got %d", len(tr))
	}
	for k, v := range tradToSimpMap {
		if k == v {
			t.Fatalf("identity entry %q in tradToSimpMap", string(k))
		}
		if next, ok := tradToSimpMap[v]; ok {
			t.Fatalf("non-idempotent chain %q -> %q -> %q", string(k), string(v), string(next))
		}
	}
}

func TestToSimplifiedDisplaySafe(t *testing.T) {
	tests := map[string]string{
		"灌籃高手":     "灌篮高手",
		"灌篮高手":     "灌篮高手",
		"髙":        "高",
		"\uFA11":   "崎",
		"嵜":        "崎",
		"桜":        "樱",
		"剣":        "剑",
		"竜":        "龙",
		"黒":        "黑",
		"横浜":       "横滨",
		"ＡＢＣ":      "ABC",
		"日々":       "日々",       // display title must keep 々 intact
		"葬送のフリーレン": "葬送のフリーレン", // display title must keep ー and katakana intact
		"萌研瓶沪":     "萌研瓶沪",     // common GB2312 chars must not be corrupted by JP table
	}
	for input, want := range tests {
		if got := ToSimplified(input); got != want {
			t.Errorf("ToSimplified(%q)=%q, want %q", input, got, want)
		}
	}
}

func TestSearchKeyIdempotentAndSymmetricKana(t *testing.T) {
	for _, input := range []string{
		"灌籃高手", "日々のカルテ", "CLOUD×COMPASS", "  ＡＢＣ  ",
		"葬送のフリーレン", "ファイト", "髙々", "をヲ",
	} {
		once := SearchKey(input)
		if again := SearchKey(once); again != once {
			t.Errorf("SearchKey not idempotent for %q: %q -> %q", input, once, again)
		}
	}

	equiv := [][2]string{
		{"日々", "日日"},
		{"髙々", "高高"},
		{"葬送のフリーレン", "葬送のふりーれん"},
		{"葬送のフリーレン", "葬送のフリレン"},
		{"ファイト", "フアイト"},
		{"ファイト", "ふぁいと"},
		{"を", "ヲ"},
		{"灌籃高手", "灌篮高手"},
		{"東立出版社", "东立出版社"},
		{"鬼滅の刃", "鬼灭の刃"},
		{"進撃の巨人", "进击の巨人"},
	}
	for _, pair := range equiv {
		a, b := SearchKey(pair[0]), SearchKey(pair[1])
		if a != b {
			t.Errorf("SearchKey(%q)=%q != SearchKey(%q)=%q", pair[0], a, pair[1], b)
		}
		na, nb := NormalizeMatchKey(pair[0]), NormalizeMatchKey(pair[1])
		if na != nb {
			t.Errorf("NormalizeMatchKey(%q)=%q != NormalizeMatchKey(%q)=%q", pair[0], na, pair[1], nb)
		}
	}
}

func TestFTSTokenizationAndQuery(t *testing.T) {
	if got := FTSIndexText("灌籃高手"); got != "灌 篮 高 手" {
		t.Fatalf("FTSIndexText(灌籃高手)=%q", got)
	}
	if got := FTSQueryPhrase("灌篮"); got != `"灌" + "篮"` {
		t.Fatalf("FTSQueryPhrase(灌篮)=%q", got)
	}
	if got := FTSIndexText("SecretManga Vol01"); got != "secretmanga vol 01 "+FTSSeparator+" secret manga" {
		t.Fatalf("FTSIndexText(SecretManga Vol01)=%q", got)
	}
	if got := FTSQueryPhrase("Vol 01"); got != `"vol" + "01"*` {
		t.Fatalf("FTSQueryPhrase(Vol 01)=%q", got)
	}
	if got := FTSJoinValues([]string{"校园", "恋爱"}); got != "校 园 "+FTSSeparator+" 恋 爱" {
		t.Fatalf("FTSJoinValues=%q", got)
	}
	if got := FTSQueryPhrase(`""ー`); got != "" {
		t.Fatalf("FTSQueryPhrase punctuation-only=%q, want empty", got)
	}
	if got := FTSQueryPhrase(FTSSeparator); got != "" {
		t.Fatalf("FTSQueryPhrase(FTSSeparator)=%q, want empty", got)
	}
}
