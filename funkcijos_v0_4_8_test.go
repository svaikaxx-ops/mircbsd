package main

// Funkcijos v0.4.8 (paketas „b“) regresijos testai:
//
//  1. SASL EXTERNAL — mechanizmo pasirinkimas (EXTERNAL / PLAIN),
//     AUTHENTICATE atsakymas „+“ be payload'o (slaptažodis NENUNEKAMAS).
//  2. Paieška: n (pirmyn) / p (atgal) su tuščia įvestimi; su tekstu —
//     „n“/„p“ LIEKA raide.
//  3. /search all — iš VISŲ langų, iki 50.
//  4. /logsearch — ieško failą, path traversal atmetamas.
//  5. /set — gyvas keitimas + įrašymas į config (kitos eilutės lieka);
//     nekeičiamas raktas (sasl) atmetamas; bloga reikšmė neĮRAŠOMA.
//  6. /exportjson — failas su ts/nick/type/text; keliai be traversal.
//  7. Tema — light/dark perjungia nick paletes.
//  8. MONITOR — 730/731 numerikai, /notify add/del, MONITOR + po 001.
//  9. Keys failas — perrašo seką į kanoninę, ciklas saugus, bloga eilutė.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testServerConnFake — fake išsiuntimų kanalas /set SASL testams.
func setupFakeConn(c *Client) func() []string {
	c.writeCh = make(chan string, 64)
	c.mu.Lock()
	c.connected = true
	c.conn = nil // send() neturi tiesioginio writeCh — test per writeCh imam
	c.mu.Unlock()
	return nil
}

func drainWriteCh(c *Client, n int) []string {
	var lines []string
	timeout := time.After(2 * time.Second)
	for len(lines) < n {
		select {
		case l := <-c.writeCh:
			lines = append(lines, l)
		case <-timeout:
			return lines
		}
	}
	return lines
}

// T1a — SASL EXTERNAL: derybos renka EXTERNAL mechanizmą (su cert).
func TestSaslExternalMechanismChosen(t *testing.T) {
	c := newTestClient()
	c.writeCh = make(chan string, 16)
	c.mu.Lock()
	c.saslExternal = true
	c.tlsCertPath = "/tmp/cert.pem"
	c.tlsKeyPath = "/tmp/key.pem"
	c.useTLS = true // EXTERNAL be TLS nesuveikia (be sertifikato)
	c.mu.Unlock()

	c.parseIRC(":srv CAP * LS :sasl")

	req := ""
	select {
	case l := <-c.writeCh:
		req = l
	case <-time.After(2 * time.Second):
		t.Fatal("CAP REQ neišsiųsta")
	}
	if !strings.Contains(req, "sasl") {
		t.Fatalf("BUG: sasl cap'as neprašytas: %q", req)
	}

	c.parseIRC(":srv CAP * ACK :sasl")
	lines := drainWriteCh(c, 1)
	if len(lines) != 1 {
		t.Fatalf("gautos %d eilutės", len(lines))
	}
	if strings.TrimSuffix(lines[0], "\n") != "AUTHENTICATE EXTERNAL" {
		t.Errorf("BUG: mechanizmas %q (norėta AUTHENTICATE EXTERNAL)", lines[0])
	}

	// Serveris atsako AUTHENTICATE + — EXTERNAL NETURI siųsti payload'o.
	c.parseIRC("AUTHENTICATE +")
	lines = drainWriteCh(c, 1)
	if len(lines) != 1 {
		t.Fatalf("AUTHENTICATE+: gautos %d eilutės", len(lines))
	}
	if strings.TrimSuffix(lines[0], "\n") != "AUTHENTICATE +" {
		t.Errorf("BUG: EXTERNAL atsakymas %q (turėtų būti tuščias „+“ — be payload)", lines[0])
	}
}

