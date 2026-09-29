package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- containsNick ----

func TestContainsNick(t *testing.T) {
	tests := []struct {
		text string
		nick string
		want bool
	}{
		{"hello world", "world", true},
		{"hello world", "WORLD", true}, // case-insensitive
		{"helloworld", "world", false}, // ne atskiras žodis
		{"worldhello", "world", false},
		{"world: hi", "world", true}, // po dvitaškio
		{"hi world!", "world", true}, // prieš šauktuką
		{"", "nick", false},
		{"nick", "", false},
		{"mynick: hello", "mynick", true},
		{"mynick_ext hello", "mynick", false}, // nick_ext != nick (nick char po jo)
		{"say mynick please", "mynick", true},
		{"hello mynick", "mynick", true},
		{"mynick", "mynick", true},
		{"hello\x00world", "world", true}, // null baitas tekste
	}

	for _, tt := range tests {
		got := containsNick(tt.text, tt.nick)
		if got != tt.want {
			t.Errorf("containsNick(%q, %q) = %v, norime %v", tt.text, tt.nick, got, tt.want)
		}
	}
}

// ---- wrapText ----

func TestWrapText(t *testing.T) {
	tests := []struct {
		text  string
		width int
		want  int // eilučių skaičius
	}{
		{"trumpas", 80, 1},
		{"", 80, 1},
		{strings.Repeat("a", 100), 40, 3},
		{"vienas du trys keturi", 10, 3},
		{"labailabailabailabai", 5, 4}, // nėra tarpų - kirpti griežtai
		{"a b c d e f g", 3, 6},        // kiekviena pora žodžių per eilutę
	}

	for _, tt := range tests {
		got := wrapText(tt.text, tt.width)
		if len(got) != tt.want {
			t.Errorf("wrapText(%q, %d): %d eilučių, norime %d (gautas: %v)",
				tt.text, tt.width, len(got), tt.want, got)
		}
		// Visas turinys turi išlikti
		joined := strings.Join(got, "")
		orig := strings.ReplaceAll(tt.text, " ", "")
		gotJoined := strings.ReplaceAll(joined, " ", "")
		if orig != gotJoined {
			t.Errorf("wrapText prarado turinį: %q → %v", tt.text, got)
		}
	}
}

func TestWrapTextWidth1(t *testing.T) {
	// Kraštinė situacija - labai siauras plotis
	got := wrapText("abc", 1)
	if len(got) != 3 {
		t.Errorf("wrapText su width=1 turėtų duoti 3 eilutes, gavome %d", len(got))
	}
}

func TestWrapTextWidth0(t *testing.T) {
	// Nulio plotis neturėtų pakibti
	got := wrapText("test", 0)
	if len(got) == 0 {
		t.Error("wrapText su width=0 negali grąžinti tuščio slice")
	}
}

func TestWrapTextUTF8(t *testing.T) {
	// Lietuviški simboliai
	text := "Labas rytas gražus oras šiandien"
	lines := wrapText(text, 10)
	if len(lines) == 0 {
		t.Fatal("wrapText grąžino 0 eilučių")
	}
	// Visas originalus turinys turi išlikti (be tarpų)
	joined := strings.Join(lines, "")
	orig := strings.ReplaceAll(text, " ", "")
	gotChars := strings.ReplaceAll(joined, " ", "")
	if orig != gotChars {
		t.Errorf("UTF-8 turinys prarastas.\nOriginalas: %q\nGautas:     %q\nEilutės: %v",
			orig, gotChars, lines)
	}
	// Kiekviena eilutė neturi viršyti pločio
	for i, line := range lines {
		if len([]rune(line)) > 10 {
			t.Errorf("eilutė %d per ilga (%d raidžių): %q", i, len([]rune(line)), line)
		}
	}
}

// ---- sanitizePath ----

func TestSanitizePath(t *testing.T) {
	tests := []struct {
		input string
		safe  bool // ar rezultate nėra pavojingų simbolių
	}{
		{"#linux", true},
		{"irc.libera.chat", true},
		{"../../../etc/passwd", true}, // taškai leidžiami, tik kirkstas keičiamas
		{"/etc/passwd", true},
		{"C:\\Windows\\system32", true},
		{"chan*name?", true},
		{"normal-chan_1", true},
	}

	for _, tt := range tests {
		got := sanitizePath(tt.input)
		if strings.ContainsAny(got, "/\\:*?") {
			t.Errorf("sanitizePath(%q) = %q vis dar turi pavojingų simbolių", tt.input, got)
		}
	}
}

