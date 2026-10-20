package main

// Ekrano pločio (ląstelių) matavimo testai — K10 radinys.
//
// Pamoka iš ankstesnės peržiūros: „be teisingo atvejo priėmimo testo
// validatorius gali tyliai atmesti viską". Čia tikrinama ir/bloga (tikslus
// plotis) ir/teisinga (lietuviškos raidės, CJK, emoji) pusės.

import (
	"fmt"
	"strings"
	"testing"
	"time"
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

// ============================================================
// Nick'ų įstatymas į surūšiuotą sąrašą (našumo optimizacija)
// ============================================================

// TestAddNickKeepsSortOrder — addNick įstatydamas nick'ą į savo vietą
// turi duoti TOKIĄ patį žaidimą kaip pilnas sortNicks. Anksčiau addNick
// kiekvieną kartą perrankydavo visą sąrašą (O(n log n) įterpimui).
func TestAddNickKeepsSortOrder(t *testing.T) {
	signs := "@+"
	base := []string{
		"@alice", "bob", "+carol", "@dave", "erin", "@frank", "+gina", "hank",
		"@ivan", "jill", "@kate", "leo", "@mike", "nina", "op", "@pam", "quinn",
	}
	// Įvairios įterpimo tvarkos — per 20 kartų sumaišoma.
	for iter := 0; iter < 20; iter++ {
		names := append([]string(nil), base...)
		// paprastas pseudoatsitiktinis sumaišymas (deterministinis)
		for i := len(names) - 1; i > 0; i-- {
			j := (i*7 + iter*13) % (i + 1)
			names[i], names[j] = names[j], names[i]
		}

		ch := &Channel{name: "#t", casemapping: "ascii"}
		for _, n := range names {
			ch.addNick(n)
		}

		ref := append([]string(nil), names...)
		sortNicks(ref, signs)

		if len(ch.nicks) != len(ref) {
			t.Fatalf("iter %d: %d nick'ų, o turėtų būti %d", iter, len(ch.nicks), len(ref))
		}
		for i := range ref {
			if ch.nicks[i] != ref[i] {
				t.Fatalf("iter %d, poz %d: addNick=%q, sortNicks=%q", iter, i, ch.nicks[i], ref[i])
			}
		}
	}
}

// ============================================================
// Inkrementalus renderio cache'as
// ============================================================

// TestIncrementalRenderCacheMatchesFullRender — cache'as turi duoti
// IDENTIŠKĄ vaizdą kaip pilnas renderiavimas, o pridedant naują žinutę
// senos eilutės turi LIKTI nepakitusios (tik naujos pridedamos).
func TestIncrementalRenderCacheMatchesFullRender(t *testing.T) {
	c := newTestClient()
	c.width = 100
	c.height = 30
	c.mu.Lock()
	c.ensureCh("#bench")
	ch := c.channels[1]
	c.activeIdx = 1
	for i := 0; i < 50; i++ {
		ch.addMsg(Message{ts: time.Now(), nick: "k", text: fmt.Sprintf("pranesimas %d", i), msgType: MsgChat})
	}
	c.mu.Unlock()

	silenceStdout(t, c.draw)
	c.mu.Lock()
	cached := append([]string(nil), ch.rendered...)
	cachedCount := ch.renderedMsgCount
	c.mu.Unlock()

	if cachedCount != 50 {
		t.Fatalf("cache: %d žinučių, norėtų 50", cachedCount)
	}

	// Pridedame dar 10 — cache turi IŠPLĖSTIS, o ne nukirti senas
	for i := 50; i < 60; i++ {
		ch.addMsg(Message{ts: time.Now(), nick: "k", text: fmt.Sprintf("pranesimas %d", i), msgType: MsgChat})
	}
	silenceStdout(t, c.draw)
	c.mu.Lock()
	cached2 := append([]string(nil), ch.rendered...)
	cachedCount2 := ch.renderedMsgCount
	c.mu.Unlock()

	if cachedCount2 != 60 {
		t.Fatalf("cache po pratęsimo: %d žinučių, norėtų 60", cachedCount2)
	}
	if len(cached2) <= len(cached) {
		t.Errorf("cache neišaugo: %d → %d", len(cached), len(cached2))
	}
	for i := range cached {
		if cached2[i] != cached[i] {
			t.Errorf("BUG: eilutė %d pasikeitė: %q → %q", i, cached[i], cached2[i])
		}
	}

	// Pilnas renderiavimas turi duoti TOKIĄ patį
	msgW := c.width - chanListWidth - 2 - (nickListWidth + 1)
	full := []string{}
	for _, m := range ch.messages {
		full = append(full, m.renderLines(msgW, c.nick)...)
	}
	if strings.Join(full, "\n") != strings.Join(cached2, "\n") {
		t.Errorf("BUG: cache ≠ pilnas renderiavimas (%d vs %d eilutės)", len(cached2), len(full))
	}
}

// TestClearResetsRenderCache — /clear ištrina žinutes, todėl cache'as
// turi būti IŠTRINTAS (kitaip inkrementalus cache'as rodytų senas
// žinutes).
func TestClearResetsRenderCache(t *testing.T) {
	c := newTestClient()
	c.width = 100
	c.height = 30
	c.mu.Lock()
	c.ensureCh("#bench")
	ch := c.channels[1]
	c.activeIdx = 1
	for i := 0; i < 20; i++ {
		ch.addMsg(Message{ts: time.Now(), nick: "k", text: fmt.Sprintf("pranesimas %d", i), msgType: MsgChat})
	}
	c.mu.Unlock()

	silenceStdout(t, c.draw)
	c.mu.Lock()
	before := len(ch.rendered)
	c.mu.Unlock()
	if before == 0 {
		t.Fatal("BUG: cache tuščias po pirmo braižimo")
	}

	c.handleCmd("clear", "")

	c.mu.Lock()
	after := len(ch.rendered)
	count := ch.renderedMsgCount
	msgs := len(ch.messages)
	c.mu.Unlock()

	if after != 0 {
		t.Errorf("BUG: po /clear cache turi %d eilučių, turėtų būti 0", after)
	}
	if count != 0 {
		t.Errorf("BUG: po /clear renderedMsgCount = %d, turėtų būti 0", count)
	}
	if msgs != 0 {
		t.Errorf("BUG: po /clear liko %d žinučių", msgs)
	}
}