// T1b — SASL PLAIN toliau siunčia payload'o (mechanizmo atsirinkimas).
func TestSaslPlainStillSendsPayload(t *testing.T) {
	c := newTestClient()
	c.writeCh = make(chan string, 16)
	c.mu.Lock()
	c.saslPass = "slaptazodis"
	c.mu.Unlock()

	c.parseIRC(":srv CAP * LS :sasl")
	drainWriteCh(c, 1) // CAP REQ
	c.parseIRC(":srv CAP * ACK :sasl")
	lines := drainWriteCh(c, 1)
	if len(lines) != 1 || !strings.Contains(lines[0], "AUTHENTICATE PLAIN") {
		t.Fatalf("BUG: ne AUTHENTICATE PLAIN: %q", lines)
	}
	c.parseIRC("AUTHENTICATE +")
	lines = drainWriteCh(c, 1)
	if len(lines) != 1 {
		t.Fatalf("gautos %d eilutės", len(lines))
	}
	if strings.TrimSuffix(lines[0], "\n") == "AUTHENTICATE +" {
		t.Errorf("BUG: PLAIN tuščias atsakymas — payload dingsta")
	}
	if !strings.Contains(lines[0], "AUTHENTICATE ") || strings.Contains(lines[0], "slaptazodis") {
		// Payload turi BŪTI base64 — ne plaintext.
		if !strings.Contains(lines[0], "AUTHENTICATE ") {
			t.Errorf("BUG: ne AUTHENTICATE payload: %q", lines[0])
		}
		if strings.Contains(lines[0], "slaptazodis") {
			t.Errorf("BUG: payload'as NEbase64 (slaptažodis atviroje eilutėje)")
		}
	}
}

// T1c — sasl_external BE TLS/serificiato: SASL neprašomas (tuščias PLAIN
// payload'as serveriui nieko neduoda — geriau visai be SASL).
func TestSaslExternalWithoutTLSNotRequested(t *testing.T) {
	c := newTestClient()
	c.writeCh = make(chan string, 16)
	c.mu.Lock()
	c.saslExternal = true // be tls_cert, be useTLS, be saslPass
	c.mu.Unlock()

	c.parseIRC(":srv CAP * LS :sasl")
	select {
	case l := <-c.writeCh:
		if strings.Contains(strings.TrimSuffix(l, "\n"), "sasl") {
			t.Errorf("BUG: sasl prašytas ir be sertifikato/TLS: %q", l)
		}
	case <-time.After(500 * time.Millisecond):
		// Nieko negauta galima (CAP END su siuntimu — ir tai gerai), čia
		// tik patikriname, kad NĖRA sasl prašymo.
	}
}

// sendTestKey — siunčia klavišo seką per handleKey.
func sendTestKey(c *Client, b []byte) { c.handleKey(b) }

// T2 — n/p paieškos navigacija: su TUŠČIA įvestimi veikia, su tekstu — raides.
func TestSearchNavEmptyInputOnly(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":testnick!u@h JOIN #chanas")
	c.addMsg("#chanas", Message{ts: time.Now(), nick: "bob", text: "žemiau labas"})
	c.addMsg("#chanas", Message{ts: time.Now().Add(time.Second), nick: "bob", text: "aukščiau labas dar"})

	// Pirmoji paieška — pats „p“ elgesys: atgal (randa PASKILTI pavyzdį —
	// antrą). „n“ paskui tai — pirmyn (randa sekančius; čia — NĖRA
	// sekančio — nieko keičia; indeksas lieka paskutinio)
	c.mu.Lock()
	c.lastSearchQuery = ""
	c.mu.Unlock()
	c.doSearch(c.findCh("#chanas"), "labas")
	c.mu.Lock()
	idxAfterP := c.searchResultIdx
	c.mu.Unlock()
	if idxAfterP < 0 {
		t.Fatal("sąranka: pirmosios paieškos rezultato nėra")
	}

	// „p“ pakartotinai su ta pačia užklausa — ieško atgal (anksčiau
	// buvusio rezultato). doSearch su ta pačia užklausa tęsia atgal.
	c.doSearch(c.findCh("#chanas"), "labas")
	c.mu.Lock()
	idxAfterP2 := c.searchResultIdx
	c.mu.Unlock()
	if idxAfterP2 >= idxAfterP {
		t.Errorf("BUG: „p“ nesurado ankstesnio indekso (%d → %d)", idxAfterP, idxAfterP2)
	}

	// „n“ paskui tai — pirmyn (gali grįžt į tą pat arba kito).
	sendTestKey(c, []byte{'n'})
	c.mu.Lock()
	idxAfterN := c.searchResultIdx
	empty := len(c.input)
	c.mu.Unlock()
	if idxAfterN < 0 {
		t.Errorf("BUG: „n“ išvalomų rezultatų indeksą (-1)")
	}
	if empty != 0 {
		t.Errorf("BUG: „n“ neveikiant su įvestimi — įvestis pilama (%d)", empty)
	}

	// SU TEKSTU — „n“ turi LIEKA raide (negalima paieškos navigacija, kai
	// rašoma žinutė prasideda „n“).
	c.mu.Lock()
	c.input = []rune("nauji pranešimai vartotoju")
	c.inputPos = len(c.input)
	c.lastSearchQuery = "labas"
	c.mu.Unlock()
	sendTestKey(c, []byte{'n'})
	c.mu.Lock()
	got := string(c.input)
	c.input = nil
	c.mu.Unlock()
	if !strings.HasPrefix(got, "n") {
		t.Errorf("BUG: su tekstu „n“ nėra raidė apdorojama (įvestis %q)", got)
	}
}

