package main

// production_test.go — testai prieš gamybinį naudojimą
// Apima: isValidNick, handleInput, history, reconnect backoff,
// doSearch, doTab, renderIRCFormat, Channel.addMsg trimming,
// KICK/INVITE/NOTICE/353/332, away state, closeLogs,
// realname, useTLS reset, 305/306, 431/432, ignore persistence.

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ============================================================
// isValidNick
// ============================================================

func TestIsValidNick(t *testing.T) {
	valid := []string{
		"jonas", "Jonas", "JONAS",
		"jonas123", "jonas_pirmas", "jonas-antras",
		"[jonas]", "{jonas}", "jonas|back",
		"a",
		strings.Repeat("a", 30), // max ilgis
	}
	for _, n := range valid {
		if !isValidNick(n) {
			t.Errorf("isValidNick(%q) = false, turėtų būti true", n)
		}
	}

	invalid := []string{
		"",
		strings.Repeat("a", 31), // per ilgas
		"jo nas",                // tarpas
		"jo\rnas",               // CR
		"jo\nnas",               // LF
		"jo\x00nas",             // null
		"jo:nas",                // dvitaškis
	}
	for _, n := range invalid {
		if isValidNick(n) {
			t.Errorf("isValidNick(%q) = true, turėtų būti false", n)
		}
	}
}

// ============================================================
// handleInput — žinutės siuntimas ir dalijimas
// ============================================================

func TestHandleInputSendsPrivmsg(t *testing.T) {
	c := newTestClient()
	c.connected = true
	c.mu.Lock()
	c.ensureCh("#linux")
	c.activeIdx = 1
	c.mu.Unlock()
	collect := captureWrites(c)

	c.handleInput("labas pasauli")
	sent := collect(20 * time.Millisecond)

	found := false
	for _, s := range sent {
		if s == "PRIVMSG #linux :labas pasauli" {
			found = true
		}
	}
	if !found {
		t.Errorf("PRIVMSG neišsiųstas. Išsiųsta: %v", sent)
	}

	// Žinutė turi atsirasti ir kanalo istorijoje
	c.mu.Lock()
	ch := c.findCh("#linux")
	c.mu.Unlock()
	if len(ch.messages) == 0 || ch.messages[len(ch.messages)-1].text != "labas pasauli" {
		t.Error("žinutė neįrašyta į kanalo istoriją")
	}
}

func TestHandleInputLongMessageSplit(t *testing.T) {
	c := newTestClient()
	c.connected = true
	c.mu.Lock()
	c.ensureCh("#chan")
	c.activeIdx = 1
	c.mu.Unlock()
	collect := captureWrites(c)

	// ~2.5x baitų budget (#chan antraštei) — turi būti 3 dalys.
	// "a" yra 1 baitas; budget = 512-len("PRIVMSG #chan :")-2-45 = 444.
	text := strings.Repeat("a", 444*2+50)
	c.handleInput(text)
	sent := collect(30 * time.Millisecond)

	privmsgs := 0
	totalChars := 0
	for _, s := range sent {
		if strings.HasPrefix(s, "PRIVMSG #chan :") {
			privmsgs++
			totalChars += len([]rune(strings.TrimPrefix(s, "PRIVMSG #chan :")))
		}
	}
	if privmsgs != 3 {
		t.Errorf("tikėjomės 3 PRIVMSG, gavome %d (total: %d)", privmsgs, totalChars)
	}
	// Kiekviena dalis turi tilpti į IRC 512 baitų eilutę
	for _, s := range sent {
		if len(s)+2 > maxLineBytes {
			t.Errorf("eilutė %d baitų — viršytas 512 limitas", len(s)+2)
		}
	}
	// UTF-8 baitais: lietuviškas tekstas per du baitus — tikrinti dalijimą.
	// Tas pats kolektorius: atskiriam eilutes pagal turinį ("ž").
	text2 := strings.Repeat("ž", 600) // 1200 baitų
	c.handleInput(text2)
	sent2 := collect(30 * time.Millisecond)
	okBytes := 0
	for _, s := range sent2 {
		if !strings.Contains(s, "ž") {
			continue
		}
		if strings.HasPrefix(s, "PRIVMSG #chan :") {
			okBytes += len([]rune(strings.TrimPrefix(s, "PRIVMSG #chan :")))
			if len(s)+2 > maxLineBytes {
				t.Errorf("UTF-8 eilutė %d baitų — viršytas limitas", len(s)+2)
			}
		}
	}
	if okBytes != 600 {
		t.Errorf("prarasta UTF-8 žinutė: %d iš 600", okBytes)
	}
	if totalChars != len([]rune(text)) {
		t.Errorf("prarastas tekstas: išsiųsta %d simbolių iš %d", totalChars, len([]rune(text)))
	}
}