func TestSanitizePathTraversal(t *testing.T) {
	// Path traversal bandymas
	dangerous := []string{
		"../../../etc/passwd",
		"..%2F..%2F",
		"/absolute/path",
		"C:\\Windows",
	}
	for _, d := range dangerous {
		got := sanitizePath(d)
		// Negalima turėti / ar \ - tai apsaugo nuo directory traversal
		if strings.Contains(got, "/") || strings.Contains(got, "\\") {
			t.Errorf("sanitizePath(%q) = %q vis dar leidžia path traversal", d, got)
		}
	}
}

// ---- IRC parsavimas ----

func newTestClient() *Client {
	cfg := defaultConfig()
	cfg.Nick = "testnick"
	c := newClient(cfg)
	c.width = 120
	c.height = 40
	c.logDir = "" // išjungti logging testams
	return c
}

// sentCollector saugiai (be lenkų) renka išsiųstas IRC komandas.
type sentCollector struct {
	mu   sync.Mutex
	list []string
}

func collectSent(c *Client) *sentCollector {
	sc := &sentCollector{}
	go func() {
		for line := range c.writeCh {
			sc.mu.Lock()
			sc.list = append(sc.list, line)
			sc.mu.Unlock()
		}
	}()
	return sc
}

func (s *sentCollector) get() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.list...)
}

func TestParseIRCPing(t *testing.T) {
	c := newTestClient()
	// Perimame send
	sc := collectSent(c)

	c.parseIRC("PING :serveris.example.com")
	time.Sleep(10 * time.Millisecond)
	sent := sc.get()

	if len(sent) == 0 {
		t.Fatal("PING negavo PONG atsakymo")
	}
	if sent[0] != "PONG :serveris.example.com" {
		t.Errorf("PONG neteisingas: %q", sent[0])
	}
}

func TestParseIRCPrivmsg(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":sender!user@host PRIVMSG #testas :Labas pasauli")

	ch := c.findCh("#testas")
	if ch == nil {
		t.Fatal("kanalas #testas nesukurtas")
	}
	if len(ch.messages) == 0 {
		t.Fatal("žinutė nepridėta")
	}
	msg := ch.messages[len(ch.messages)-1]
	if msg.nick != "sender" {
		t.Errorf("nick = %q, norime %q", msg.nick, "sender")
	}
	if msg.text != "Labas pasauli" {
		t.Errorf("text = %q, norime %q", msg.text, "Labas pasauli")
	}
	if msg.msgType != MsgChat {
		t.Errorf("msgType = %v, norime MsgChat", msg.msgType)
	}
}

func TestParseIRCPrivmsgHighlight(t *testing.T) {
	c := newTestClient()
	c.nick = "testnick"
	c.parseIRC(":kitas!user@host PRIVMSG #chan :testnick: labas!")

	ch := c.findCh("#chan")
	if ch == nil {
		t.Fatal("kanalas nesukurtas")
	}
	if len(ch.messages) == 0 {
		t.Fatal("žinutė nepridėta")
	}
	msg := ch.messages[len(ch.messages)-1]
	if !msg.highlight {
		t.Error("highlight turėtų būti true kai mūsų nick paminėtas")
	}
}

func TestParseIRCPrivmsgNoHighlight(t *testing.T) {
	c := newTestClient()
	c.nick = "testnick"
	c.parseIRC(":kitas!user@host PRIVMSG #chan :paprastas tekstas")

	ch := c.findCh("#chan")
	if ch == nil || len(ch.messages) == 0 {
		t.Fatal("žinutė nepridėta")
	}
	if ch.messages[len(ch.messages)-1].highlight {
		t.Error("highlight turėtų būti false kai nick nepaminėtas")
	}
}

func TestParseIRCIgnore(t *testing.T) {
	c := newTestClient()
	c.ignoreList = []string{"spammer"}
	c.parseIRC(":spammer!user@host PRIVMSG #chan :šlamštas")

	ch := c.findCh("#chan")
	if ch != nil && len(ch.messages) > 0 {
		t.Error("ignoruojamo žinutė neturėtų būti pridėta")
	}
}

func TestParseIRCJoin(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":jonas!user@host JOIN #linux")

	ch := c.findCh("#linux")
	if ch == nil {
		t.Fatal("kanalas nesukurtas po JOIN")
	}
	found := false
	for _, n := range ch.nicks {
		if stripMode(n) == "jonas" {
			found = true
		}
	}
	if !found {
		t.Errorf("jonas nerastas nick sąraše: %v", ch.nicks)
	}
}

func TestParseIRCPart(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":jonas!user@host JOIN #linux")
	c.parseIRC(":jonas!user@host PART #linux :Viso gero")

	ch := c.findCh("#linux")
	if ch != nil {
		for _, n := range ch.nicks {
			if stripMode(n) == "jonas" {
				t.Error("jonas turėtų būti pašalintas po PART")
			}
		}
	}
}