// T3 — /search all: visuose languose, iki 50 atitikmenų.
func TestSearchAllChannels(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":testnick!u@h JOIN #pirmas")
	c.parseIRC(":testnick!u@h JOIN #antras")
	c.addMsg("#pirmas", Message{ts: time.Now(), nick: "bob", text: "specialaus sakinys"})
	c.addMsg("#antras", Message{ts: time.Now(), nick: "alice", text: "kita tema"})
	c.addMsg("*server*", Message{ts: time.Now(), text: "sistemos žinutė specialaus"})

	c.handleCmd("search", "all specialaus")
	sh := c.findCh("*server*")
	if sh == nil {
		t.Fatal("*server* langas išnyko")
	}
	var found int
	c.mu.Lock()
	for _, m := range sh.messages {
		if strings.Contains(m.text, "specialaus") {
			found++
		}
	}
	c.mu.Unlock()
	// Dvi žinutės su „specialaus“ (kanale #pirmas ir *server*) + save
	// paieškos pranešimas. Mažiausiai 2 turėtų būti.
	if found < 2 {
		t.Errorf("BUG: /search all rado %d žinute (norėta ≥2)", found)
	}
	// Kanalas #antras neturi „specialaus“ — įsitikinti, kad jis nebuvo iš
	// kito lango.
	c.mu.Lock()
	wrongWindow := false
	for _, m := range sh.messages {
		if strings.Contains(m.text, "specialaus") && strings.Contains(m.text, "kita tema") {
			wrongWindow = true
		}
	}
	c.mu.Unlock()
	if wrongWindow {
		t.Error("BUG: atitikmenys iš #antras (kuriame nėra „specialaus“)")
	}
}

// T4 — /logsearch ir /exportjson turi tą patį testinį log katalogą.
func setupLogDir(t *testing.T) (string, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "mircbsd-logs")
	if err != nil {
		t.Fatal(err)
	}
	srvdir := filepath.Join(dir, "test.server")
	if err := os.MkdirAll(srvdir, 0700); err != nil {
		t.Fatal(err)
	}
	logContent := "2026-10-22 10:00:00 <bob> slaptas paslapties žodis\n" +
		"2026-10-22 11:00:00 <alice> kita žinutė\n"
	if err := os.WriteFile(filepath.Join(srvdir, "#chanas-2026-10-22.log"), []byte(logContent), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srvdir, "..-evil-2026-10-22.log"), []byte("traversal\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return dir, func() { os.RemoveAll(dir) }
}

func newTestClientWithLog(t *testing.T) (*Client, func()) {
	t.Helper()
	dir, cleanup := setupLogDir(t)
	c := newTestClient()
	c.logDir = dir
	return c, cleanup
}