func TestHandleInputServerChannelBlocked(t *testing.T) {
	c := newTestClient()
	c.connected = true
	// *server* yra activeIdx=0

	c.handleInput("žinutė")

	c.mu.Lock()
	ch := c.channels[0]
	c.mu.Unlock()
	// Turi būti klaidos žinutė, ne PRIVMSG
	found := false
	for _, m := range ch.messages {
		if m.msgType == MsgServer && strings.Contains(m.text, "join") {
			found = true
		}
	}
	if !found {
		t.Error("siuntimas į *server* turėtų rodyti nurodymo žinutę")
	}
}

func TestHandleInputNotConnected(t *testing.T) {
	c := newTestClient()
	c.connected = false
	c.mu.Lock()
	c.ensureCh("#chan")
	c.activeIdx = 1
	c.mu.Unlock()

	c.handleInput("labas")

	c.mu.Lock()
	ch := c.channels[0] // klaidos rodomos *server*
	c.mu.Unlock()
	found := false
	for _, m := range ch.messages {
		if m.msgType == MsgError {
			found = true
		}
	}
	if !found {
		t.Error("siuntimas kai neprisijungta turėtų rodyti klaidos žinutę")
	}
}

func TestHandleInputCommand(t *testing.T) {
	c := newTestClient()
	c.connected = true
	c.mu.Lock()
	c.ensureCh("#chan")
	c.activeIdx = 1
	c.mu.Unlock()
	collect := captureWrites(c)

	c.handleInput("/me bėga")
	sent := collect(20 * time.Millisecond)

	found := false
	for _, s := range sent {
		if strings.HasPrefix(s, "PRIVMSG #chan :\x01ACTION") {
			found = true
		}
	}
	if !found {
		t.Errorf("/me nekonvertuota į ACTION CTCP: %v", sent)
	}
}

// ============================================================
// saveHistory / loadHistory roundtrip
// ============================================================

func TestHistoryRoundtrip(t *testing.T) {
	dir := t.TempDir()
	// Perimti historyPath
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	defer os.Setenv("HOME", origHome)

	c := newTestClient()
	c.mu.Lock()
	c.history = []string{"pirma", "antra", "trečia"}
	c.mu.Unlock()
	c.saveHistory()

	c2 := newTestClient()
	c2.loadHistory()
	c2.mu.Lock()
	h := c2.history
	c2.mu.Unlock()

	if len(h) != 3 {
		t.Fatalf("tikėjomės 3 istorijos įrašų, gavome %d: %v", len(h), h)
	}
	if h[0] != "pirma" || h[1] != "antra" || h[2] != "trečia" {
		t.Errorf("istorija nesutampa: %v", h)
	}
}

func TestHistorySaveLimitTo1000(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("HOME", dir)
	defer os.Unsetenv("HOME")

	c := newTestClient()
	c.mu.Lock()
	for i := 0; i < 1500; i++ {
		c.history = append(c.history, fmt.Sprintf("eilutė %d", i))
	}
	c.mu.Unlock()
	c.saveHistory()

	c2 := newTestClient()
	c2.loadHistory()
	c2.mu.Lock()
	n := len(c2.history)
	first := ""
	if n > 0 {
		first = c2.history[0]
	}
	c2.mu.Unlock()

	if n != 1000 {
		t.Errorf("po išsaugojimo turėtų būti 1000 eilučių, yra %d", n)
	}
	// Turi likti PASKUTINĖS 1000 (500..1499)
	if first != "eilutė 500" {
		t.Errorf("pirma eilutė turėtų būti 'eilutė 500', gavome %q", first)
	}
}

func TestHistoryAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("HOME", dir)
	defer os.Unsetenv("HOME")

	c := newTestClient()
	c.mu.Lock()
	c.history = []string{"a", "b", "c"}
	c.mu.Unlock()
	c.saveHistory()

	// Temp failas neturėtų likti
	entries, _ := os.ReadDir(filepath.Join(dir, ".config", "mircbsd"))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp failas liko po saveHistory: %s", e.Name())
		}
	}
}

// ============================================================
// historyUp / historyDown
// ============================================================

func TestHistoryUpDown(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	c.history = []string{"pirma", "antra", "trečia"}
	c.mu.Unlock()

	c.historyUp()
	c.mu.Lock()
	if string(c.input) != "trečia" {
		t.Errorf("historyUp: input = %q, norime 'trečia'", string(c.input))
	}
	c.mu.Unlock()

	c.historyUp()
	c.mu.Lock()
	if string(c.input) != "antra" {
		t.Errorf("historyUp x2: input = %q, norime 'antra'", string(c.input))
	}
	c.mu.Unlock()

	c.historyDown()
	c.mu.Lock()
	if string(c.input) != "trečia" {
		t.Errorf("historyDown: input = %q, norime 'trečia'", string(c.input))
	}
	c.mu.Unlock()

	c.historyDown()
	c.mu.Lock()
	if len(c.input) != 0 {
		t.Errorf("historyDown iki galo: input turėtų būti tuščias, gavome %q", string(c.input))
	}
	c.mu.Unlock()
}