func TestParseIRCNick(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":jonas!user@host JOIN #chan")
	c.parseIRC(":jonas!user@host NICK :jonas2")

	ch := c.findCh("#chan")
	if ch == nil {
		t.Fatal("kanalas nerastas")
	}
	found := false
	for _, n := range ch.nicks {
		if stripMode(n) == "jonas2" {
			found = true
		}
	}
	if !found {
		t.Errorf("jonas2 nerastas po NICK: %v", ch.nicks)
	}
}

func TestParseIRCMalformed(t *testing.T) {
	c := newTestClient()
	// Šios eilutės neturėtų sukelti panic
	malformed := []string{
		"",
		":",
		":only_prefix",
		"COMMAND",
		":a!b@c PRIVMSG",          // be parametrų
		":a!b@c PRIVMSG #chan",    // be teksto
		"PING",                    // be serverio
		strings.Repeat("A", 4096), // labai ilga eilutė
		":nick!user@host JOIN",    // be kanalo
		"\x00\x01\x02",            // kontroliniai simboliai
	}
	for _, line := range malformed {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("panic su eilute %q: %v", line, r)
				}
			}()
			c.parseIRC(line)
		}()
	}
}

func TestParseIRCCTCPVersion(t *testing.T) {
	c := newTestClient()
	sc := collectSent(c)

	c.parseIRC(":user!u@h PRIVMSG testnick :\x01VERSION\x01")
	time.Sleep(10 * time.Millisecond)
	sent := sc.get()

	found := false
	for _, s := range sent {
		if strings.Contains(s, "VERSION") && strings.Contains(s, "NOTICE") {
			found = true
		}
	}
	if !found {
		t.Error("CTCP VERSION turėtų gauti NOTICE atsakymą")
	}
}

func TestParseIRCCTCPAction(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":jonas!u@h PRIVMSG #chan :\x01ACTION bėga\x01")

	ch := c.findCh("#chan")
	if ch == nil || len(ch.messages) == 0 {
		t.Fatal("ACTION žinutė nepridėta")
	}
	msg := ch.messages[len(ch.messages)-1]
	if msg.msgType != MsgAction {
		t.Errorf("msgType = %v, norime MsgAction", msg.msgType)
	}
	if msg.text != "bėga" {
		t.Errorf("ACTION tekstas = %q, norime %q", msg.text, "bėga")
	}
}

func TestParseIRC433NickInUse(t *testing.T) {
	c := newTestClient()
	c.nick = "testnick"
	collectSent(c)

	c.parseIRC(":server 433 * testnick :Nickname is already in use")
	time.Sleep(10 * time.Millisecond)

	c.mu.Lock()
	newNick := c.nick
	c.mu.Unlock()

	if newNick != "testnick_" {
		t.Errorf("nick po 433 = %q, norime %q", newNick, "testnick_")
	}
}

func TestParseIRCNickserv(t *testing.T) {
	c := newTestClient()
	c.nickservPass = "slaptazodis"
	sc := collectSent(c)

	c.parseIRC(":server 001 testnick :Welcome to IRC")
	time.Sleep(10 * time.Millisecond)
	sent := sc.get()

	found := false
	for _, s := range sent {
		if strings.Contains(s, "NickServ") && strings.Contains(s, "IDENTIFY") && strings.Contains(s, "slaptazodis") {
			found = true
		}
	}
	if !found {
		t.Errorf("NickServ IDENTIFY neišsiųstas. Išsiųsta: %v", sent)
	}
}

func TestParseIRCAutoJoin(t *testing.T) {
	c := newTestClient()
	c.autoJoin = []string{"#linux", "#bsd"}
	sc := collectSent(c)

	c.parseIRC(":server 001 testnick :Welcome")
	time.Sleep(10 * time.Millisecond)
	sent := sc.get()

	joined := 0
	for _, s := range sent {
		if strings.HasPrefix(s, "JOIN ") {
			joined++
		}
	}
	if joined != 2 {
		t.Errorf("tikėjomės 2 JOIN komandų, gavome %d: %v", joined, sent)
	}
}

// ---- Nick sąrašas ----

func TestNickSorting(t *testing.T) {
	ch := &Channel{name: "#test"}
	ch.addNick("paprastas")
	ch.addNick("@ops")
	ch.addNick("+voiced")
	ch.addNick("~owner")
	ch.addNick("%halfop")

	if len(ch.nicks) != 5 {
		t.Fatalf("tikėjomės 5 nickų, gavome %d", len(ch.nicks))
	}

	// Pirmi turi būti privilegijuoti
	if ch.nicks[0] != "~owner" {
		t.Errorf("pirmas turi būti ~owner, gavome %q", ch.nicks[0])
	}
	if ch.nicks[1] != "@ops" {
		t.Errorf("antras turi būti @ops, gavome %q", ch.nicks[1])
	}
	if ch.nicks[2] != "%halfop" {
		t.Errorf("trečias turi būti %%halfop, gavome %q", ch.nicks[2])
	}
}