// T4 — /logsearch ieško žurnalo faile; atsakymai yra *server* lange.
func TestLogSearchFindsAndFilters(t *testing.T) {
	c, cleanup := newTestClientWithLog(t)
	defer cleanup()
	// serverio katalogo pavadinimas logDir/‹serveris› — sutampa su
	// setupLogDir sukurtu „test.server“.
	c.server = "test.server"

	c.handleCmd("logsearch", "#chanas paslapties")
	sh := c.findCh("*server*")
	c.mu.Lock()
	found := false
	for _, m := range sh.messages {
		if strings.Contains(m.text, "slaptas paslapties žodis") {
			found = true
		}
	}
	c.mu.Unlock()
	if !found {
		t.Error("BUG: /logsearch žinutės neranda žurnale")
	}

	// Be kanalo filtrą — visi failai šiame serverio kataloge.
	c.handleCmd("logsearch", "kita")
	c.mu.Lock()
	found2 := false
	for _, m := range sh.messages {
		if strings.Contains(m.text, "kita žinutė") {
			found2 = true
		}
	}
	c.mu.Unlock()
	if !found2 {
		t.Error("BUG: /logsearch be filtro neranda kitos žinutės")
	}

	// Nežinomas kanalas — NEIEŠKOMA (be klaidą failo glob'ą už kitu
	// katalogu — sanitizePath apdoroja).
	c.mu.Lock()
	before := len(sh.messages)
	c.mu.Unlock()
	c.handleCmd("logsearch", ".. traversal")
	c.mu.Lock()
	after := len(sh.messages)
	c.mu.Unlock()
	if after-before > 3 {
		t.Errorf("BUG: „..“ filtrą grąžino rezultatus (%d naujų)", after-before)
	}
}

// T5 — Message JSON: ts RFC3339, sanitized text, type.
func TestMessageMarshalJSON(t *testing.T) {
	m := Message{
		ts:      time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC),
		nick:    "bob",
		text:    "\x031,2spalvota\x0f žinutė \x1b]0;hacked\x07",
		msgType: MsgChat,
	}
	buf, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(buf, &v); err != nil {
		t.Fatal(err)
	}
	if v["ts"] != "2026-10-22T10:00:00Z" {
		t.Errorf("BUG: ts ne RFC3339 UTC: %v", v["ts"])
	}
	if !strings.Contains(v["text"].(string), "spalvota") {
		t.Errorf("BUG: tekstas dingti JSON'o: %q", v["text"])
	}
	if s := v["text"].(string); strings.Contains(s, "hacked\x07") || strings.Contains(s, "\x03") {
		t.Errorf("BUG: text nesant sanatizuotas: %q", s)
	}
	if v["type"] != "chat" {
		t.Errorf("BUG: type ne „chat“: %v", v["type"])
	}
}

// T6 — /exportjson: failas su visom žinutėm; katalogas neperduoda.
func TestExportJSONWritesFile(t *testing.T) {
	c, cleanup := newTestClientWithLog(t)
	defer cleanup()
	c.parseIRC(":testnick!u@h JOIN #chanas")
	c.addMsg("#chanas", Message{ts: time.Now(), nick: "bob", text: "labas į eksportą", msgType: MsgChat})

	c.handleCmd("exportjson", "#chanas")

	// Failas atsiras tame pačiam serverio kataloge (c.server tuščias
	// testuose → tiesiog logDir).
	sh := c.findCh("*server*")
	c.mu.Lock()
	var path string
	for _, m := range sh.messages {
		if strings.Contains(m.text, "→") && strings.Contains(m.text, ".json") {
			path = strings.TrimSpace(m.text[strings.Index(m.text, "→")+len("→"):])
		}
	}
	c.mu.Unlock()
	if path == "" {
		t.Fatal("BUG: eksporto pranešimas be kelio")
	}
	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("BUG: eksporto failas nesukurtas (%s): %v", path, err)
	}
	var msgs []map[string]any
	if err := json.Unmarshal(buf, &msgs); err != nil {
		t.Fatalf("BUG: failas nėra JSON: %v", err)
	}
	var foundText bool
	for _, m := range msgs {
		if s, ok := m["text"].(string); ok && strings.Contains(s, "labas") {
			foundText = true
		}
	}
	if !foundText {
		t.Error("BUG: eksportuotame JSON'e ne žinutės teksto")
	}
}

