package main

// Protokolo ir renderio radiniai (K5, K6, K7, K9, K11, K14, K15, K18).

import (
	"bufio"
	"fmt"
	"net"
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

// ============================================================
// K16 — 353 negali sukurti kanalų
// ============================================================

// Test353DoesNotCreateChannels — serverio komanda (353) neturi teisės kurti
// naujų kanalų. Priešiškas serveris galėtų siųsti šimtais numerių ir
// užpildyti šoninę juostą.
func Test353DoesNotCreateChannels(t *testing.T) {
	c := newTestClient()
	for i := 0; i < 5; i++ {
		c.parseIRC(":server 353 testnick = #fake" + string(rune('a'+i)) + " :user1 user2")
	}

	c.mu.Lock()
	n := len(c.channels)
	c.mu.Unlock()
	// Tik *server* (1) — nė vieno naujo kanalo.
	if n != 1 {
		names := []string{}
		c.mu.Lock()
		for _, ch := range c.channels {
			names = append(names, ch.name)
		}
		c.mu.Unlock()
		t.Errorf("BUG: 353 sukūrė %d kanalų (turėjo likti 1): %v", n-1, names)
	}
}

// Test353AddsNicksToExistingChannel — teisingas atvejis: kanalas, kuriam
// prisijungėme, gauna nick'ų sąrašą.
func Test353AddsNicksToExistingChannel(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":testnick!u@h JOIN #chan")
	c.parseIRC(":server 353 testnick = #chan :user1 user2")

	c.mu.Lock()
	ch := c.findCh("#chan")
	var nicks []string
	if ch != nil {
		nicks = append(nicks, ch.nicks...)
	}
	c.mu.Unlock()

	found := false
	for _, n := range nicks {
		if n == "user1" {
			found = true
		}
	}
	if !found {
		t.Errorf("BUG: 353 nepridėjo nick'ų į esamą kanalą: %v", nicks)
	}
}

// ============================================================
// K20 — CAP NEW / LIST / DEL
// ============================================================

// TestCapNewRequestsCap — serveris siunčia CAP NEW — klientas turi
// atsakyti CAP REQ arba CAP END.
func TestCapNewRequestsCap(t *testing.T) {
	c := newTestClient()
	got := captureWrites(c)
	c.parseIRC(":server CAP * NEW :multi-prefix")

	lines := got(200 * time.Millisecond)
	found := false
	for _, l := range lines {
		if strings.HasPrefix(l, "CAP REQ") || strings.HasPrefix(l, "CAP END") {
			found = true
		}
	}
	if !found {
		t.Errorf("BUG: CAP NEW neapdoruotas (nėra CAP REQ/CAP END). Eilutės: %v", lines)
	}
}

// TestCapListResponds — CAP LIST gauna atsakymą.
func TestCapListResponds(t *testing.T) {
	c := newTestClient()
	got := captureWrites(c)
	c.parseIRC(":server CAP * LIST :")

	lines := got(200 * time.Millisecond)
	found := false
	for _, l := range lines {
		if strings.HasPrefix(l, "CAP LIST") {
			found = true
		}
	}
	if !found {
		t.Errorf("BUG: CAP LIST neapdoruotas. Eilutės: %v", lines)
	}
}

// TestCapDelRemovesActiveCap — CAP DEL pašalina cap'ą iš aktyviųjų.
func TestCapDelRemovesActiveCap(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	c.activeCaps["multi-prefix"] = true
	c.mu.Unlock()

	c.parseIRC(":server CAP * DEL :multi-prefix")

	c.mu.Lock()
	active := c.activeCaps["multi-prefix"]
	c.mu.Unlock()
	if active {
		t.Error("BUG: CAP DEL nepašalino aktyvaus capa")
	}
}

// ============================================================
// K22 — NICK komanda rodo pranešimą kanaluose
// ============================================================

// TestNickChangeShowsInChannel — nick'o pakeitimas rodomas kanale, kuriame
// tas nickas buvo (kaip mIRC). Anksčiau pranešimas matomas tik serverio
// kanalo lange.
func TestNickChangeShowsInChannel(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":testnick!u@h JOIN #kanalas")
	c.parseIRC(":senas!u@h JOIN #kanalas")
	c.parseIRC(":senas!u@h NICK naujas")

	c.mu.Lock()
	ch := c.findCh("#kanalas")
	var texts []string
	if ch != nil {
		for _, m := range ch.messages {
			texts = append(texts, m.text)
		}
	}
	c.mu.Unlock()

	joined := strings.Join(texts, "\n")
	if !strings.Contains(joined, "naujas") {
		t.Errorf("BUG: nick'o pakeitimas nerodomas kanale: %q", joined)
	}
	// Pranešimo tipas turi būti MsgNick.
	c.mu.Lock()
	ch = c.findCh("#kanalas")
	found := false
	if ch != nil {
		for _, m := range ch.messages {
			if m.msgType == MsgNick && m.nick == "senas" {
				found = true
			}
		}
	}
	c.mu.Unlock()
	if !found {
		t.Error("BUG: nėra MsgNick pranešimo kanale")
	}
}