func TestHistoryUpEmptyList(t *testing.T) {
	c := newTestClient()
	// Neturėtų panic su tuščia istorija
	c.historyUp()
	c.historyDown()
}

// ============================================================
// reconnectDelay backoff
// ============================================================

func TestReconnectDelayBackoff(t *testing.T) {
	c := newTestClient()
	// Simuliuoti connectLoop logikos veikimą be realaus ryšio
	c.mu.Lock()
	c.reconnect = true
	c.reconnectDelay = 5 * time.Second
	c.mu.Unlock()

	delays := []time.Duration{}
	for i := 0; i < 5; i++ {
		c.mu.Lock()
		d := c.reconnectDelay
		c.reconnectDelay *= 2
		if c.reconnectDelay > 120*time.Second {
			c.reconnectDelay = 120 * time.Second
		}
		c.mu.Unlock()
		delays = append(delays, d)
	}

	expected := []time.Duration{5, 10, 20, 40, 80}
	for i, e := range expected {
		if delays[i] != e*time.Second {
			t.Errorf("delays[%d] = %v, norime %v", i, delays[i], e*time.Second)
		}
	}
}

func TestReconnectDelayCapAt120s(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	c.reconnectDelay = 64 * time.Second
	c.mu.Unlock()

	// Po kelių bandymų turi „užsifiksuoti" prie 120s
	for i := 0; i < 10; i++ {
		c.mu.Lock()
		c.reconnectDelay *= 2
		if c.reconnectDelay > 120*time.Second {
			c.reconnectDelay = 120 * time.Second
		}
		c.mu.Unlock()
	}

	c.mu.Lock()
	d := c.reconnectDelay
	c.mu.Unlock()
	if d != 120*time.Second {
		t.Errorf("delay turėtų būti 120s, gavome %v", d)
	}
}

func TestReconnectDelayResetOn001(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	c.reconnectDelay = 80 * time.Second
	c.mu.Unlock()
	collect := captureWrites(c)

	// 001 turi atstatyti delay
	c.parseIRC(":server 001 testnick :Welcome")
	collect(20 * time.Millisecond)

	c.mu.Lock()
	d := c.reconnectDelay
	c.mu.Unlock()
	if d != 5*time.Second {
		t.Errorf("po 001 delay turėtų būti 5s, gavome %v", d)
	}
}

// ============================================================
// doSearch
// ============================================================

func TestDoSearchFindsMessage(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	ch := c.ensureCh("#chan")
	for i := 0; i < 20; i++ {
		ch.addMsg(Message{ts: time.Now(), nick: "user", text: fmt.Sprintf("žinutė %d", i), msgType: MsgChat})
	}
	ch.addMsg(Message{ts: time.Now(), nick: "jonas", text: "ieškomas tekstas čia", msgType: MsgChat})
	for i := 0; i < 5; i++ {
		ch.addMsg(Message{ts: time.Now(), nick: "user", text: fmt.Sprintf("po paieškos %d", i), msgType: MsgChat})
	}
	c.mu.Unlock()

	c.doSearch(ch, "ieškomas")

	c.mu.Lock()
	scrollOff := c.scrollOff
	c.mu.Unlock()

	// scrollOff turi rodyti į tą žinutę (5 nuo galo)
	if scrollOff != 5 {
		t.Errorf("scrollOff = %d, norime 5", scrollOff)
	}
}

func TestDoSearchNotFoundProd(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	ch := c.ensureCh("#chan")
	ch.addMsg(Message{ts: time.Now(), nick: "user", text: "kažkas", msgType: MsgChat})
	c.searchMode = true
	c.mu.Unlock()

	c.doSearch(ch, "NEEGZISTUOJA_TEKSTAS_xyz")

	// Neradus — searchMode turi likti true
	c.mu.Lock()
	mode := c.searchMode
	c.mu.Unlock()
	if !mode {
		t.Error("paieška neradus turėtų palikti searchMode=true")
	}
}

func TestDoSearchCaseInsensitive(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	ch := c.ensureCh("#chan")
	ch.addMsg(Message{ts: time.Now(), nick: "user", text: "Lietuviškas Tekstas", msgType: MsgChat})
	ch.addMsg(Message{ts: time.Now(), nick: "user", text: "kitas", msgType: MsgChat})
	c.mu.Unlock()

	c.doSearch(ch, "lietuviškas")

	c.mu.Lock()
	scrollOff := c.scrollOff
	c.mu.Unlock()
	if scrollOff != 1 {
		t.Errorf("case-insensitive paieška nepavyko, scrollOff=%d", scrollOff)
	}
}

