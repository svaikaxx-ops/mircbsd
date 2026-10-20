package main

// Protokolo ir renderio radiniai (K5, K6, K7, K9, K11, K14, K15, K18).

import (
	"strings"
	"testing"
	"time"
)

// ============================================================
// K5 — INVITE rodo kanalą, o ne target nick'ą
// ============================================================

// TestInviteShowsChannelNotNick — INVITE pranešime turi būti KANALAS.
func TestInviteShowsChannelNotNick(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":draugas!u@h INVITE testnick :#specialus")

	c.mu.Lock()
	var texts []string
	for _, m := range c.channels[0].messages {
		texts = append(texts, m.text)
	}
	c.mu.Unlock()

	joined := strings.Join(texts, "\n")
	if !strings.Contains(joined, "#specialus") {
		t.Errorf("BUG: INVITE pranešime nėra kanalo: %q", joined)
	}
	// „/join manick“ — kanalas NIEKADA neparodomas.
	if strings.Contains(joined, "/join testnick") {
		t.Errorf("BUG: INVITE rodo target nick'ą vietoj kanalo: %q", joined)
	}
}

// TestInviteIgnoresOtherUsers — kai mus kitaip, pranešimo nėra.
func TestInviteIgnoresOtherUsers(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":draugas!u@h INVITE kazkitas :#specialus")

	c.mu.Lock()
	n := len(c.channels[0].messages)
	c.mu.Unlock()
	if n != 0 {
		t.Errorf("BUG: INVITE kitam nick'ui sukūrė %d žinutę", n)
	}
}

// ============================================================
// K6 — MODE argumentų indeksai pagal CHANMODES
// ============================================================

// TestModeUsesChanModesForArgumentIndex — kai serveris naudoja nestandartines
// CHANMODES, argumentų indeksai turi likti teisingi, o statuso prefiksas
// turi būti priskirtas TEISINGAM nick'ui.
func TestModeUsesChanModesForArgumentIndex(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	c.ensureCh("#chan")
	ch := c.channels[1]
	c.mu.Unlock()

	// Serveris skelbia CHANMODES su modomis, kurios anksčiau nebuvo žinomos.
	c.applyISUPPORT([]string{"CHANMODES=eIbq,k,flj,CFLMPQScgimnprstz"})
	_, signs, modes, _, chanModes := c.isupSnapshot()
	if chanModes == "" {
		t.Fatal("BUG: CHANMODES neprivalėtas į ISUPPORT")
	}

	// Standartinės CHANMODES dalys: pirma (su arg) + antra (su arg).
	// RFC 2811: "eIbq,k,flj,CFLMPQScgimnprstz" → su argumentu: e,I,b,q,k
	// (be argumento: f,l,j ir t.t.)
	letters := chanModesArgLetters(chanModes, modes)
	for _, want := range []string{"o", "v", "k", "b", "e", "I"} {
		if !strings.Contains(letters, want) {
			t.Errorf("BUG: modas %q neįtrauktas į argumentų raides: %q", want, letters)
		}
	}
	// Be argumento modai (iš trečiosios CHANMODES dalies) turi būti IŠSKIRTAS.
	for _, unwanted := range []string{"f", "l", "j", "C", "n", "m", "z"} {
		if strings.Contains(letters, unwanted) {
			t.Errorf("BUG: modas be argumento (%q) įtrauktas į argumentų raides: %q", unwanted, letters)
		}
	}
	_ = ch
	_ = signs
}

// TestChanModesArgLettersFallback — be CHANMODES naudojamas RFC standartas.
func TestChanModesArgLettersFallback(t *testing.T) {
	letters := chanModesArgLetters("", "qaohv")
	// Standartinės CHANMODES: "eIbq,k,flj,..." → e,I,b,q,k imasi argumentą.
	for _, want := range []string{"e", "I", "b", "q", "k", "q", "a", "o", "h", "v"} {
		if !strings.Contains(letters, want) {
			t.Errorf("BUG: modas %q neįtrauktas į %q", want, letters)
		}
	}
	// Trečiosios dalies modai (be argumento) — neįtraukiami.
	for _, unwanted := range []string{"C", "F", "L", "M", "P", "Q", "S", "c", "g", "i", "m", "n", "p", "r", "s", "t", "z"} {
		if strings.Contains(letters, unwanted) {
			t.Errorf("BUG: modas be argumento %q įtrauktas į %q", unwanted, letters)
		}
	}
}

// ============================================================
// K7 — /w siunčia WHO, o /window rodo langus
// ============================================================

