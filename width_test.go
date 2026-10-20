package main

// Ekrano pločio (ląstelių) matavimo testai — K10 radinys.
//
// Pamoka iš ankstesnės peržiūros: „be teisingo atvejo priėmimo testo
// validatorius gali tyliai atmesti viską". Čia tikrinama ir/bloga (tikslus
// plotis) ir/teisinga (lietuviškos raidės, CJK, emoji) pusės.

import (
	"strings"
	"testing"
)

// TestWidthRangesSorted — intervalai turi būti surušiuoti, nes paieška
// juose dvejetainė. Nesurūšiuoti intervalai tyliai neveikdavo (emoji odų
// tonai, ZWJ, katakana).
func TestWidthRangesSorted(t *testing.T) {
	check := func(name string, ranges [][2]rune) {
		for i := 1; i < len(ranges); i++ {
			if ranges[i][0] <= ranges[i-1][1] {
				t.Errorf("%s: intervalai nesurušiuoti arba persidengia: %04X-%04X po %04X-%04X",
					name, ranges[i][0], ranges[i][1], ranges[i-1][0], ranges[i-1][1])
			}
		}
	}
	check("wideRanges", wideRanges)
	check("zeroWidthRanges", zeroWidthRanges)
}

// TestRuneWidthBasic — pagrindiniai pločiai.
func TestRuneWidthBasic(t *testing.T) {
	cases := []struct {
		r    rune
		want int
	}{
		{'a', 1},
		{'Z', 1},
		{'5', 1},
		{' ', 1},
		{'į', 1},     // lietuviška
		{'č', 1},     // lietuviška
		{'ų', 1},     // lietuviška
		{'Ž', 1},     // lietuviška
		{'→', 1},     // rodo strėliukas
		{'─', 1},     // brūkšnio linija
		{'★', 1},     // žvaigždutė
		{'日', 2},     // CJK ideograma
		{'あ', 2},     // hiragana
		{'한', 2},     // hangul
		{'😀', 2},     // emoji
		{'́', 0},     // derinamysis diakritinis ženklas
		{'‍', 0},     // ZWJ
		{'​', 0},     // zero-width space
		{0x1F3FD, 0}, // emoji odų tonas
	}
	for _, c := range cases {
		if got := runeWidth(c.r); got != c.want {
			t.Errorf("runeWidth(%U) = %d, norėtų %d", c.r, got, c.want)
		}
	}
}

// TestStringWidth — teksto plotis ląstelėmis.
func TestStringWidth(t *testing.T) {
	cases := []struct {
		s    string
		want int
	}{
		{"", 0},
		{"abc", 3},
		{"žinutė", 6},
		{"日本語", 6},   // 3 ideogramas po 2 ląsteles
		{"a日本語b", 8}, // 1 + 3*2 + 1
		{"👨‍👩‍👧", 2}, // ZWJ seka = 2 ląstelės
		{"🇦🇹", 2},    // vėliava = 2 ląstelės
		{"👍🏽", 2},    // emoji + odų tonas = 2 ląstelės
		{"é", 1},    // e + derinamasis = 1 ląstelė
	}
	for _, c := range cases {
		if got := stringWidth(c.s); got != c.want {
			t.Errorf("stringWidth(%q) = %d, norėtų %d", c.s, got, c.want)
		}
	}
}

// TestStringWidthMatchesClusters — stringWidth ir clustersWidth turi
// sutapti (kitaip apvyniojimas ir statuso juosta rodytų skirtingus plotis).
func TestStringWidthMatchesClusters(t *testing.T) {
	cases := []string{
		"", "abc", "žinutė", "日本語", "👨‍👩‍👧", "🇦🇹", "👍🏽", "é", "🎉", "a👨‍👩‍👧b",
	}
	for _, s := range cases {
		if a, b := stringWidth(s), clustersWidth(runeClusters(s)); a != b {
			t.Errorf("MISMATCH %q: stringWidth=%d clustersWidth=%d", s, a, b)
		}
	}
}

// TestWrapTextCellWidth — apvyniojimas turi laikytis ląstelių, ne rune'ų.
func TestWrapTextCellWidth(t *testing.T) {
	lines := wrapText("日本語のテキストです", 10)
	for _, l := range lines {
		if w := stringWidth(l); w > 10 {
			t.Errorf("wrapText ląstelės: %q plotis %d > 10", l, w)
		}
	}
	// Tekstas turi būti visas (niekas neprarastas).
	joined := strings.Join(lines, "")
	if strings.ReplaceAll(joined, " ", "") != "日本語のテキストです" {
		t.Errorf("wrapText prarado tekstą: %q", joined)
	}
}