// T7 — tema: light/dark perjungia nick paletes.
func TestSetThemeSwapsPalettes(t *testing.T) {
	defer setTheme("dark")
	dark := nickColors
	setTheme("light")
	if &nickColors[0] == &dark[0] {
		t.Error("BUG: theme=light nepakeitė paletes")
	}
	setTheme("dark")
	if &nickColors[0] != &nickColorsDark[0] {
		t.Error("BUG: theme=dark negryžta į numatytą paletę")
	}
}

// T8 — MONITOR: 001 siunčia MONITOR +; 730/731 atnaujinti būseną.
func TestMonitorFlow(t *testing.T) {
	c := newTestClient()
	c.writeCh = make(chan string, 16)
	c.mu.Lock()
	c.monitorList = []string{"bobas", "alice"}
	c.mu.Unlock()

	c.parseIRC(":srv 001 nick :Welcome")
	lines := drainWriteCh(c, 1)
	if len(lines) == 0 || !strings.Contains(strings.Join(lines, " "), "MONITOR + bobas,alice") {
		t.Fatalf("BUG: 001 nesiuntė MONITOR sąrašo: %q", lines)
	}

	// 730 — bobas prisijungė.
	c.parseIRC(":srv 730 nick :bobas!u@host")
	c.mu.Lock()
	on := c.monitorOnline["bobas"]
	c.mu.Unlock()
	if !on {
		t.Error("BUG: 730 nepažymėjo bobas online")
	}

	// 731 — bobas atsijungė.
	c.parseIRC(":srv 731 nick :bobas!u@host")
	c.mu.Lock()
	_, stillOn := c.monitorOnline["bobas"]
	c.mu.Unlock()
	if stillOn {
		t.Error("BUG: 731 neištrynė bobas")
	}

	// 730 — keli tuo pačiu laiką.
	c.parseIRC(":srv 730 nick :a!1@2,b!3@4")
	c.mu.Lock()
	a := c.monitorOnline["a"]
	b := c.monitorOnline["b"]
	c.mu.Unlock()
	if !a || !b {
		t.Errorf("BUG: 730 keli nick'ai (a=%v, b=%v)", a, b)
	}

	// 734 — nepriimti nick'ai šalinami sąraše (kitu atveju jie liktų
	// „stebimi“ nieko nepranešant). Formatas: <limit> <maskos> :<priežastis>
	// → nick'ų sąrašas yra p(2) (p(1) yra skaičius 300 — seniau
	// atmetinėjome jį ir NIEKAS nebūtų pašalinta).
	c.mu.Lock()
	c.monitorList = []string{"liko", "toonas"}
	c.mu.Unlock()
	c.parseIRC(":srv 734 nick 300 toonas!u@h :priezastis")
	c.mu.Lock()
	keptToonas := false
	keptLiko := false
	for _, n := range c.monitorList {
		if strings.EqualFold(n, "toonas") {
			keptToonas = true
		}
		if strings.EqualFold(n, "liko") {
			keptLiko = true
		}
	}
	c.mu.Unlock()
	if keptToonas {
		t.Error("BUG: 734 nepašalino nicko (p(2) nick'ai)")
	}
	// „liko“ neturi būti pašalintas — 734 šalina tik nepriimtus.
	if !keptLiko {
		t.Error("BUG: 734 pašalino kitą nick'ą („liko“) iš sąrašo")
	}
}

// T9 — /notify add/del valdo sąrašą ir siunčia MONITOR ±.
func TestNotifyAddDel(t *testing.T) {
	c := newTestClient()
	c.writeCh = make(chan string, 16)

	c.handleCmd("notify", "add zmones kitas")
	lines := drainWriteCh(c, 1)
	if len(lines) == 0 || !strings.Contains(lines[0], "MONITOR + ") {
		t.Fatalf("BUG: /notify add nesiuntė MONITOR +: %q", lines)
	}
	c.mu.Lock()
	n := len(c.monitorList)
	c.mu.Unlock()
	if n != 2 {
		t.Errorf("BUG: sąraše %d (norėta 2)", n)
	}

	// Nevalidus nick (su @) — atmetamas (be MONITOR siuntimo ir be sąrašo).
	c.handleCmd("notify", "add b@d")
	c.mu.Lock()
	n2 := len(c.monitorList)
	c.mu.Unlock()
	if n2 != 2 {
		t.Errorf("BUG: nevalidus nick pridėtas (%d)", n2)
	}

	c.handleCmd("notify", "del zmones")
	c.mu.Lock()
	var hasZmones bool
	for _, name := range c.monitorList {
		if strings.EqualFold(name, "zmones") {
			hasZmones = true
		}
	}
	c.mu.Unlock()
	if hasZmones {
		t.Error("BUG: /notify del nepašalino")
	}
}