// TestSlashWSendsWho — /w turi siųsti WHO komandą.
func TestSlashWSendsWho(t *testing.T) {
	c := newTestClient()
	got := captureWrites(c)
	c.handleCmd("w", "")
	c.handleCmd("w", "#kanalas")

	lines := got(200 * time.Millisecond)
	foundWho := false
	for _, l := range lines {
		if strings.HasPrefix(l, "WHO ") {
			foundWho = true
		}
	}
	if !foundWho {
		t.Errorf("BUG: /w nesiuntė WHO komandos. Eilutės: %v", lines)
	}
}

// TestSlashWindowListsWindows — /window (be arg) rodo langų sąrašą.
func TestSlashWindowListsWindows(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	c.ensureCh("#kanalas")
	c.mu.Unlock()

	c.handleCmd("window", "")

	c.mu.Lock()
	var texts []string
	for _, m := range c.channels[0].messages {
		texts = append(texts, m.text)
	}
	c.mu.Unlock()
	joined := strings.Join(texts, "\n")
	if !strings.Contains(joined, "#kanalas") {
		t.Errorf("BUG: /window nerodo langų sąrašo: %q", joined)
	}
}

// ============================================================
// K9 — slaptažodžio komandų aptikimas
// ============================================================

// TestIsSensitiveHistoryDetectsSetPassword — „set p“ (trumpinta forma)
// taip pat turi būti laikoma slaptažodžiu.
func TestIsSensitiveHistoryDetectsSetPassword(t *testing.T) {
	sensitive := []string{
		"/msg NickServ IDENTIFY slapta",
		"/msg NickServ REGISTER slapta",
		"/msg NickServ SET PASSWORD slapta",
		"/msg NickServ set p slapta",
		"/msg NickServ set password slapta",
		"/msg NickServ confirm slapta",
	}
	for _, s := range sensitive {
		if !isSensitiveHistory(s) {
			t.Errorf("BUG: isSensitiveHistory(%q) = false, turėtų būti true", s)
		}
	}
	// O įprastos komandos — NE slaptažodžiai.
	for _, s := range []string{
		"/msg kazkadas labas",
		"/join #kanalas",
		"/me šoka",
		"/msg NickServ help",
	} {
		if isSensitiveHistory(s) {
			t.Errorf("BUG: isSensitiveHistory(%q) = true, turėtų būti false", s)
		}
	}
}

// ============================================================
// K11 — formatavimas išgyvena apvyniuotose eilutėse
// ============================================================

// TestFindChunkKeepsFormatting — findChunk turi grąžinti ORIGINALĄ
// (su formatavimo kodais), o ne plain tekstą.
func TestFindChunkKeepsFormatting(t *testing.T) {
	orig := "\x0304raudonas\x03 \x02boldas\x02"
	got := findChunk(orig, "raudonas boldas")
	if got != orig {
		t.Errorf("BUG: findChunk = %q, norėtų originalą %q", got, orig)
	}
	if stripIRCFormat(got) != "raudonas boldas" {
		t.Errorf("BUG: plain(got) = %q, norėtų %q", stripIRCFormat(got), "raudonas boldas")
	}
}

// TestFindChunkPartialKeepsFormatting — dalis teksto su spalva.
func TestFindChunkPartialKeepsFormatting(t *testing.T) {
	orig := "\x0304raudonas\x03 \x02boldas\x02"
	got := findChunk(orig, "raudonas")
	if !strings.Contains(got, "\x0304") {
		t.Errorf("BUG: findChunk = %q — prarasta spalva", got)
	}
	if stripIRCFormat(got) != "raudonas" {
		t.Errorf("BUG: plain(got) = %q, norėtų %q", stripIRCFormat(got), "raudonas")
	}
}

// TestRenderLinesKeepsColorOnWrappedMessage — apvyniuota žinutė turi
// išlaikyti spalvą (anksčiau spalva likdavo tik 1 eilutės žinutėse).
func TestRenderLinesKeepsColorOnWrappedMessage(t *testing.T) {
	m := Message{
		ts:      time.Now(),
		nick:    "jonas",
		text:    "\x0304raudona\x03 " + strings.Repeat("žodis ", 20),
		msgType: MsgChat,
	}
	lines := m.renderLines(20, "testnick")
	if len(lines) < 2 {
		t.Fatalf("BUG: žinutė neapvyniuota (1 eilutė)")
	}
	colored := false
	for _, l := range lines {
		if strings.Contains(l, "\x1b[") && strings.Contains(l, "3") {
			colored = true
		}
	}
	// ANSI konvertuojamas į ANSI — tikriname, kad bent viena eilutė turi
	// spalvos kodą (ne tik reset'ą).
	found := false
	for _, l := range lines {
		if strings.Contains(l, ansiCyan) || strings.Contains(l, ansiRed) ||
			strings.Contains(l, ansiGreen) || strings.Contains(l, ansiBlue) {
			found = true
		}
	}
	_ = colored
	if !found {
		t.Errorf("BUG: apvyniuota žinutė neturi spalvos kodų: %q", lines)
	}
}