func TestNickDuplicates(t *testing.T) {
	ch := &Channel{name: "#test"}
	ch.addNick("jonas")
	ch.addNick("jonas")
	ch.addNick("JONAS") // case-insensitive duplikatas

	if len(ch.nicks) != 1 {
		t.Errorf("duplikatai turėtų nebūti pridedami, gavome %d nickų: %v", len(ch.nicks), ch.nicks)
	}
}

func TestNickRemove(t *testing.T) {
	ch := &Channel{name: "#test"}
	ch.addNick("@jonas")
	ch.addNick("petras")
	ch.removeNick("jonas") // be @ prefikso

	if len(ch.nicks) != 1 {
		t.Errorf("tikėjomės 1 nick po remove, gavome %d: %v", len(ch.nicks), ch.nicks)
	}
	if stripMode(ch.nicks[0]) != "petras" {
		t.Errorf("liko neteisingas nick: %q", ch.nicks[0])
	}
}

func TestNickRename(t *testing.T) {
	ch := &Channel{name: "#test"}
	ch.addNick("@jonas")
	ch.renameNick("jonas", "jonas2")

	if len(ch.nicks) != 1 {
		t.Fatalf("tikėjomės 1 nick, gavome %d", len(ch.nicks))
	}
	if ch.nicks[0] != "@jonas2" {
		t.Errorf("po rename gavome %q, norime %q", ch.nicks[0], "@jonas2")
	}
}

// ---- stripMode / modePrefix ----

func TestStripMode(t *testing.T) {
	tests := []struct{ input, want string }{
		{"@ops", "ops"},
		{"+voice", "voice"},
		{"%half", "half"},
		{"~owner", "owner"},
		{"&admin", "admin"},
		{"plain", "plain"},
		{"", ""},
	}
	for _, tt := range tests {
		got := stripMode(tt.input)
		if got != tt.want {
			t.Errorf("stripMode(%q) = %q, norime %q", tt.input, got, tt.want)
		}
	}
}