// T10 — keys failas: perrašymas veikia, ciklas saugus, blogos eilutės.
func TestKeyBindingsOverride(t *testing.T) {
	c := newTestClient()
	// „\\x73“ = 's' — remap 's' į paiešką (keys failo sintaksė \x73).
	c.mu.Lock()
	c.customBinds = map[string]string{"s": "search"}
	c.mu.Unlock()
	sendTestKey(c, []byte{'s'})
	c.mu.Lock()
	searchMode := c.searchMode
	c.mu.Unlock()
	if !searchMode {
		t.Error("BUG: keys perrašymas nepaleido paieškos (search)")
	}

	// Ciklo apsauga: veiksmas remapintas į SAVO PATIES seką — negali
	// įstrigti (ir įprastas apdorojimas toliau vyksta: → paieška).
	c.mu.Lock()
	c.searchMode = false
	c.customBinds = map[string]string{"\x12": "search"}
	c.mu.Unlock()
	sendTestKey(c, []byte{0x12})
	c.mu.Lock()
	cycleMode := c.searchMode
	c.searchMode = false
	c.customBinds = nil
	c.mu.Unlock()
	if !cycleMode {
		t.Error("BUG: savo sekos remap ciklas pertraukė apdorojimą")
	}

	// „n“ su TEKSTU turi likti raide (navigacija tik su tuščia įvestimi).
	c.mu.Lock()
	c.input = []rune("nauji pranešimai")
	c.inputPos = len(c.input)
	c.lastSearchQuery = "labas"
	c.mu.Unlock()
	sendTestKey(c, []byte{'n'})
	c.mu.Lock()
	gotInput := string(c.input)
	c.input = nil
	c.mu.Unlock()
	if !strings.HasPrefix(gotInput, "n") {
		t.Errorf("BUG: „n“ su tekstu neturėjo pereiti į navigaciją (%q)", gotInput)
	}

	// Blogos eilutės failas — įspėjimai, perrašymas neskaitomas.
	dir, err := os.MkdirTemp("", "mircbsd-keys")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "keys")
	content := "# komentaras\n" +
		"chan-next = \\x0f\n" +
		"nezinomas_veiksmas = \\ea\n" +
		"search = \\xZZ\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	binds, warns := parseKeysFile(path)
	if _, ok := binds["\x0f"]; !ok {
		t.Error("BUG: chan-next perrašymas neskaitytas iš failo")
	}
	if len(warns) != 2 {
		t.Errorf("BUG: įspėjimai = %d (norėta 2: nežinomas veiksmas + nevalidus \\x): %v", len(warns), warns)
	}
}

// P1 — peržiūra 2026-10-22: monitorOnline raktas FOLDINTAS (serveris gali
// grąžinti "BOBAS", o sąraše — "bobas").
func TestMonitorOnlineFolded(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	c.monitorList = []string{"bobas"}
	c.mu.Unlock()
	c.parseIRC(":srv 730 nick :BOBAS!u@host")
	c.mu.Lock()
	on := c.monitorOnline["bobas"]
	c.mu.Unlock()
	if !on {
		t.Error("BUG: 730 su kita raide (BOBAS) nepažymėjo bobas online (raktas nefoldintas)")
	}
}

// P2 — /notify add apsiriboja serverio MONITOR=N limitu (005).
func TestNotifyMonitorLimitCap(t *testing.T) {
	c := newTestClient()
	c.writeCh = make(chan string, 16)
	c.parseIRC(":srv 005 nick MONITOR=2 :are supported")
	c.handleCmd("notify", "add a b c")
	c.mu.Lock()
	n := len(c.monitorList)
	c.mu.Unlock()
	if n != 2 {
		t.Errorf("BUG: limitas MONITOR=2 neveikia (sąraše %d)", n)
	}
}