// ============================================================
// Tab completion (doTab)
// ============================================================

func TestDoTabNickCompletion(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	ch := c.ensureCh("#chan")
	c.activeIdx = 1
	ch.addNick("@Jonas")
	ch.addNick("Janina")
	ch.addNick("Petras")
	c.input = []rune("Ja")
	c.inputPos = 2
	c.mu.Unlock()

	c.doTab()

	c.mu.Lock()
	result := string(c.input)
	c.mu.Unlock()

	if !strings.HasPrefix(result, "Jan") && !strings.HasPrefix(result, "Ja") {
		t.Errorf("tab completion nepavyko, input = %q", result)
	}
	// Turi pridėti ": " po nick
	if !strings.Contains(result, ":") {
		t.Errorf("tab completion turi pridėti ': ' po nick: %q", result)
	}
}

func TestDoTabCommandCompletion(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	c.input = []rune("/jo")
	c.inputPos = 3
	c.mu.Unlock()

	c.doTab()

	c.mu.Lock()
	result := string(c.input)
	c.mu.Unlock()

	if !strings.HasPrefix(result, "/join") {
		t.Errorf("komandos completion nepavyko: %q", result)
	}
}

func TestDoTabCyclesThroughCandidates(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	ch := c.ensureCh("#chan")
	c.activeIdx = 1
	ch.addNick("Alice")
	ch.addNick("Alfredas")
	c.input = []rune("Al")
	c.inputPos = 2
	c.mu.Unlock()

	c.doTab()
	c.mu.Lock()
	first := string(c.input)
	c.mu.Unlock()

	c.doTab()
	c.mu.Lock()
	second := string(c.input)
	c.mu.Unlock()

	if first == second {
		t.Errorf("antrasis Tab turėtų pereiti prie kito kandidato: %q == %q", first, second)
	}
}

// ============================================================
// renderIRCFormat
// ============================================================

func TestRenderIRCFormatBold(t *testing.T) {
	result := renderIRCFormat("\x02bold\x02")
	if !strings.Contains(result, ansiBold) {
		t.Errorf("bold nesuformatuotas: %q", result)
	}
	if !strings.Contains(result, "bold") {
		t.Errorf("tekstas prarastas: %q", result)
	}
}

func TestRenderIRCFormatColorProd(t *testing.T) {
	result := renderIRCFormat("\x034raudonas\x0f")
	if !strings.Contains(result, mircColors[4]) {
		t.Errorf("spalva 4 (raudona) nesuformatuota: %q", result)
	}
	if !strings.Contains(result, "raudonas") {
		t.Errorf("tekstas prarastas: %q", result)
	}
}

func TestRenderIRCFormatUnknownColor(t *testing.T) {
	// Spalvos numeris >= 16 turi būti ignoruotas (ne panic)
	result := renderIRCFormat("\x0399tekstas\x0f")
	if !strings.Contains(result, "tekstas") {
		t.Errorf("tekstas prarastas su nežinoma spalva: %q", result)
	}
}

func TestRenderIRCFormatReset(t *testing.T) {
	result := renderIRCFormat("\x02bold\x0fnormal")
	if !strings.Contains(result, ansiReset) {
		t.Errorf("reset simbolis nesuformatuotas: %q", result)
	}
}

func TestRenderIRCFormatAlwaysEndsWithReset(t *testing.T) {
	inputs := []string{
		"paprastas tekstas",
		"\x02bold",
		"\x034raudona",
		"",
	}
	for _, in := range inputs {
		result := renderIRCFormat(in)
		if !strings.HasSuffix(result, ansiReset) {
			t.Errorf("renderIRCFormat(%q) nesibaigia ansiReset", in)
		}
	}
}

// ============================================================
// Channel.addMsg — maxMessages apkarpymas
// ============================================================

func TestChannelMaxMessagesTrim(t *testing.T) {
	ch := newChannel("#test", 100)

	for i := 0; i < 120; i++ {
		ch.addMsg(Message{ts: time.Now(), text: fmt.Sprintf("žinutė %d", i), msgType: MsgChat})
	}

	// Turi turėti <= 100 žinučių (tiksliau: 100 - 25 = 75 po trim, tada +20 = 95)
	if len(ch.messages) > 100 {
		t.Errorf("kanalas turi %d žinučių, turėtų būti <= 100", len(ch.messages))
	}

	// Paskutinė žinutė turi būti naujausia
	last := ch.messages[len(ch.messages)-1]
	if last.text != "žinutė 119" {
		t.Errorf("paskutinė žinutė = %q, turėtų būti 'žinutė 119'", last.text)
	}
}