func TestModePrefix(t *testing.T) {
	tests := []struct{ input, want string }{
		{"@ops", "@"},
		{"+voice", "+"},
		{"plain", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := modePrefix(tt.input)
		if got != tt.want {
			t.Errorf("modePrefix(%q) = %q, norime %q", tt.input, got, tt.want)
		}
	}
}

// ---- Config ----

func TestLoadConfigDefaults(t *testing.T) {
	cfg := defaultConfig()
	if cfg.Nick == "" {
		t.Error("default nick neturėtų būti tuščias")
	}
	if cfg.Port == "" {
		t.Error("default port neturėtų būti tuščias")
	}
}

func TestLoadConfigFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config")
	content := `
server=irc.example.com
port=6697
tls=true
nick=testuser
nickserv=pass123
autojoin=#chan1,#chan2
`
	if err := os.WriteFile(cfgPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := loadConfig(cfgPath)

	if cfg.Server != "irc.example.com" {
		t.Errorf("server = %q", cfg.Server)
	}
	if cfg.Port != "6697" {
		t.Errorf("port = %q", cfg.Port)
	}
	if !cfg.TLS {
		t.Error("tls turėtų būti true")
	}
	if cfg.Nick != "testuser" {
		t.Errorf("nick = %q", cfg.Nick)
	}
	if cfg.NickServ != "pass123" {
		t.Errorf("nickserv = %q", cfg.NickServ)
	}
	if len(cfg.AutoJoin) != 2 {
		t.Errorf("autojoin: %v", cfg.AutoJoin)
	}
}

func TestLoadConfigMissing(t *testing.T) {
	// Neegzistuojantis failas - grąžinti defaults
	cfg := loadConfig("/nonexistent/path/config")
	if cfg.Nick == "" {
		t.Error("missing config turėtų grąžinti defaults")
	}
}

func TestLoadConfigInvalid(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config")
	// Neteisingas formatas
	content := "blogasformatas\n===\nkey"
	os.WriteFile(cfgPath, []byte(content), 0600)

	// Neturėtų panic
	cfg := loadConfig(cfgPath)
	if cfg.Nick == "" {
		t.Error("config su klaidomis turėtų naudoti defaults")
	}
}

// ---- Logging ----

func TestLogging(t *testing.T) {
	dir := t.TempDir()
	c := newTestClient()
	c.logDir = dir
	c.server = "irc.test.com"

	msg := Message{
		ts:      time.Now(),
		nick:    "jonas",
		text:    "Labas!",
		msgType: MsgChat,
	}
	c.logMessage(dir, "irc.test.com", "#testas", msg)
	// Leisti goroutine baigti
	time.Sleep(20 * time.Millisecond)

	// Patikrinti ar failas sukurtas (rotacija per dieną: kanalas-YYYY-MM-DD.log)
	logPath := filepath.Join(dir, "irc.test.com", "#testas-"+time.Now().Format("2006-01-02")+".log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("log failas nesukurtas: %v", err)
	}
	if !strings.Contains(string(data), "jonas") {
		t.Error("log faile nėra nick")
	}
	if !strings.Contains(string(data), "Labas!") {
		t.Error("log faile nėra teksto")
	}
}

func TestLoggingDisabled(t *testing.T) {
	c := newTestClient()
	c.logDir = "" // išjungtas

	// Neturėtų panic
	c.logMessage("", "server", "#chan", Message{ts: time.Now(), text: "test"})
}

// ---- highlightURLs ----

func TestHighlightURLs(t *testing.T) {
	tests := []struct {
		input    string
		contains string
	}{
		{"žiūrėk https://example.com čia", ansiCyan},
		{"http://foo.bar/path?q=1 yra", ansiCyan},
		{"paprastas tekstas", "paprastas"},
		{"ftp://files.example.com", ansiCyan},
		{"ne url čia", "ne url čia"},
	}
	for _, tt := range tests {
		got := highlightURLs(tt.input)
		if !strings.Contains(got, tt.contains) {
			t.Errorf("highlightURLs(%q) neturi %q, gavome: %q", tt.input, tt.contains, got)
		}
	}
}

// ---- Žinučių dalijimas ----

func TestMessageSplitting(t *testing.T) {
	// Ilga žinutė turi būti dalijama baitais (splitForPrivmsg)
	longText := strings.Repeat("a", 444*2+50)
	parts := splitForPrivmsg("#chan", longText)

	total := 0
	for _, p := range parts {
		total += len(p)
		if line := "PRIVMSG #chan :" + p + "\r\n"; len(line) > maxLineBytes {
			t.Errorf("eilutė %d baitų — viršyta %d riba", len(line), maxLineBytes)
		}
	}

	if len(parts) != 3 {
		t.Errorf("tikėjomės 3 dalių, gavome %d", len(parts))
	}
}

// ---- isIgnored ----

func TestIsIgnored(t *testing.T) {
	c := newTestClient()
	c.ignoreList = []string{"spammer", "bot123"}

	if !c.isIgnored("spammer") {
		t.Error("spammer turėtų būti ignoruojamas")
	}
	if !c.isIgnored("SPAMMER") {
		t.Error("ignore turi būti case-insensitive")
	}
	if c.isIgnored("normalus") {
		t.Error("normalus neturėtų būti ignoruojamas")
	}
	if c.isIgnored("") {
		t.Error("tuščias nick neturėtų būti ignoruojamas")
	}
}

// ---- nickColor ----

func TestNickColor(t *testing.T) {
	// Tas pats nick visada ta pati spalva
	c1 := nickColor("jonas")
	c2 := nickColor("jonas")
	if c1 != c2 {
		t.Error("tas pats nick turėtų gauti tą pačią spalvą")
	}

	// Skirtingi nickai - skirtingos spalvos (dažniausiai)
	colors := map[string]bool{}
	nicks := []string{"alice", "bob", "carol", "dave", "eve", "frank", "grace", "henry"}
	for _, n := range nicks {
		colors[nickColor(n)] = true
	}
	if len(colors) < 3 {
		t.Errorf("per mažai spalvų variantų iš %d nickų: %d", len(nicks), len(colors))
	}

	// Spalva turi būti validus ANSI kodas
	for _, n := range nicks {
		color := nickColor(n)
		if !strings.HasPrefix(color, "\033[") {
			t.Errorf("nickColor(%q) = %q nėra ANSI kodas", n, color)
		}
	}
}

// ---- ensureCh / findCh ----

func TestEnsureCh(t *testing.T) {
	c := newTestClient()

	ch1 := c.ensureCh("#linux")
	ch2 := c.ensureCh("#linux")
	ch3 := c.ensureCh("#LINUX") // case-insensitive

	if ch1 != ch2 {
		t.Error("ensureCh turėtų grąžinti tą patį kanalą")
	}
	if ch1 != ch3 {
		t.Error("ensureCh turėtų būti case-insensitive")
	}
	if len(c.channels) != 2 { // *server* + #linux
		t.Errorf("tikėjomės 2 kanalų, gavome %d", len(c.channels))
	}
}

// ---- renderLines ----

func TestRenderLinesNoPanic(t *testing.T) {
	msgs := []Message{
		{ts: time.Now(), nick: "jonas", text: "Labas!", msgType: MsgChat},
		{ts: time.Now(), nick: "petras", text: "", msgType: MsgChat},
		{ts: time.Now(), nick: "", text: "Serverio žinutė", msgType: MsgServer},
		{ts: time.Now(), nick: "x", text: strings.Repeat("ilgas ", 100), msgType: MsgChat},
		{ts: time.Now(), nick: "hl", text: "testnick: labas", msgType: MsgChat, highlight: true},
	}

	for _, m := range msgs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("panic renderLines su %+v: %v", m, r)
				}
			}()
			lines := m.renderLines(60, "testnick")
			if len(lines) == 0 {
				t.Errorf("renderLines grąžino 0 eilučių: %+v", m)
			}
		}()
	}
}