// TestWrapTextDoesNotExceedWidth — lietuviškas tekstas (1 ląstelė/raide).
func TestWrapTextDoesNotExceedWidth(t *testing.T) {
	lines := wrapText("žinutė lietuviškai čia", 10)
	for _, l := range lines {
		if w := stringWidth(l); w > 10 {
			t.Errorf("wrapText: %q plotis %d > 10", l, w)
		}
	}
}

// TestWrapTextKeepsText — tekstas neturi dingti apvyniojant.
func TestWrapTextKeepsText(t *testing.T) {
	for _, text := range []string{
		"hello world this is a test",
		"žinutė lietuviškai čia",
		"日本語のテキストです",
		"supercalifragilisticexpialidocious",
	} {
		lines := wrapText(text, 10)
		joined := strings.Join(lines, "")
		if strings.ReplaceAll(joined, " ", "") != strings.ReplaceAll(text, " ", "") {
			t.Errorf("wrapText(%q) -> %q — tekstas pasikeitė", text, joined)
		}
	}
}

// TestWrapTextEmpty — tuščias tekstas.
func TestWrapTextEmpty(t *testing.T) {
	lines := wrapText("", 10)
	if len(lines) != 1 || lines[0] != "" {
		t.Errorf("wrapText(\"\") = %q, norėtų [\"\"]", lines)
	}
}

// TestTruncateWidth — nukirpimas pagal ląsteles, su ellipsės ženklu.
func TestTruncateWidth(t *testing.T) {
	if got := truncateWidth("abc", 10); got != "abc" {
		t.Errorf("truncateWidth(\"abc\", 10) = %q, norėtų \"abc\"", got)
	}
	got := truncateWidth("abcdefghij", 5)
	if stringWidth(got) > 5 {
		t.Errorf("truncateWidth = %q, plotis %d > 5", got, stringWidth(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncateWidth = %q, turi baigtis ellipsės ženklu", got)
	}
	// CJK: 3 ideogramas (6 ląstelės) į 5 ląsteles → 2 ideogramas + ellipsė.
	cjk := truncateWidth("日本語", 5)
	if stringWidth(cjk) > 5 {
		t.Errorf("truncateWidth(CJK) = %q, plotis %d > 5", cjk, stringWidth(cjk))
	}
}

// TestFitWidth — pildymas iki tikslaus pločio.
func TestFitWidth(t *testing.T) {
	if got := fitWidth("ab", 5); got != "ab   " {
		t.Errorf("fitWidth(\"ab\", 5) = %q, norėtų \"ab   \"", got)
	}
	if got := fitWidth("žinutė", 10); stringWidth(got) != 10 {
		t.Errorf("fitWidth plotis = %d, norėtų 10", stringWidth(got))
	}
	// Per ilgas tekstas — apkerpamas ir užpildomas.
	got := fitWidth("abcdefghijklmnop", 8)
	if stringWidth(got) != 8 {
		t.Errorf("fitWidth ilgas = %q, plotis %d, norėtų 8", got, stringWidth(got))
	}
}

// TestPadRight — pildymas tarpais.
func TestPadRight(t *testing.T) {
	if got := padRight("ab", 5); got != "ab   " {
		t.Errorf("padRight = %q, norėtų \"ab   \"", got)
	}
	if got := padRight("abc", 2); got != "abc" {
		t.Errorf("padRight (per ilgas) = %q, norėtų \"abc\"", got)
	}
}

// TestRuneClustersNoSplit — grafiniai klasteriai neturi būti skaidomi.
func TestRuneClustersNoSplit(t *testing.T) {
	// e + derinamasis ų — vienas klasteris.
	cl := runeClusters("é")
	if len(cl) != 1 {
		t.Errorf("runeClusters(\"e\\u0301\") = %q, norėtų 1 klasteris", cl)
	}
	// ZWJ emoji seka — vienas klasteris.
	cl = runeClusters("👨‍👩‍👧")
	if len(cl) != 1 {
		t.Errorf("runeClusters(ZWJ) = %q, norėtų 1 klasteris", cl)
	}
	// Trys paprastos raidės — trys klasteriai.
	cl = runeClusters("abc")
	if len(cl) != 3 {
		t.Errorf("runeClusters(\"abc\") = %q, norėtų 3", cl)
	}
}