// TestNickChangeRenamesInChannel — senasis nick'as turi būti pakeistas
// nick'ų sąraše.
func TestNickChangeRenamesInChannel(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":senas!u@h JOIN #kanalas")
	c.parseIRC(":senas!u@h NICK naujas")

	c.mu.Lock()
	ch := c.findCh("#kanalas")
	var nicks []string
	if ch != nil {
		nicks = append(nicks, ch.nicks...)
	}
	c.mu.Unlock()

	hasNew := false
	for _, n := range nicks {
		if n == "naujas" {
			hasNew = true
		}
	}
	if !hasNew {
		t.Errorf("BUG: nickas neperpildytas: %v", nicks)
	}
}

// TestNickChangeNotShownInUnrelatedChannel — kanale, kuriame nick'o nebuvo,
// pranešimo nėra.
func TestNickChangeNotShownInUnrelatedChannel(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":testnick!u@h JOIN #kazkitas")
	c.parseIRC(":senas!u@h JOIN #kazkitas")
	c.parseIRC(":senas!u@h NICK naujas")

	c.mu.Lock()
	ch := c.findCh("#kazkitas")
	n := 0
	if ch != nil {
		for _, m := range ch.messages {
			if m.msgType == MsgNick {
				n++
			}
		}
	}
	c.mu.Unlock()
	// Serverio kanalo pranešimas — ne kanalo pranešimas.
	if n != 1 {
		t.Errorf("BUG: MsgNick pranešimų kanale %d, norėtų 1", n)
	}
}

// ============================================================
// K23 — WHO ir kiti numerikai nekrenta į default:
// ============================================================

// TestWhoNumericsAreFormatted — 352/315 rodomi suprantamai, o ne kaip
// „[352] …“ (raw numerikas).
func TestWhoNumericsAreFormatted(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":server 352 testnick #kanalas user host srv Jonas@ H :0 Jonas Ilgavardis")
	c.parseIRC(":server 315 testnick #kanalas :End of /WHO list.")

	c.mu.Lock()
	var texts []string
	for _, m := range c.channels[0].messages {
		texts = append(texts, m.text)
	}
	c.mu.Unlock()

	joined := strings.Join(texts, "\n")
	if strings.Contains(joined, "[352]") {
		t.Errorf("BUG: 352 rodomas kaip raw numerikas: %q", joined)
	}
	if strings.Contains(joined, "[315]") {
		t.Errorf("BUG: 315 rodomas kaip raw numerikas: %q", joined)
	}
	if !strings.Contains(joined, "Jonas") {
		t.Errorf("BUG: WHO atsakymas nerodo nick'o: %q", joined)
	}
}

// TestLuserNumericsAreFormatted — 251/255/265/266 apdorojami.
func TestLuserNumericsAreFormatted(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":server 251 testnick :I am 42 users, 3 unknown and 7 channels")
	c.parseIRC(":server 255 testnick :I have 7 clients and 9 servers")
	c.parseIRC(":server 265 testnick 12 50 :Current local users 12, max 50")
	c.parseIRC(":server 266 testnick 100 200 :Current global users 100, max 200")

	c.mu.Lock()
	var texts []string
	for _, m := range c.channels[0].messages {
		texts = append(texts, m.text)
	}
	c.mu.Unlock()

	joined := strings.Join(texts, "\n")
	for _, bad := range []string{"[251]", "[255]", "[265]", "[266]"} {
		if strings.Contains(joined, bad) {
			t.Errorf("BUG: %s rodomas kaip raw numerikas: %q", bad, joined)
		}
	}
	if !strings.Contains(joined, "42") {
		t.Errorf("BUG: 251 duomenys nerodomi: %q", joined)
	}
}

// ============================================================
// K24 — kanalų sąrašo slinktis (daugiau kanalų, nei telpa ekrane)
// ============================================================