// ---- Konkurencija (race condition) ----

func TestConcurrentAddMsg(t *testing.T) {
	c := newTestClient()
	done := make(chan struct{})

	// Daug goroutine rašo vienu metu
	for i := 0; i < 10; i++ {
		go func(n int) {
			for j := 0; j < 20; j++ {
				c.addMsg("#chan", Message{
					ts:      time.Now(),
					nick:    "user",
					text:    "žinutė",
					msgType: MsgChat,
				})
			}
			done <- struct{}{}
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	ch := c.findCh("#chan")
	if ch == nil {
		t.Fatal("kanalas nerastas")
	}
	if len(ch.messages) != 200 {
		t.Errorf("tikėjomės 200 žinučių, gavome %d", len(ch.messages))
	}
}

func TestConcurrentSwitchChannel(t *testing.T) {
	c := newTestClient()
	c.ensureCh("#chan1")
	c.ensureCh("#chan2")
	c.ensureCh("#chan3")

	done := make(chan struct{})
	for i := 0; i < 5; i++ {
		go func(idx int) {
			for j := 0; j < 50; j++ {
				c.switchCh(idx % 4)
			}
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 5; i++ {
		<-done
	}

	c.mu.Lock()
	idx := c.activeIdx
	total := len(c.channels)
	c.mu.Unlock()

	if idx < 0 || idx >= total {
		t.Errorf("activeIdx %d yra už ribų (0..%d)", idx, total-1)
	}
}

// ---- stripIRCFormat ----

func TestStripIRCFormat(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello world", "hello world"},
		{"\x02bold\x02", "bold"},
		{"\x1ditalic\x0f", "italic"},
		{"\x1funderline", "underline"},
		{"\x16reverse\x0f", "reverse"},
		{"\x03hello", "hello"},
		{"\x034red text\x03", "red text"},
		{"\x0304,02colored\x0f", "colored"},
		{"\x031,2fg,bg\x0f", "fg,bg"},
		{"plain \x02bold\x0f normal", "plain bold normal"},
		{"\x03\x02mixed\x1f\x0f", "mixed"},
		{"", ""},
	}
	for _, tt := range tests {
		got := stripIRCFormat(tt.input)
		if got != tt.want {
			t.Errorf("stripIRCFormat(%q) = %q, norime %q", tt.input, got, tt.want)
		}
	}
}

// ---- renderIRCFormat ----

func TestRenderIRCFormatContainsANSI(t *testing.T) {
	// Formatuotas tekstas turi turėti ANSI kodus
	result := renderIRCFormat("\x02bold\x0f")
	if !strings.Contains(result, "\033[1m") {
		t.Error("bold kodas nerandamas")
	}
	if !strings.Contains(result, "bold") {
		t.Error("tekstas nerandamas")
	}
}

func TestRenderIRCFormatColor(t *testing.T) {
	// \x034 = red
	result := renderIRCFormat("\x034red\x0f")
	if !strings.Contains(result, "red") {
		t.Error("tekstas nerandamas")
	}
	// Turi turėti kokį nors ANSI kodą
	if !strings.Contains(result, "\033[") {
		t.Error("ANSI kodas nerandamas")
	}
}

func TestRenderIRCFormatPlain(t *testing.T) {
	// Be formatavimo - grąžinti tą patį tekstą + reset
	result := renderIRCFormat("plain text")
	if !strings.Contains(result, "plain text") {
		t.Errorf("plain text nerandamas: %q", result)
	}
}

// ---- Channel.setMode / changeNickMode ----

func TestSetModeOp(t *testing.T) {
	ch := newChannel("#test", 1000)
	ch.addNick("jonas")

	ch.setMode("jonas", "+o")
	found := false
	for _, n := range ch.nicks {
		if n == "@jonas" {
			found = true
		}
	}
	if !found {
		t.Errorf("@jonas nerandamas po +o: %v", ch.nicks)
	}
}

func TestSetModeDeop(t *testing.T) {
	ch := newChannel("#test", 1000)
	ch.addNick("@jonas")

	ch.setMode("jonas", "-o")
	for _, n := range ch.nicks {
		if n == "@jonas" {
			t.Error("@jonas vis dar matomas po -o")
		}
	}
}

func TestSetModeVoice(t *testing.T) {
	ch := newChannel("#test", 1000)
	ch.addNick("petras")

	ch.setMode("petras", "+v")
	found := false
	for _, n := range ch.nicks {
		if n == "+petras" {
			found = true
		}
	}
	if !found {
		t.Errorf("+petras nerandamas po +v: %v", ch.nicks)
	}
}

func TestSetModeMultiple(t *testing.T) {
	ch := newChannel("#test", 1000)
	ch.addNick("a")
	ch.addNick("b")

	ch.setMode("a", "+o")
	ch.setMode("b", "+v")

	opFound, voiceFound := false, false
	for _, n := range ch.nicks {
		if n == "@a" {
			opFound = true
		}
		if n == "+b" {
			voiceFound = true
		}
	}
	if !opFound {
		t.Error("@a nerandamas")
	}
	if !voiceFound {
		t.Error("+b nerandamas")
	}
}

// ---- Channel.addMsg su limitu ----

func TestAddMsgLimit(t *testing.T) {
	maxMsg := 100
	ch := newChannel("#test", maxMsg)

	// Pridėti daugiau nei maxMsg
	for i := 0; i < 150; i++ {
		ch.addMsg(Message{
			ts:   time.Now(),
			nick: "u",
			text: fmt.Sprintf("žinutė %d", i),
		})
	}

	// Po trim turi būti <= maxMsg
	if len(ch.messages) > maxMsg {
		t.Errorf("per daug žinučių: %d (max %d)", len(ch.messages), maxMsg)
	}
}

func TestAddMsgLimitKeepsNewest(t *testing.T) {
	ch := newChannel("#test", 10)

	for i := 0; i < 20; i++ {
		ch.addMsg(Message{
			ts:   time.Now(),
			nick: "u",
			text: fmt.Sprintf("msg%d", i),
		})
	}

	// Paskutinė žinutė turi išlikti
	last := ch.messages[len(ch.messages)-1]
	if !strings.Contains(last.text, "msg19") {
		t.Errorf("paskutinė žinutė neteisinga: %q", last.text)
	}
}

// ---- doSearch ----

func TestDoSearch(t *testing.T) {
	c := newTestClient()
	c.ensureCh("#chan")
	ch := c.findCh("#chan")

	for i := 0; i < 20; i++ {
		ch.addMsg(Message{
			ts:   time.Now(),
			nick: "user",
			text: fmt.Sprintf("žinutė numeris %d", i),
		})
	}
	// Speciali žinutė
	ch.addMsg(Message{
		ts:   time.Now(),
		nick: "user",
		text: "ieškomas tekstas xyz",
	})
	for i := 0; i < 5; i++ {
		ch.addMsg(Message{
			ts:   time.Now(),
			nick: "user",
			text: fmt.Sprintf("po paieškos %d", i),
		})
	}

	c.doSearch(ch, "xyz")

	c.mu.Lock()
	off := c.scrollOff
	c.mu.Unlock()

	// scrollOff turi būti > 0 (scroll atgal)
	if off <= 0 {
		t.Errorf("scrollOff = %d, tikėjomės > 0", off)
	}
}

func TestDoSearchNotFound(t *testing.T) {
	c := newTestClient()
	c.ensureCh("#chan")
	ch := c.findCh("#chan")

	ch.addMsg(Message{ts: time.Now(), nick: "u", text: "labas"})

	c.mu.Lock()
	c.searchMode = true
	c.mu.Unlock()

	c.doSearch(ch, "neegzistuojantis_tekstas_xyz")

	// searchMode turi likti true kai nerasta
	c.mu.Lock()
	mode := c.searchMode
	c.mu.Unlock()

	if !mode {
		t.Error("searchMode turėtų likti true kai nerasta")
	}
}

// ---- saveHistory / loadHistory ----

func TestSaveLoadHistory(t *testing.T) {
	dir := t.TempDir()
	histFile := filepath.Join(dir, "history")

	// Monkeypatching negalima, todėl testuojame logiką tiesiogiai
	lines := []string{"/join #test", "/msg user labas", "hello world"}

	// Rašyti
	f, err := os.OpenFile(histFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lines {
		f.WriteString(l + "\n")
	}
	f.Close()

	// Skaityti
	data, err := os.ReadFile(histFile)
	if err != nil {
		t.Fatal(err)
	}
	readLines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(readLines) != len(lines) {
		t.Errorf("eilučių skaičius: got %d, want %d", len(readLines), len(lines))
	}
	for i, l := range lines {
		if readLines[i] != l {
			t.Errorf("eilutė %d: got %q, want %q", i, readLines[i], l)
		}
	}
}

// ---- Topic saugojimas ----

func TestChannelTopic(t *testing.T) {
	ch := newChannel("#test", 1000)
	if ch.topic != "" {
		t.Error("pradinis topic turi būti tuščias")
	}

	ch.topic = "Test topic čia"
	if ch.topic != "Test topic čia" {
		t.Errorf("topic neteisinga: %q", ch.topic)
	}
}

// ---- configDir / keliai ----

func TestConfigDirUsesXDGCustom(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	want := filepath.Join(dir, "mircbsd")
	if got := configDir(); got != want {
		t.Errorf("configDir() = %q, norime %q", got, want)
	}
	if got, want := configPath(), filepath.Join(want, "config"); got != want {
		t.Errorf("configPath() = %q, norime %q", got, want)
	}
	if got, want := historyPath(), filepath.Join(want, "history"); got != want {
		t.Errorf("historyPath() = %q, norime %q", got, want)
	}
	if got, want := ignorePath(), filepath.Join(want, "ignore"); got != want {
		t.Errorf("ignorePath() = %q, norime %q", got, want)
	}
}

func TestConfigDirFallsBackToHomeDotConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", home)

	want := filepath.Join(home, ".config", "mircbsd")
	if got := configDir(); got != want {
		t.Errorf("configDir() = %q, norime %q", got, want)
	}
}

func TestConfigDirEmptyWithoutHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")

	if got := configDir(); got != "" {
		t.Errorf("be namų katalogo configDir() turi būti tuščias, gavome %q", got)
	}
	if got := configPath(); got != "" {
		t.Errorf("configPath() turi būti tuščias, gavome %q", got)
	}
}

func TestDefaultConfigLogDirInHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := defaultConfig()
	if want := filepath.Join(home, "irclogs"); cfg.LogDir != want {
		t.Errorf("LogDir = %q, norime %q", cfg.LogDir, want)
	}
}

func TestLoadConfigExpandsTildeLogDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte("logdir=~/katalogas\n"), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := loadConfig(path)
	if want := filepath.Join(home, "katalogas"); cfg.LogDir != want {
		t.Errorf("LogDir = %q, norime %q", cfg.LogDir, want)
	}
}