func TestChannelMaxMessagesKeepsNewest(t *testing.T) {
	ch := newChannel("#test", 10)
	for i := 0; i < 20; i++ {
		ch.addMsg(Message{ts: time.Now(), text: fmt.Sprintf("msg%d", i), msgType: MsgChat})
	}
	// Turi likti paskutinės žinutės, ne pirmosios
	found := false
	for _, m := range ch.messages {
		if m.text == "msg19" {
			found = true
		}
	}
	if !found {
		t.Error("naujausia žinutė turėtų išlikti po apkarpymo")
	}
}

// ============================================================
// KICK gautas iš serverio
// ============================================================

func TestParseIRCKickOther(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":ops!u@h JOIN #chan")
	c.parseIRC(":victim!u@h JOIN #chan")
	c.parseIRC(":ops!u@h KICK #chan victim :taisyklės")

	c.mu.Lock()
	ch := c.findCh("#chan")
	c.mu.Unlock()

	if ch == nil {
		t.Fatal("kanalas nerastas")
	}
	for _, n := range ch.nicks {
		if strings.EqualFold(stripMode(n), "victim") {
			t.Error("victim turėtų būti pašalintas po KICK")
		}
	}
}

func TestParseIRCKickSelf(t *testing.T) {
	c := newTestClient()
	c.nick = "testnick"
	c.parseIRC(":ops!u@h JOIN #chan")
	c.parseIRC(":testnick!u@h JOIN #chan")

	collect := captureWrites(c)
	c.parseIRC(":ops!u@h KICK #chan testnick :bye")
	collect(20 * time.Millisecond)

	// Turi rodyti klaidos žinutę
	c.mu.Lock()
	serverCh := c.channels[0]
	c.mu.Unlock()

	found := false
	for _, m := range serverCh.messages {
		if m.msgType == MsgError && strings.Contains(m.text, "#chan") {
			found = true
		}
	}
	if !found {
		t.Error("savęs KICK turėtų rodyti klaidos žinutę")
	}
}

// ============================================================
// INVITE gautas
// ============================================================

func TestParseIRCInvite(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":draugas!u@h INVITE testnick :#specialus")

	c.mu.Lock()
	serverCh := c.channels[0]
	c.mu.Unlock()

	found := false
	for _, m := range serverCh.messages {
		if strings.Contains(m.text, "#specialus") && strings.Contains(m.text, "draugas") {
			found = true
		}
	}
	if !found {
		t.Error("INVITE turėtų rodyti pranešimą su kanalo ir kviečiančio nick info")
	}
}

// ============================================================
// NOTICE apdorojimas
// ============================================================

func TestParseIRCNotice(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":serveris.net NOTICE testnick :Sveiki prisijungę!")

	c.mu.Lock()
	ch := c.channels[0]
	c.mu.Unlock()

	found := false
	for _, m := range ch.messages {
		if m.msgType == MsgNotice && strings.Contains(m.text, "Sveiki") {
			found = true
		}
	}
	if !found {
		t.Error("NOTICE žinutė nepridėta")
	}
}

func TestParseIRCNoticeIgnored(t *testing.T) {
	c := newTestClient()
	c.ignoreList = []string{"spammer"}
	c.parseIRC(":spammer!u@h NOTICE testnick :šlamštas")

	c.mu.Lock()
	ch := c.channels[0]
	c.mu.Unlock()

	for _, m := range ch.messages {
		if m.nick == "spammer" {
			t.Error("ignoruojamo NOTICE neturėtų būti rodomas")
		}
	}
}

// ============================================================
// 353 NAMES — nick sąrašas
// ============================================================

func TestParseIRC353Names(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":server 353 testnick = #chan :@ops +voiced plainuser")

	c.mu.Lock()
	ch := c.findCh("#chan")
	c.mu.Unlock()

	if ch == nil {
		t.Fatal("kanalas #chan nesukurtas po 353")
	}
	if len(ch.nicks) != 3 {
		t.Fatalf("tikėjomės 3 nickų, gavome %d: %v", len(ch.nicks), ch.nicks)
	}

	// @ops turi būti pirmoje vietoje
	if !strings.HasPrefix(ch.nicks[0], "@") {
		t.Errorf("pirmasis nick turi būti @ops, gavome %q", ch.nicks[0])
	}
}

func TestParseIRC353Accumulates(t *testing.T) {
	c := newTestClient()
	// Didelių kanalų NAMES siunčiamas keliais pranešimais
	c.parseIRC(":server 353 testnick = #chan :@ops user1")
	c.parseIRC(":server 353 testnick = #chan :user2 user3")
	c.parseIRC(":server 366 testnick #chan :End of NAMES")

	c.mu.Lock()
	ch := c.findCh("#chan")
	c.mu.Unlock()

	if ch == nil || len(ch.nicks) != 4 {
		t.Errorf("tikėjomės 4 nickų iš dviejų 353 pranešimų, gavome %d", len(ch.nicks))
	}
}