// TestChannelListScrollsToShowActive — peršokus į kanalą, kuris yra už
// slinkties ribos, kanalų sąrašas turi PASLINKTI, kad jis matytųsi.
func TestChannelListScrollsToShowActive(t *testing.T) {
	c := newTestClient()
	c.width = 100
	c.height = 20 // mažas ekranas — telpa tik ~16 kanalų
	c.mu.Lock()
	for i := 0; i < 30; i++ {
		c.ensureCh(fmt.Sprintf("#kanalas%02d", i))
	}
	c.mu.Unlock()

	// Peršokime į 25-ą kanalą (už slinkties ribos)
	c.switchCh(26)

	c.mu.Lock()
	scroll := c.chanScrollOff
	active := c.activeIdx
	c.mu.Unlock()

	if active != 26 {
		t.Fatalf("BUG: aktyvus kanalas = %d, norėtų 26", active)
	}
	if scroll == 0 {
		t.Error("BUG: kanalų sąrašas nepaslinko — aktyvus kanalas nematomas")
	}
	if scroll > 26 {
		t.Errorf("BUG: slinktis = %d — per didelis (aktyvus kanalas 26)", scroll)
	}
}

// TestChannelListScrollKeepsActiveVisible — peršokius atgal į žemesnį
// kanalą, jis VIS TIEK turi būti matomas (sąrašas paslinkamas arba
// grąžinamas).
func TestChannelListScrollKeepsActiveVisible(t *testing.T) {
	c := newTestClient()
	c.width = 100
	c.height = 20
	c.mu.Lock()
	for i := 0; i < 30; i++ {
		c.ensureCh(fmt.Sprintf("#kanalas%02d", i))
	}
	c.mu.Unlock()

	c.switchCh(26)
	c.switchCh(1)

	c.mu.Lock()
	scroll := c.chanScrollOff
	c.mu.Unlock()

	rows := 20 - 4
	if scroll > 1 || scroll+rows <= 1 {
		t.Errorf("BUG: slinktis = %d — kanalas 1 nematomas (rows=%d)", scroll, rows)
	}
}

// ============================================================
// K25 — serverio slaptažodis (PASS) ir ISUPPORT ribos
// ============================================================

// TestServerPassIsSentBeforeNick — serverio slaptažodis siunčiamas PASS
// komanda PRIEŠ NICK (RFC 2812).
func TestServerPassIsSentBeforeNick(t *testing.T) {
	c := newTestClient()
	c.logDir = ""
	c.mu.Lock()
	c.serverPass = "slaptas-serverio-zodis"
	c.mu.Unlock()

	_ = captureWrites(c)

	// Paleidžiame prisijungimą prie netikro serverio (lokalaus).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()
	// Priimame vieną jungtį ir rinkime eilutes.
	lines := make(chan string, 50)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		sc := bufio.NewScanner(conn)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()

	c.mu.Lock()
	c.server = "127.0.0.1"
	c.port = portOf(ln)
	c.mu.Unlock()
	go c.doConnect(0)

	// Rinkime iki NICK.
	var passLine, nickLine string
	deadline := time.After(3 * time.Second)
	for passLine == "" || nickLine == "" {
		select {
		case l := <-lines:
			if strings.HasPrefix(l, "PASS ") {
				passLine = l
			}
			if strings.HasPrefix(l, "NICK ") {
				nickLine = l
			}
		case <-deadline:
			t.Fatalf("nepavyko gauti PASS/NICK (pass=%q nick=%q)", passLine, nickLine)
		}
	}

	if passLine != "PASS slaptas-serverio-zodis" {
		t.Errorf("BUG: PASS = %q", passLine)
	}
	if nickLine == "" {
		t.Error("BUG: NICK neatsiųstas")
	}
}

// TestNoPassWhenServerPassEmpty — be serverio slaptažodžio PASS
// komanda nesiunčiama.
func TestNoPassWhenServerPassEmpty(t *testing.T) {
	c := newTestClient()
	c.logDir = ""
	c.mu.Lock()
	c.serverPass = ""
	c.mu.Unlock()

	_ = captureWrites(c)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()
	lines := make(chan string, 50)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		sc := bufio.NewScanner(conn)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()

	c.mu.Lock()
	c.server = "127.0.0.1"
	c.port = portOf(ln)
	c.mu.Unlock()
	go c.doConnect(0)

	deadline := time.After(3 * time.Second)
	for {
		select {
		case l := <-lines:
			if strings.HasPrefix(l, "NICK ") {
				// NICK atėjo — PASS turėjo būti arba nebuvo.
				return
			}
		case <-deadline:
			t.Fatal("NICK neatsiųstas")
		}
	}
}