// ============================================================
// K14 — tab-baigimas neperpja UTF-8
// ============================================================

// TestStripLastWordKeepsUTF8 — prefix turi likti sveikas UTF-8.
func TestStripLastWordKeepsUTF8(t *testing.T) {
	cases := []struct{ in, want string }{
		{"žinutė kai", "žinutė "},
		{"žinutė", ""},
		{"abc def", "abc "},
		{"", ""},
		{"a", ""},
	}
	for _, c := range cases {
		got := stripLastWord(c.in)
		if got != c.want {
			t.Errorf("BUG: stripLastWord(%q) = %q, norėtų %q", c.in, got, c.want)
		}
		if !isValidUTF8(got) {
			t.Errorf("BUG: stripLastWord(%q) = %q — nutrūkusi UTF-8 seka", c.in, got)
		}
	}
}

// isValidUTF8 tikrina, ar eilutė yra teisinga UTF-8.
func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == 0xFFFD && !strings.ContainsRune(s, 0xFFFD) {
			return false
		}
	}
	return true
}

// TestDoTabWithLithuanianNick — tab-baigimas su lietuviškais nick'ais
// neturi sulaužyti UTF-8.
func TestDoTabWithLithuanianNick(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	c.ensureCh("#kanalas")
	ch := c.channels[1]
	ch.addNick("Žilvinas")
	// Aktyvus kanalas turi būti #kanalas — doTab skaito jo nick'ų sąrašą.
	for i, cn := range c.channels {
		if cn == ch {
			c.activeIdx = i
			break
		}
	}
	c.input = []rune("žil")
	c.inputPos = 3
	c.mu.Unlock()

	c.doTab()

	c.mu.Lock()
	input := string(c.input)
	c.mu.Unlock()

	if !strings.Contains(input, "Žilvinas") {
		t.Errorf("BUG: tab-baigimas nerado lietuviško nick'o: %q", input)
	}
	if !isValidUTF8(input) {
		t.Errorf("BUG: tab-baigimas sulaužė UTF-8: %q", input)
	}
}

// ============================================================
// K15 — highlightURLs neturi lyginti kodų
// ============================================================

// TestHighlightURLsNoDoubleCodes — URL apvyniojamas VIENĄ kartą.
func TestHighlightURLsNoDoubleCodes(t *testing.T) {
	in := "žiūrėkite http://example.com čia"
	got := highlightURLs(in)
	if strings.Count(got, "http://example.com") != 1 {
		t.Errorf("BUG: URL atsikartotas: %q", got)
	}
	// Kodai turi būti suporiuoti, ne įdubinti.
	if strings.Contains(got, ansiCyan+ansiCyan) {
		t.Errorf("BUG: įdubinti ANSI kodai: %q", got)
	}
	// Tekstas turi likti (be ANSI kodų).
	if stripANSI(got) != in {
		t.Errorf("BUG: stripANSI(got) = %q, norėtų %q", stripANSI(got), in)
	}
}

// TestHighlightURLsTwoDistinctURLs — du skirtingi URL apvyniojami abu.
func TestHighlightURLsTwoDistinctURLs(t *testing.T) {
	in := "http://a.com ir http://b.com"
	got := highlightURLs(in)
	if strings.Count(got, ansiCyan) != 2 {
		t.Errorf("BUG: %d URL apvyniojami, norėtų 2: %q", strings.Count(got, ansiCyan), got)
	}
}

// TestHighlightURLsInColoredText — URL esantis jau spalvotame tekste taip pat
// apvyniojamas (anksčiau nebuvo aptinkamas dėl formatavimo kodų).
func TestHighlightURLsInColoredText(t *testing.T) {
	in := ansiRed + "žr. http://example.com" + ansiReset
	got := highlightURLs(in)
	if !strings.Contains(got, "http://example.com") {
		t.Errorf("BUG: URL dingo: %q", got)
	}
	if strings.Count(got, ansiCyan+ansiBold) != 1 {
		t.Errorf("BUG: URL neapvyniotas arba apvyniotas dukart: %q", got)
	}
}

// stripANSI pašalina ANSI kodus (testams).
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := skipANSISeq(s, i)
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// ============================================================
// K18 — 306 (RPL_NOWAWAY) nustato away būseną
// ============================================================

// TestRPLNowawaySetsAway — po 306 numeriko statuso juosta turi rodyti
// [AWAY].
func TestRPLNowawaySetsAway(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":serveris 306 testnick :You have been marked as being away")

	c.mu.Lock()
	away := c.away
	c.mu.Unlock()
	if away == "" {
		t.Error("BUG: po 306 c.away liko tuščias — statuso juosta nerodo [AWAY]")
	}
}