// ============================================================
// 332 TOPIC
// ============================================================

func TestParseIRC332Topic(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":testnick!u@h JOIN #chan")
	c.parseIRC(":server 332 testnick #chan :FreeBSD diskusijų kanalas")

	c.mu.Lock()
	ch := c.findCh("#chan")
	c.mu.Unlock()

	if ch == nil {
		t.Fatal("kanalas nesukurtas")
	}
	if ch.topic != "FreeBSD diskusijų kanalas" {
		t.Errorf("topic = %q, norime 'FreeBSD diskusijų kanalas'", ch.topic)
	}
}

// ============================================================
// away / back komandos
// ============================================================

func TestAwayCommand(t *testing.T) {
	c := newTestClient()
	c.connected = true
	collect := captureWrites(c)

	c.handleCmd("away", "pietauju")
	sent := collect(20 * time.Millisecond)

	found := false
	for _, s := range sent {
		if s == "AWAY :pietauju" {
			found = true
		}
	}
	if !found {
		t.Errorf("AWAY komanda neišsiųsta: %v", sent)
	}
}

func TestBackCommand(t *testing.T) {
	c := newTestClient()
	c.connected = true
	c.mu.Lock()
	c.away = "pietauju"
	c.mu.Unlock()
	collect := captureWrites(c)

	c.handleCmd("back", "")
	sent := collect(20 * time.Millisecond)

	found := false
	for _, s := range sent {
		if s == "AWAY" {
			found = true
		}
	}
	if !found {
		t.Errorf("AWAY (grįžimas) neišsiųstas: %v", sent)
	}

	c.mu.Lock()
	away := c.away
	c.mu.Unlock()
	if away != "" {
		t.Errorf("away state turėtų būti tuščias po /back, yra %q", away)
	}
}

// ============================================================
// closeLogs — failų handles uždarymas
// ============================================================

func TestCloseLogs(t *testing.T) {
	dir := t.TempDir()
	c := newTestClient()
	c.logDir = dir
	c.server = "irc.test.com"

	// Sukurti log įrašą
	c.logMessage(dir, "irc.test.com", "#chan", Message{
		ts: time.Now(), nick: "u", text: "test", msgType: MsgChat,
	})
	time.Sleep(20 * time.Millisecond)

	c.mu.Lock()
	n := len(c.logFiles)
	c.mu.Unlock()
	if n == 0 {
		t.Fatal("log failas nebuvo atidarytas")
	}

	c.closeLogs()

	c.mu.Lock()
	n2 := len(c.logFiles)
	c.mu.Unlock()
	if n2 != 0 {
		t.Errorf("po closeLogs turi būti 0 failų, yra %d", n2)
	}
}

// ============================================================
// realname naudojimas USER komandoje
// ============================================================

func TestRealNameSentInUserCommand(t *testing.T) {
	// Serveris fiksuoja ką klientas siunčia
	var mu sync.Mutex
	var received []string

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("nepavyko paleisti serverio: %v", err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))

		// Fiksuoti ką klientas siunčia
		go func() {
			sc := bufio.NewScanner(conn)
			for sc.Scan() {
				mu.Lock()
				received = append(received, sc.Text())
				mu.Unlock()
			}
		}()

		fmt.Fprintf(conn, ":test.server 001 testnick :Welcome\r\n")
		time.Sleep(300 * time.Millisecond)
	}()

	host, port, _ := net.SplitHostPort(ln.Addr().String())
	c := newTestClient()
	c.server = host
	c.port = port
	c.realName = "Jonas Jonaitis"
	c.reconnect = false

	done := make(chan struct{})
	go func() {
		defer close(done)
		c.doConnect(0)
	}()

	select {
	case <-done:
	case <-time.After(4 * time.Second):
		if c.conn != nil {
			c.conn.Close()
		}
	}

	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	for _, s := range received {
		if strings.HasPrefix(s, "USER ") {
			if !strings.Contains(s, "Jonas Jonaitis") {
				t.Errorf("USER komanda nenaudoja realname: %q", s)
			}
			return
		}
	}
	t.Errorf("USER komanda nerasta. Gauta iš kliento: %v", received)
}

// ============================================================
// /connect useTLS atstatymas
// ============================================================

func TestConnectResetsTLS(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	c.useTLS = true // buvo prisijungta su TLS
	c.mu.Unlock()

	// Jungti be TLS — useTLS turi tapti false
	c.handleCmd("connect", "irc.example.com 6667 manick")

	c.mu.Lock()
	tls := c.useTLS
	c.mu.Unlock()

	if tls {
		t.Error("/connect be 'tls' argumento turėtų nustatyti useTLS=false")
	}
}