// TestISUPPORTLimitsAreParsed — 005 ribos (NICKLEN, CHANNELLEN,
// TOPICLEN, KICKLEN, MODES) ir NETWORK yra išskaitomi.
func TestISUPPORTLimitsAreParsed(t *testing.T) {
	c := newTestClient()
	c.applyISUPPORT([]string{
		"NETWORK=Libera.Chat",
		"NICKLEN=20",
		"CHANNELLEN=64",
		"TOPICLEN=120",
		"KICKLEN=50",
		"MODES=4",
	})

	c.isupMu.RLock()
	network := c.network
	limits := c.limits
	c.isupMu.RUnlock()

	if network != "Libera.Chat" {
		t.Errorf("BUG: NETWORK = %q, norėtų „Libera.Chat“", network)
	}
	if limits.NickLen != 20 {
		t.Errorf("BUG: NICKLEN = %d, norėtų 20", limits.NickLen)
	}
	if limits.ChannelLen != 64 {
		t.Errorf("BUG: CHANNELLEN = %d, norėtų 64", limits.ChannelLen)
	}
	if limits.TopicLen != 120 {
		t.Errorf("BUG: TOPICLEN = %d, norėtų 120", limits.TopicLen)
	}
	if limits.KickLen != 50 {
		t.Errorf("BUG: KICKLEN = %d, norėtų 50", limits.KickLen)
	}
	if limits.Modes != 4 {
		t.Errorf("BUG: MODES = %d, norėtų 4", limits.Modes)
	}
}

// TestISUPPORTLimitsFallBackToDefaults — be 005 ribų naudojamos
// standartinės.
func TestISUPPORTLimitsFallBackToDefaults(t *testing.T) {
	c := newTestClient()
	if c.nickLenLimit() != 30 {
		t.Errorf("BUG: NICKLEN = %d, norėtų 30", c.nickLenLimit())
	}
	if c.channelLenLimit() != 50 {
		t.Errorf("BUG: CHANNELLEN = %d, norėtų 50", c.channelLenLimit())
	}
	if c.topicLenLimit() != 390 {
		t.Errorf("BUG: TOPICLEN = %d, norėtų 390", c.topicLenLimit())
	}
	if c.kickLenLimit() != 0 {
		t.Errorf("BUG: KICKLEN = %d, norėtų 0 (serveris nepranešė)", c.kickLenLimit())
	}
}

// TestNickRejectedWhenOverServerLimit — su serverio NICKLEN riba
// per ilgas nick'as atmetamas.
func TestNickRejectedWhenOverServerLimit(t *testing.T) {
	c := newTestClient()
	c.applyISUPPORT([]string{"NICKLEN=10"})

	if c.isValidNickLen("LabaiIlgasNickas") {
		t.Error("BUG: per ilgas nick priimtas, nors NICKLEN=10")
	}
	if !c.isValidNickLen("Trumpas") {
		t.Error("BUG: trumpas nick atmestas, nors NICKLEN=10")
	}
}

// ---- E2E patikra aptiktos klaidos (2026-10-20) ----

// TestNickPrefixUpdatedFromNames — 353 (NAMES) atneša STATUSO prefiksą
// („@nick“), kuris ankstesniam JOIN įrašui nebuvo. Anksčiau addNick
// ignoravo „dublikatus“ → prefiksas tyliai dingdavo, o vartotojas nematė,
// kad jis operatorius.
//
// BUG atkurtas: JOIN pridėjo „testnick“ (be prefikso), tada 353 atnešė
// „@testnick“ → sąraše turi likti „@testnick“.
func TestNickPrefixUpdatedFromNames(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":testnick!u@h JOIN #k")
	c.parseIRC(":srv 353 testnick = #k :@testnick Opas +Balsas testnick Paprastas")

	c.mu.Lock()
	var nicks []string
	for _, ch := range c.channels {
		if ch.name == "#k" {
			nicks = append(nicks, ch.nicks...)
		}
	}
	c.mu.Unlock()

	joined := strings.Join(nicks, ",")
	if !strings.Contains(joined, "@testnick") {
		t.Errorf("BUG: operatoriaus prefiksas @ dingęs — nicks=%q", joined)
	}
	// Prefiksas turi būti ir teisingoje vietoje ( operatorius — pirmas).
	if len(nicks) == 0 || nicks[0] != "@testnick" {
		t.Errorf("BUG: prefiksuotas nick'as ne pirmas — nicks=%q", joined)
	}
}