// P3 — /search all su kitom raidėm („ALL“) ir „/search all“ be tarpo =
// įprasta paieška žodžiui „all“.
func TestSearchAllWordParsing(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":testnick!u@h JOIN #k")
	c.addMsg("#k", Message{ts: time.Now(), nick: "bob", text: "visi yra all čia"})

	// „ALL“ didžiosiomis — vis tiek visų langų paieška.
	c.handleCmd("search", "ALL visi")
	sh := c.findCh("*server*")
	c.mu.Lock()
	found := false
	for _, m := range sh.messages {
		if strings.Contains(m.text, "visi") {
			found = true
		}
	}
	c.mu.Unlock()
	if !found {
		t.Error("BUG: /search ALL (didžiosiomis) neveikia")
	}

	// „/search all“ be tarpo — ĮPRASTA paieška žodžio „all“ aktyviame
	// kanale (seniau „all“ buvo sugertas kaip „all“ rėžimas ir žodžio
	// „all“ ieškoti negalėta). doSearch pranešimo nerašo — tikrina būseną.
	c.handleCmd("search", "all")
	c.mu.Lock()
	idx := c.searchResultIdx
	c.mu.Unlock()
	if idx < 0 {
		t.Errorf("BUG: /search all (be tarpo) nerado žodžio „all“ (idx %+d)", idx)
	}
}