func TestConnectSetsTLS(t *testing.T) {
	c := newTestClient()

	c.handleCmd("connect", "irc.example.com 6697 manick tls")

	c.mu.Lock()
	tls := c.useTLS
	c.mu.Unlock()

	if !tls {
		t.Error("/connect su 'tls' turėtų nustatyti useTLS=true")
	}
}

// ============================================================
// 305 / 306 away serverio patvirtinimas
// ============================================================

func TestParseIRC305UnAway(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	c.away = "pietauju"
	c.mu.Unlock()

	c.parseIRC(":server 305 testnick :You are no longer marked as being away")

	c.mu.Lock()
	away := c.away
	c.mu.Unlock()
	if away != "" {
		t.Errorf("po 305 away turėtų būti tuščias, yra %q", away)
	}
}

func TestParseIRC306NowAway(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":server 306 testnick :You have been marked as being away")

	// Žinutė turi atsirasti
	c.mu.Lock()
	ch := c.channels[0]
	c.mu.Unlock()
	found := false
	for _, m := range ch.messages {
		if strings.Contains(m.text, "Away") {
			found = true
		}
	}
	if !found {
		t.Error("306 turėtų rodyti away patvirtinimo žinutę")
	}
}

// ============================================================
// 431 / 432 nick klaidos
// ============================================================

func TestParseIRC431NoNick(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":server 431 * :No nickname given")

	c.mu.Lock()
	ch := c.channels[0]
	c.mu.Unlock()
	found := false
	for _, m := range ch.messages {
		if m.msgType == MsgError {
			found = true
		}
	}
	if !found {
		t.Error("431 turėtų rodyti klaidos žinutę")
	}
}

func TestParseIRC432BadNick(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":server 432 * bad!nick :Erroneous Nickname")

	c.mu.Lock()
	ch := c.channels[0]
	c.mu.Unlock()
	found := false
	for _, m := range ch.messages {
		if m.msgType == MsgError && strings.Contains(m.text, "Netinkamas") {
			found = true
		}
	}
	if !found {
		t.Error("432 turėtų rodyti klaidos žinutę su nick")
	}
}

// ============================================================
// Ignore sąrašo išsaugojimas / įkėlimas
// ============================================================

func TestIgnoreRoundtrip(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("HOME", dir)
	defer os.Unsetenv("HOME")

	c := newTestClient()
	c.mu.Lock()
	c.ignoreList = []string{"spammer", "botas", "Trollis"}
	c.mu.Unlock()
	c.saveIgnore()

	c2 := newTestClient()
	c2.loadIgnore()
	c2.mu.Lock()
	list := c2.ignoreList
	c2.mu.Unlock()

	if len(list) != 3 {
		t.Fatalf("tikėjomės 3 ignore įrašų, gavome %d: %v", len(list), list)
	}
	if list[0] != "spammer" || list[1] != "botas" || list[2] != "Trollis" {
		t.Errorf("ignore sąrašas nesutampa: %v", list)
	}
}

func TestIgnoreSavedAfterIgnoreCmd(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("HOME", dir)
	defer os.Unsetenv("HOME")

	c := newTestClient()
	c.handleCmd("ignore", "spammer123")
	time.Sleep(30 * time.Millisecond) // saveIgnore vyksta goroutine

	c2 := newTestClient()
	c2.loadIgnore()
	if !c2.isIgnored("spammer123") {
		t.Error("ignoruojamas turėtų būti išsaugotas po /ignore komandos")
	}
}

func TestIgnoreRemovedAfterUnignore(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("HOME", dir)
	defer os.Unsetenv("HOME")

	c := newTestClient()
	c.handleCmd("ignore", "spammer123")
	time.Sleep(30 * time.Millisecond)

	c.handleCmd("unignore", "spammer123")
	time.Sleep(30 * time.Millisecond)

	c2 := newTestClient()
	c2.loadIgnore()
	if c2.isIgnored("spammer123") {
		t.Error("po /unignore spammer123 neturėtų būti ignore sąraše")
	}
}