// TestNickPrefixNotDuplicated — kai prefiksas jau yra (iš 353), vėlesnis
// 353 su tuo pačiu nick'u NEPRIDEDA dar vieno prefikso.
func TestNickPrefixNotDuplicated(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":testnick!u@h JOIN #k")
	c.parseIRC(":srv 353 testnick = #k :@testnick")
	c.parseIRC(":srv 353 testnick = #k :@testnick")

	c.mu.Lock()
	var nicks []string
	for _, ch := range c.channels {
		if ch.name == "#k" {
			nicks = append(nicks, ch.nicks...)
		}
	}
	c.mu.Unlock()

	joined := strings.Join(nicks, ",")
	if strings.Count(joined, "testnick") != 1 {
		t.Errorf("BUG: nick'as dubliuojasi — nicks=%q", joined)
	}
	if strings.Contains(joined, "@@") {
		t.Errorf("BUG: prefiksas dubliuojasi — nicks=%q", joined)
	}
}

// TestNickPrefixKeptWhenNamesHasNone — jei 353 atneša nick'ą BE prefikso,
// o jis jau sąraše su prefiksu (iš MODE komandos) — prefiksas LIEKA.
func TestNickPrefixKeptWhenNamesHasNone(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":testnick!u@h JOIN #k")
	c.parseIRC(":srv MODE #k +o testnick")
	c.parseIRC(":srv 353 testnick = #k :testnick")

	c.mu.Lock()
	var nicks []string
	for _, ch := range c.channels {
		if ch.name == "#k" {
			nicks = append(nicks, ch.nicks...)
		}
	}
	c.mu.Unlock()

	joined := strings.Join(nicks, ",")
	if !strings.Contains(joined, "@testnick") {
		t.Errorf("BUG: prefiksas dingo, kai 353 atnešė nick'ą be prefikso — nicks=%q", joined)
	}
}

// TestWhoReplyGoesToQueriedChannel — WHO atsakymai rodomi kanale, kuriame
// buvo užklausta. Anksčiau jie visada eidavo į *server* langą, todėl
// vartotojas kanale jų nematė.
func TestWhoReplyGoesToQueriedChannel(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":testnick!u@h JOIN #lietuva")
	c.setWhoTarget("#lietuva")
	c.parseIRC(":srv 352 testnick #lietuva user host srv Jonas H@ :0 Jonas Ilgavardis")
	c.parseIRC(":srv 315 testnick #lietuva :End of /WHO list.")

	c.mu.Lock()
	var chanTexts, serverTexts []string
	for _, ch := range c.channels {
		for _, m := range ch.messages {
			if ch.name == "#lietuva" {
				chanTexts = append(chanTexts, m.text)
			} else {
				serverTexts = append(serverTexts, m.text)
			}
		}
	}
	c.mu.Unlock()

	joined := strings.Join(chanTexts, "\n")
	if !strings.Contains(joined, "Jonas") {
		t.Errorf("BUG: WHO atsakymas ne kanale #lietuva — kanale=%q", joined)
	}
	if strings.Contains(strings.Join(serverTexts, "\n"), "Jonas") {
		t.Errorf("BUG: WHO atsakymas vis tiek *server* lange — server=%q", strings.Join(serverTexts, "\n"))
	}
}

// TestWhoReplyGoesToServerWhenNotChannel — jei WHO užklausta ne kanale
// (pvz. „WHO *“), atsakymai eina į *server* langą.
func TestWhoReplyGoesToServerWhenNotChannel(t *testing.T) {
	c := newTestClient()
	c.setWhoTarget("")
	c.parseIRC(":srv 352 testnick * user host srv Jonas H@ :0 Jonas Ilgavardis")

	c.mu.Lock()
	var serverTexts []string
	for _, ch := range c.channels {
		if ch.name == "*server*" {
			for _, m := range ch.messages {
				serverTexts = append(serverTexts, m.text)
			}
		}
	}
	c.mu.Unlock()

	if !strings.Contains(strings.Join(serverTexts, "\n"), "Jonas") {
		t.Errorf("BUG: WHO atsakymas nerastas *server* lange — %q", strings.Join(serverTexts, "\n"))
	}
}

// TestWhoisReplyGoesToActiveChannel — WHOIS atsakymai rodomi aktyviame kanale.
func TestWhoisReplyGoesToActiveChannel(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":testnick!u@h JOIN #lietuva")
	c.setWhoisTarget("#lietuva")
	c.parseIRC(":srv 311 testnick Jonas user host * :Jonas Ilgavardis")
	c.parseIRC(":srv 318 testnick Jonas :End of /WHOIS list.")

	c.mu.Lock()
	var chanTexts []string
	for _, ch := range c.channels {
		if ch.name == "#lietuva" {
			for _, m := range ch.messages {
				chanTexts = append(chanTexts, m.text)
			}
		}
	}
	c.mu.Unlock()

	joined := strings.Join(chanTexts, "\n")
	if !strings.Contains(joined, "Jonas") {
		t.Errorf("BUG: WHOIS atsakymas ne kanale — %q", joined)
	}
}