// P4 — logsearch: atitikmenys TIK žinutės tekste (be laiko žymos).
func TestLogSearchBodyOnly(t *testing.T) {
	dir, err := os.MkdirTemp("", "mircbsd-logbody")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	srvdir := filepath.Join(dir, "test.server")
	os.MkdirAll(srvdir, 0700)
	content := "[12:34:56] <bob> labas pasauli\n"
	if err := os.WriteFile(filepath.Join(srvdir, "#k-2026-10-22.log"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	c := newTestClient()
	c.logDir = dir
	c.server = "test.server"

	// „12:34“ yra LAIKO ŽYMOJE — neturi atitikti (seniau atitiko:
	// ieškojome visoje eilutėje).
	c.handleCmd("logsearch", "12:34")
	sh := c.findCh("*server*")
	c.mu.Lock()
	timestampHit := false
	for _, m := range sh.messages {
		if strings.Contains(m.text, ".log> ") {
			timestampHit = true
		}
	}
	c.mu.Unlock()
	if timestampHit {
		t.Error("BUG: laiko žyma („12:34“) atitiko paiešką")
	}

	// Pats tekstas — atitinka.
	c.handleCmd("logsearch", "pasauli")
	c.mu.Lock()
	found := false
	for _, m := range sh.messages {
		if strings.Contains(m.text, ".log> ") && strings.Contains(m.text, "labas pasauli") {
			found = true
		}
	}
	c.mu.Unlock()
	if !found {
		t.Error("BUG: logsearch nebetiko žinutės teksto")
	}
}

// P5 — logsearch filtre glob'o simbolių rinkinių atslinkimas: kanalas
// „#[t]est“ turi atitikti SAVO failą, o ne naudoti „[t]“ kaip rinkinį
// (seniau glob'e „[t]“ reiškė vieną iš t/e/... — failas „#[t]est-...“
// tyliai nesurandamas, o „#test-...“ atsiranda be savavališkos priežasties).
func TestLogSearchGlobQuote(t *testing.T) {
	dir, err := os.MkdirTemp("", "mircbsd-logglob")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	srvdir := filepath.Join(dir, "test.server")
	os.MkdirAll(srvdir, 0700)
	os.WriteFile(filepath.Join(srvdir, "#[t]est-2026-10-22.log"), []byte("[10:00:00] <bob> globas"), 0600)
	os.WriteFile(filepath.Join(srvdir, "#test-2026-10-22.log"), []byte("[10:00:00] <bob> kitas"), 0600)
	c := newTestClient()
	c.logDir = dir
	c.server = "test.server"

	c.handleCmd("logsearch", "#[t]est globas")
	sh := c.findCh("*server*")
	c.mu.Lock()
	// TIK tikri atitikmenys („.log> “ eilutė) — klaidos pranešimas
	// „nieko nerasta: globas“ irgi turi tą žodį.
	found, wrong := false, false
	for _, m := range sh.messages {
		if strings.Contains(m.text, ".log> ") && strings.Contains(m.text, "globas") {
			found = true
		}
		if strings.Contains(m.text, ".log> ") && strings.Contains(m.text, "kitas") {
			wrong = true
		}
	}
	c.mu.Unlock()
	if !found {
		t.Error("BUG: „#[t]est“ savo failo nesurasta (glob rinkinys „[t]“)")
	}
	if wrong {
		t.Error("BUG: „#[t]est“ filtras paėmė „#test“ failą (glob rinkinys)")
	}
}

// P6 — /set bool su nežinoma reikšme („taip“) — KLAIDA, be įrašo į
// config (seniau bet koks neatpažintas žodis tyliai reikšdavo false ir
// tą melą įrašydavo į failą).
func TestSetBoolUnknownValue(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", t.TempDir())

	c := newTestClient()
	// „on“ — dabar PRIIMTAS (true bool'as, kaip loadConfig senovėje buvo
	// priimamas tik true/1/yes — čia platesnis žodynas /set'ui).
	c.handleCmd("set", "timestamps on")
	if !timestampsEnabled {
		t.Error("BUG: /set timestamps on nepritaikė true")
	}

	// Nežinomas žodis — klaida: reikšmė NEKEIČIAMA ir NEĮRAŠOMA į failą.
	c.handleCmd("set", "timestamps taip")
	if !timestampsEnabled {
		t.Error("BUG: nežinoma reikšmė pakeitė nustatymą į false")
	}

	// Į failą „taip“ NĖRA įrašyta (ir „timestamps=on“ ten gyvena tik iš
	// pirmosios komandos rašymo).
	p := configPath()
	if buf, err := os.ReadFile(p); err != nil {
		t.Fatalf("config failo nėra: %v", err)
	} else if strings.Contains(string(buf), "taip") {
		t.Error("BUG: nežinoma reikšmė („taip“) įrašyta į config failą")
	}
}

// P7 — escapeKeySeq: tarpas → \\x20 (keys failo apvalinimui TrimSpace
// nepašalintų jo).
func TestEscapeKeySeqSpace(t *testing.T) {
	if got := escapeKeySeq("a "); got != `a\x20` {
		t.Errorf("BUG: escapeKeySeq(tarpas) = %q (norėta \\x20)", got)
	}
	// Būgnas: unescape(escape(x)) == x.
	b, err := unescapeKeySeq(`a\x20`)
	if err == nil && len(b) == 2 && b[0] == 'a' && b[1] == ' ' {
		// ok
	} else if err == nil {
		t.Errorf("BUG: unescape(\\x20) = %v", b)
	} else {
		t.Errorf("BUG: unescape(\\x20) neveikia: %v", err)
	}
}

// P8 — switchCh išvalo paieškos būseną: indeksas/query buvo VISO KLIENTO
// (ne lango) — kitaip „n“/„p“ kito kanale ieškojo pagal seno lango žinutes.
func TestSwitchChResetsSearchState(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":testnick!u@h JOIN #pirmas")
	c.parseIRC(":testnick!u@h JOIN #antras")
	c.addMsg("#pirmas", Message{ts: time.Now(), nick: "bob", text: "labas pirmame"})

	c.doSearch(c.findCh("#pirmas"), "labas")
	c.mu.Lock()
	if c.searchResultIdx < 0 || c.lastSearchQuery == "" {
		c.mu.Unlock()
		t.Fatal("sąranka: paieškos būsenos nėra")
	}
	c.mu.Unlock()

	c.switchCh(1) // #antras
	c.mu.Lock()
	idx, q := c.searchResultIdx, c.lastSearchQuery
	c.mu.Unlock()
	if idx != -1 || q != "" {
		t.Errorf("BUG: switchCh neišvalė paieškos būsenos (idx=%d, q=%q)", idx, q)
	}
}