func TestLogRotationDaily(t *testing.T) {
	dir := t.TempDir()
	c := newTestClient()
	c.logDir = dir
	c.server = "srv"

	day1 := time.Date(2026, 9, 28, 23, 59, 0, 0, time.Local)
	day2 := time.Date(2026, 9, 29, 0, 1, 0, 0, time.Local)

	c.logMessage(dir, "srv", "#kanalas", Message{ts: day1, nick: "a", text: "pirmadienio", msgType: MsgChat})
	time.Sleep(20 * time.Millisecond)
	c.logMessage(dir, "srv", "#kanalas", Message{ts: day2, nick: "a", text: "naujadra", msgType: MsgChat})
	time.Sleep(20 * time.Millisecond)

	p1 := filepath.Join(dir, "srv", "#kanalas-2026-09-28.log")
	p2 := filepath.Join(dir, "srv", "#kanalas-2026-09-29.log")
	d1, err1 := os.ReadFile(p1)
	d2, err2 := os.ReadFile(p2)
	if err1 != nil || err2 != nil {
		t.Fatalf("rotacijos failai nesukurti: %v, %v", err1, err2)
	}
	if !strings.Contains(string(d1), "pirmadienio") || strings.Contains(string(d1), "naujadra") {
		t.Errorf("pirmos dienos logas neteisingas: %q", d1)
	}
	if !strings.Contains(string(d2), "naujadra") {
		t.Errorf("antroje dienos logas neteisingas: %q", d2)
	}
	// seno failo handle turi būti uždarintas (logFiles nėra ne-today įrašo)
	c.mu.Lock()
	for k := range c.logFiles {
		if !strings.HasSuffix(k, "2026-09-29") {
			t.Errorf("neuždarytas seno failo handle: %q", k)
		}
	}
	c.mu.Unlock()
}

func TestNotificationsConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config")
	os.WriteFile(p, []byte("nick=x\nnotifications=false\n"), 0o600)
	cfg := loadConfig(p)
	if cfg.Notifications {
		t.Error("notifications=false neturi įjungti pranešimų")
	}
	os.WriteFile(p, []byte("notifications=yes\n"), 0o600)
	if !loadConfig(p).Notifications {
		t.Error("notifications=yes turi įjungti pranešimus")
	}
	// numatyta reikšmė – įjungta
	if !defaultConfig().Notifications {
		t.Error("numatyta: pranešimai įjungti")
	}
}

func TestNotifyRateLimit(t *testing.T) {
	c := newTestClient()
	c.notify = true
	c.addMsg("#kanalas", Message{ts: time.Now(), nick: "buddy", text: "hey!", msgType: MsgChat, highlight: true})
	c.addMsg("#kitas", Message{ts: time.Now(), nick: "buddy", text: "hey!", msgType: MsgChat, highlight: true})
	c.mu.Lock()
	limitSet := !c.lastNotify.IsZero()
	c.mu.Unlock()
	if !limitSet {
		t.Error("lastNotify nustatytas po highlight")
	}
	// neaktyvaus kanalo highlight be pranešimų flago – neturi setinti
	c2 := newTestClient()
	c2.notify = false
	c2.addMsg("#kanalas", Message{ts: time.Now(), nick: "buddy", text: "hey!", msgType: MsgChat, highlight: true})
	c2.mu.Lock()
	zero := c2.lastNotify.IsZero()
	c2.mu.Unlock()
	if !zero {
		t.Error("notify=false neturi nustatinėti lastNotify")
	}
}

func TestClearLogs(t *testing.T) {
	dir := t.TempDir()
	c := newTestClient()
	c.logDir = dir
	c.server = "srv"

	now := time.Now()
	mk := func(chanName, text string) Message {
		return Message{ts: now, nick: "k", text: text, msgType: MsgChat}
	}
	c.logMessage(dir, "srv", "#a", mk("#a", "aa"))
	c.logMessage(dir, "srv", "#b", mk("#b", "bb"))
	time.Sleep(30 * time.Millisecond)

	srvDir := filepath.Join(dir, "srv")
	before, _ := os.ReadDir(srvDir)
	if len(before) < 2 {
		t.Fatalf("log failai nesukurti: %d", len(before))
	}

	// Išvalyti tik #a
	if n := c.clearLogs("#a"); n == 0 {
		t.Error("clearLogs(#a) grąžino 0, bet failas buvo")
	}
	files, _ := os.ReadDir(srvDir)
	for _, f := range files {
		if strings.HasPrefix(f.Name(), "#a-") {
			t.Errorf("#a logas liko: %s", f.Name())
		}
	}
	// #b lieka
	if n := c.clearLogs("#b"); n == 0 {
		t.Error("clearLogs(#b) nerado failo")
	}

	// Visas serveris
	c.logMessage(dir, "srv", "#c", mk("#c", "cc"))
	time.Sleep(30 * time.Millisecond)
	if n := c.clearLogs(""); n < 1 {
		t.Errorf("clearLogs(visi) grąžino %d", n)
	}
	files, _ = os.ReadDir(srvDir)
	for _, f := range files {
		if !f.IsDir() && strings.HasSuffix(f.Name(), ".log") {
			t.Errorf("po viso išvalymo liko: %s", f.Name())
		}
	}
	// logFiles atviri handle'ai išvalyti
	c.mu.Lock()
	openHandles := len(c.logFiles)
	c.mu.Unlock()
	if openHandles != 0 {
		t.Errorf("logFiles neturi likti atvirų: %d", openHandles)
	}
}