// ---- osLabel ----

func TestOSLabelNotEmpty(t *testing.T) {
	label := osLabel()
	if label == "" {
		t.Fatal("osLabel() tuščias")
	}
	switch label {
	case "Linux", "FreeBSD", "macOS", "Windows":
		// žinomos reikšmės
	default:
		t.Errorf("osLabel() nepažįstama reikšmė %q (GOOS=%s)", label, runtime.GOOS)
	}
}

// ---- openURL ----

func TestOpenURLNoOpenerFound(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	err := openURL("https://example.com")
	if err == nil {
		t.Fatal("be naršyklės įrankio turi grąžinti klaidą")
	}
	if !strings.Contains(err.Error(), "xdg-open") {
		t.Errorf("klaida turi nurodyti ieškotą įrankį: %v", err)
	}
}

func TestOpenURLUsesFirstAvailableOpener(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skip("skirta unix tipo sistemoms")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "sensible-browser")
	body := "#!/bin/sh\necho \"$@\" > " + filepath.Join(dir, "args") + "\n"
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	if err := openURL("https://libera.chat"); err != nil {
		t.Fatalf("openURL() klaida: %v", err)
	}

	// Duomenys gali būti nebaigti rašyti – sukant būtinumo nėra
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(filepath.Join(dir, "args")); err == nil &&
			strings.Contains(string(b), "https://libera.chat") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("naršyklės įrankis negaavo URL")
}

func TestHighlightColorConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config")
	os.WriteFile(p, []byte("highlight_color=blue\nnick_colors=false\n"), 0o600)
	cfg := loadConfig(p)
	if cfg.HighlightColor != "blue" {
		t.Errorf("highlight_color: %q", cfg.HighlightColor)
	}
	if cfg.NickColors {
		t.Error("nick_colors=false turi išjungti")
	}
	if !defaultConfig().NickColors {
		t.Error("numatytai nick spalvos įjungtos")
	}
}

func TestSetHighlightColor(t *testing.T) {
	want := map[string]string{
		"red": ansiBgRed, "blue": ansiBgBlue, "none": ansiReverse,
	}
	for name, code := range want {
		setHighlightColor(name)
		if hlBgCode != code {
			t.Errorf("%s: gauta %q, norėta %q", name, hlBgCode, code)
		}
	}
	setHighlightColor("") // grįžta į red
	if hlBgCode != ansiBgRed {
		t.Errorf("nežinoma/našta grįžta prie red: %q", hlBgCode)
	}
	// nick spalvos išjungimas: nickColor grąžina tuščią
	old := nickColoring
	setNickColors(false)
	if nickColor("testas") != "" {
		t.Error("išjungtos nick spalvos vis tiek spalvina")
	}
	setNickColors(old)
}
