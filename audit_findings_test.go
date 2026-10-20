//go:build audit

// Reprodukcijos testai, rasti peržiūroje 2026-10-20.
// Paleisti: go test -tags audit -race ./...
//
// Kiekvienas testas atkarto realiai rastą klaidą. Kol klaida neištaisyta —
// testas FAIL'ina (tikėdamas klaidą). Kai klaida ištaisyta — testas praeina.
//
// Pažymėti: tikėjimo lygis:
//   CONFIRMED  — klaida realiai įvyksta (panic / race / krizė)
//   PLAUSIBLE  — elgsena neatitinka sutartos specifikacijos (dokumento ar kodo)

package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// ==================================================================
// 1. CONFIRMED — serverio PART *server* (mūsų nick) ištrina *server* kanalą
//    → activeIdx = -1 → draw()/addMsg() PANIK. DoS per serverį.
// ==================================================================

func TestFinding_ServerPartServerChannelPanic(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	c.nick = "testnick"
	c.mu.Unlock()

	// Serveris imituoja mūsų nick'ą ir sako, kad išeiname iš *server*
	c.parseIRC(":testnick!u@h PART *server* :bye")

	c.mu.Lock()
	idx := c.activeIdx
	n := len(c.channels)
	c.mu.Unlock()

	if idx < 0 || idx >= n {
		t.Errorf("CONFIRMED: serverio PART *server* paliko activeIdx=%d (kanalų: %d) → draw() panikos", idx, n)
		return
	}
	t.Logf("praėjo: activeIdx=%d, kanalų=%d", idx, n)
}

func TestFinding_ServerPartServerChannelDrawPanic(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	c.nick = "testnick"
	c.mu.Unlock()
	c.parseIRC(":testnick!u@h PART *server* :bye")

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("CONFIRMED: draw() po serverio PART *server* → panic: %v", r)
		}
	}()
	c.draw()
}

func TestFinding_ServerPartServerChannelAddMsgPanic(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	c.nick = "testnick"
	c.mu.Unlock()
	c.parseIRC(":testnick!u@h PART *server* :bye")

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("CONFIRMED: addMsg() po serverio PART *server* → panic: %v", r)
		}
	}()
	c.addMsg("#x", Message{nick: "a", text: "hi", msgType: MsgChat})
}

// ==================================================================
// 2. CONFIRMED — activeIdx neperskaičiuojamas, kai pašalinamas kanalas
//    PRIEŠ aktyvų. Aktyvus kanalas „slypčioja“ — vartotojas rašo į kitą.
// ==================================================================

func TestFinding_ActiveIdxNotShiftedOnPart(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	c.nick = "testnick"
	c.ensureCh("#a")
	c.ensureCh("#b")
	c.ensureCh("#c")
	c.activeIdx = 3 // aktyvus #c
	c.mu.Unlock()

	// Išeiname iš #b (indeksas 2) — aktyvus turi likti #c
	c.parseIRC(":testnick!u@h PART #b :bye")

	c.mu.Lock()
	active := c.channels[c.activeIdx].name
	c.mu.Unlock()
	if active != "#c" {
		t.Errorf("CONFIRMED: po PART #b aktyvus kanalas = %q (turi likti #c)", active)
	}
}

func TestFinding_ActiveIdxNotShiftedOnClose(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	c.ensureCh("#a")
	c.ensureCh("#b")
	c.ensureCh("#c")
	c.activeIdx = 2 // aktyvus #b
	c.mu.Unlock()

	c.handleCmd("close", "#a") // pašalina indeksą 1

	c.mu.Lock()
	active := c.channels[c.activeIdx].name
	c.mu.Unlock()
	if active != "#b" {
		t.Errorf("CONFIRMED: po /close #a aktyvus kanalas = %q (turi likti #b)", active)
	}
}

// ==================================================================
// 3. CONFIRMED — /mode su tik tarpais → PANIK (index out of range).
//    Reprodukuojama per config `perform=` — t.y. BE nartytojo įspėjimo.
// ==================================================================

func TestFinding_ModeWhitespaceArgsPanic(t *testing.T) {
	c := newTestClient()
	c.connected = true
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("CONFIRMED: /mode su tik tarpais → panic: %v", r)
		}
	}()
	c.handleCmd("mode", "   ")
}

func TestFinding_PerformModePanic(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	c.perform = []string{"/mode  "}
	c.mu.Unlock()

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("CONFIRMED: perform='/mode' → panic parseIRC gijoje: %v", r)
		}
	}()
	c.parseIRC(":srv 001 testnick :Welcome")
}

// ==================================================================
// 4. CONFIRMED — isIgnored() skaito ignoreList BEZ RAKINO.
//    parseIRC gija skaito, /ignore gija rašo → data race.
// ==================================================================

func TestFinding_IsIgnoredDataRace(t *testing.T) {
	c := newTestClient()
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(2)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				c.isIgnored("spammer!u@h")
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			c.mu.Lock()
			c.ignoreList = append(c.ignoreList, "x")
			c.mu.Unlock()
		}
		close(stop)
	}()
	wg.Wait()
}

// ==================================================================
// 5. CONFIRMED — c.connected / c.nick skaitomi BEZ RAKINO (input.go, irc.go)
// ==================================================================

func TestFinding_ConnectedNickDataRace(t *testing.T) {
	c := newTestClient()
	c.connected = true
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(2)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				// Naudojame SAUGIAS prieigas — jei klientas jas naudoja
				// be rakino, race detektorius parodys.
				_ = c.isConnected()
				_ = c.myNick()
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			c.mu.Lock()
			c.connected = !c.connected
			c.nick = "n"
			c.mu.Unlock()
		}
		close(stop)
	}()
	wg.Wait()
}

// ==================================================================
// 6. CONFIRMED — applyISUPPORT keičia ISUPPORT parametrus BEZ RAKINO,
//    o kitos gorutinos juos skaito (isChannel, nickEqual, stripMode).
//    Parametrai turi būti PER SERVERĮ (Client laukuose) — ne paketinio
//    lygio globaliai, nes vienas serveris turi negadinti kito nustatymų.
// ==================================================================

func TestFinding_ISUPPORTDataRace(t *testing.T) {
	c := newTestClient()
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(2)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = c.isChannel("#chan")
				_ = c.nickEqual("A", "a")
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 300; i++ {
			c.applyISUPPORT([]string{"CHANTYPES=!@", "PREFIX=(ov)@+", "CASEMAPPING=ascii"})
		}
		close(stop)
	}()
	wg.Wait()
}

// ISUPPORT turi būti izoliuotas per serverį: vieno kliento nustatymai
// negali gadinti kito.
func TestFinding_ISUPPORTIsolatedPerClient(t *testing.T) {
	c1 := newTestClient()
	c2 := newTestClient()

	c1.applyISUPPORT([]string{"CHANTYPES=!@"})

	if ct, _, _, _ := c2.isupSnapshot(); ct != "#&" {
		t.Errorf("CONFIRMED: vieno kliento ISUPPORT pakeitė kito: %q (turi likti \"#&\")", ct)
	}
}

// ==================================================================
// 7. CONFIRMED — privataus kanalo pavadinimas braižomas NESANITIZUOTAS
//    (ui.go:694 → displayName = ">" + name). Terminalo ESC injekcija
//    per priešiško serverio PRIIMTĄ PRIVMSG.
// ==================================================================

func TestFinding_PrivateChannelNameRawESC(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":ev\x1b[2J\x1b]0;PWN\x07!u@h PRIVMSG testnick :hi")

	c.mu.Lock()
	var pmNames []string
	for _, ch := range c.channels {
		if ch.isPrivate {
			pmNames = append(pmNames, ch.name)
		}
	}
	c.mu.Unlock()

	if len(pmNames) == 0 {
		t.Skip("privatus kanalas nesukurtas")
	}
	for _, n := range pmNames {
		if strings.ContainsRune(n, 0x1b) {
			t.Errorf("CONFIRMED: privataus kanalo pavadinimas turi ESC (braižomas raw): %q", n)
		}
	}
}

// ==================================================================
// 8. CONFIRMED — serverio pavadinimas ".." → logai rašomi UŽ logDir ribų
//    (sanitizePath ReplaceDir replace, bet ".." lieka kaip "..").
// ==================================================================

func TestFinding_LogPathTraversal(t *testing.T) {
	c := newTestClient()
	dir := t.TempDir()
	c.logDir = dir
	parent := filepath.Dir(dir)

	c.logMessage(dir, "..", "#a", Message{ts: time.Now(), text: "x", msgType: MsgChat})
	time.Sleep(150 * time.Millisecond)

	_ = filepath.Walk(parent, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		// Failas turi likti VIDUJE logDir — viskas, kas parašyta už jo
		// ribų, yra traversal.
		if !strings.HasPrefix(p, dir+string(os.PathSeparator)) {
			t.Errorf("CONFIRMED: log failas %s parašytas UŽ logDir ribų (%s)", p, dir)
		}
		return nil
	})
}

// ==================================================================
// 9. CONFIRMED — NickServ slaptažodis (IDENTIFY) patenka į log failą.
//    isSensitiveHistory saugoja TIK įvedimo istoriją, bet logMessage
//    rašo VISĄ, kas praėjo per addMsg.
// ==================================================================

func TestFinding_NickServPasswordInLog(t *testing.T) {
	c := newTestClient()
	c.connected = true
	dir := t.TempDir()
	c.logDir = dir

	c.handleInput("/msg NickServ identify hunter2secret")
	time.Sleep(250 * time.Millisecond)

	leaked := false
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		b, _ := os.ReadFile(p)
		if strings.Contains(string(b), "hunter2secret") {
			leaked = true
		}
		return nil
	})
	if leaked {
		t.Errorf("CONFIRMED: NickServ slaptažodis \"hunter2secret\" parašytas į log failą")
	}
}

// ==================================================================
// 10. CONFIRMED — CTCP PING atspinduliuoja serverio turinį BE ribojimo.
//     Užpuolėjas siunčia 20 KB CTCP PING → serveris gauna 20 KB.
// ==================================================================

func TestFinding_CTCPPingUnboundedReflection(t *testing.T) {
	c := newTestClient()
	c.connected = true
	big := strings.Repeat("A", 20000)
	ch := make(chan string, 10)
	c.writeCh = ch
	var got []string
	var mu sync.Mutex
	go func() {
		for l := range ch {
			mu.Lock()
			got = append(got, l)
			mu.Unlock()
		}
	}()
	c.parseIRC(":a!b@c PRIVMSG me :\x01PING " + big + "\x01")
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	for _, l := range got {
		if len(l) > 1000 {
			t.Errorf("CONFIRMED: CTCP PING atspinduliuotas be ribojimo: %d baitų", len(l))
		}
	}
}

// ==================================================================
// 11. CONFIRMED — /notice, /topic, /away siunčia >512 baitų eilutes.
//     IRC protokolo pažeidimas → serveris nutraukia ryšį.
// ==================================================================

func TestFinding_LongOutgoingLines(t *testing.T) {
	c := newTestClient()
	c.connected = true
	ch := make(chan string, 100)
	c.writeCh = ch
	var got []string
	var mu sync.Mutex
	go func() {
		for l := range ch {
			mu.Lock()
			got = append(got, l)
			mu.Unlock()
		}
	}()

	long := strings.Repeat("x", 2000)
	c.handleCmd("notice", "target "+long)
	c.handleCmd("topic", long)
	c.handleCmd("away", long)
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	for _, l := range got {
		if len(l) > 512 {
			t.Errorf("CONFIRMED: išsiųsta %d baitų eilutė (IRC limitas 512): %.40q", len(l), l)
		}
	}
}

// ==================================================================
// 12. CONFIRMED — neribotas eilučių skaitymas nuo serverio (memory DoS).
//     serveris siunčia MB be '\n' → ReadString kaupia viską atmintyje.
// ==================================================================

func TestFinding_UnboundedReadMemory(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		chunk := []byte(strings.Repeat("A", 64*1024))
		for i := 0; i < 64; i++ { // 4 MB be '\n'
			if _, err := conn.Write(chunk); err != nil {
				return
			}
		}
		time.Sleep(3 * time.Second)
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)

	// Tikriname TIKRĄ kliento skaitymo kelią (readLoop), ne savo ReadString.
	c := newTestClient()
	r := bufio.NewReader(conn)
	go c.readLoop(r)
	time.Sleep(1 * time.Second)
	runtime.ReadMemStats(&m1)

	if delta := int64(m1.HeapAlloc) - int64(m0.HeapAlloc); delta > 2*1024*1024 {
		t.Errorf("CONFIRMED: 4 MB eilutė be '\\n' kaupė %d baitų — read() neribota", delta)
	}
}

// ==================================================================
// 13. CONFIRMED — 332/353 be kanalo parametro sukuria kanalą su tuščiu
//     pavadinimu; PART neįjungtame kanale atkuria kanalą sąraše.
// ==================================================================

func TestFinding_EmptyChannelNameCreated(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	before := len(c.channels)
	c.mu.Unlock()

	c.parseIRC(":srv 332 :topic here")

	c.mu.Lock()
	after := len(c.channels)
	empty := 0
	for _, ch := range c.channels {
		if ch.name == "" {
			empty++
		}
	}
	c.mu.Unlock()
	if after != before || empty > 0 {
		t.Errorf("CONFIRMED: 332 be kanalo parametro sukūrė tuščią kanalą (kanalų: %d → %d)", before, after)
	}
}

func TestFinding_PartRecreatesForeignChannel(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	before := len(c.channels)
	c.mu.Unlock()

	c.parseIRC(":mes!u@h PART #neegzistuojantis :bye")

	c.mu.Lock()
	after := len(c.channels)
	c.mu.Unlock()
	if after != before {
		t.Errorf("CONFIRMED: PART neįjungtame kanale atkūrė kanalą sąraše (%d → %d)", before, after)
	}
}

// ==================================================================
// 14. CONFIRMED — kanalų sąrašas neribotas: JOIN/PM flood sukuria
//     šimtus kanalų (sidebar bloat, atmintis).
// ==================================================================

func TestFinding_UnboundedChannelList(t *testing.T) {
	c := newTestClient()
	for i := 0; i < 500; i++ {
		n := string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + string(rune('a'+(i/676)%26))
		c.parseIRC(":flood" + n + "!u@h JOIN #c" + n)
	}
	c.mu.Lock()
	n := len(c.channels)
	c.mu.Unlock()
	if n > 100 {
		t.Errorf("CONFIRMED: JOIN flood sukūrė %d kanalų (neribota)", n)
	}
}

// ==================================================================
// 15. PLAUSIBLE — /clearlogs (be argumentų) elgsenos neatitinka /help:
//     help sako "numatytai: visi šiame serveryje", kodas naudoja AKTYVŲ kanalą.
// ==================================================================

func TestFinding_ClearLogsDocMismatch(t *testing.T) {
	c := newTestClient()
	c.connected = true
	dir := t.TempDir()
	c.logDir = dir
	c.server = "srv"
	c.mu.Lock()
	c.ensureCh("#a")
	c.ensureCh("#b")
	c.activeIdx = 0 // aktyvus *server*
	c.mu.Unlock()

	c.logMessage(dir, "srv", "#a", Message{ts: time.Now(), text: "x", msgType: MsgChat})
	c.logMessage(dir, "srv", "#b", Message{ts: time.Now(), text: "x", msgType: MsgChat})

	c.handleCmd("clearlogs", "")

	srvDir := filepath.Join(dir, "srv")
	entries, err := os.ReadDir(srvDir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("PLAUSIBLE: /clearlogs (be args) turėjo ištrinti VISUS serverio log failus, liko: %v", names)
	}
}

// ==================================================================
// 16. CONFIRMED — switchCh() su activeIdx=-1 → PANIK
//     (activeIdx nustatomas -1 po PART *server*; switchCh naudoja
//     c.channels[c.activeIdx] be patikros)
// ==================================================================

func TestFinding_SwitchChWithNegativeActiveIdx(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	c.nick = "testnick"
	c.ensureCh("#a")
	c.activeIdx = -1 // kaip po PART *server*
	c.mu.Unlock()

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("CONFIRMED: switchCh() su activeIdx=-1 → panic: %v", r)
		}
	}()
	c.switchCh(0)
}

// ==================================================================
// 17. CONFIRMED — AWAY handleris (irc.go) nuskaito active.nicks ir
//     naudoja activeIdx be patikros → PANIC kai activeIdx = -1.
// ==================================================================

func TestFinding_AwayWithNegativeActiveIdx(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	c.nick = "testnick"
	c.activeIdx = -1
	c.mu.Unlock()

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("CONFIRMED: AWAY su activeIdx=-1 → panic: %v", r)
		}
	}()
	c.parseIRC(":x!u@h AWAY :away nu")
}

// ==================================================================
// 18. CONFIRMED — /connect su serveriu „..“ → logai rašomi UŽ logDir ribų.
//     sanitizePath ReplaceDir pakeičia "/" į "_", bet „..“ lieka nepakeistas
//     → filepath.Join(logDir, "..") = logDir's parent.
// ==================================================================

func TestFinding_ConnectServerPathTraversal(t *testing.T) {
	c := newTestClient()
	dir := t.TempDir()
	c.logDir = dir
	parent := filepath.Dir(dir)

	c.handleCmd("connect", ".. 6667 nick")
	c.mu.Lock()
	srv := c.server
	c.mu.Unlock()

	c.logMessage(dir, srv, "#a", Message{ts: time.Now(), text: "x", msgType: MsgChat})
	time.Sleep(150 * time.Millisecond)

	_ = filepath.Walk(parent, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasPrefix(p, dir+string(os.PathSeparator)) {
			t.Errorf("CONFIRMED: /connect su „%s“ → log už logDir ribų: %s", srv, p)
		}
		return nil
	})
}

// ==================================================================
// 19. CONFIRMED — savo žinutės pridedamos su c.nick BE RAKINO.
//     input.go:76 ir input.go:768 — c.nick skaitomas užrakintos
//     zonos (c.mu) išvestoj. /nick keičia c.nick iš kitos gorutinos.
// ==================================================================

func TestFinding_OwnMessageNickRace(t *testing.T) {
	c := newTestClient()
	c.connected = true
	c.mu.Lock()
	c.ensureCh("#a")
	c.activeIdx = 1
	c.mu.Unlock()

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(2)
	go func() { // /nick keičia c.nick
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			c.mu.Lock()
			c.nick = fmt.Sprintf("nick%d", i)
			c.mu.Unlock()
		}
	}()
	go func() { // savo žinutės rašomos su c.nick (be rakino)
		defer wg.Done()
		for i := 0; i < 500; i++ {
			c.handleInput("labas")
		}
		close(stop)
	}()
	wg.Wait()
}

// ==================================================================
// 20. CONFIRMED — serverio pavadinimas nevaliduojamas.
//     Pavadinimas įrašomas į IRC komandas (NICK/USER/JOIN) ir į žurnalų
//     katalogo vardą. „..“ ar „evil\r\nJOIN #x“ leistų komandų injekciją
//     arba katalogų traversą.
// ==================================================================

func TestFinding_ConnectServerNotValidated(t *testing.T) {
	for _, bad := range []string{"..", "evil\r\nJOIN #x", "a b", "sérver", "evil\x1b[2J"} {
		c := newTestClient()
		c.handleCmd("connect", bad+" 6667 nick")
		c.mu.Lock()
		srv := c.server
		c.mu.Unlock()
		if srv == bad {
			t.Errorf("CONFIRMED: /connect priėmė netinkamą serverio pavadinimą %q", bad)
		}
	}
}

func TestFinding_ConfigServerNotValidated(t *testing.T) {
	cfg := defaultConfig()
	cfg.Server = "evil\r\nJOIN #x"
	c := newClient(cfg)
	if c.server == cfg.Server {
		t.Errorf("CONFIRMED: netinkamas serverio pavadinimas iš konfigūracijos priimtas: %q", c.server)
	}
}

// ==================================================================
// 21. CONFIRMED — komandų injekcija per /join, /msg, /notice.
//     Kanalo/gavėjo pavadinimas įrašomas į JOIN/PRIVMSG/NOTICE komandą.
//     CRLF ar tarpai leistų įterpti papildomą komandą į srautą.
// ==================================================================

func TestFinding_CommandInjectionViaTargets(t *testing.T) {
	cases := []struct {
		cmd  string
		args string
	}{
		{"join", "#x\r\nJOIN #injekcija"},
		{"join", "#x y"},
		{"msg", "victim\r\nJOIN #x labas"},
		{"notice", "victim\r\nJOIN #x labas"},
	}
	for _, tc := range cases {
		c := newTestClient()
		c.connected = true
		ch := make(chan string, 50)
		c.writeCh = ch
		var got []string
		var mu sync.Mutex
		go func() {
			for l := range ch {
				mu.Lock()
				got = append(got, l)
				mu.Unlock()
			}
		}()
		c.handleCmd(tc.cmd, tc.args)
		time.Sleep(80 * time.Millisecond)
		mu.Lock()
		for _, l := range got {
			if strings.ContainsAny(l, "\r\n") {
				t.Errorf("CONFIRMED: /%s %q → išsiųsta eilutė su CRLF: %q", tc.cmd, tc.args, l)
			}
		}
		mu.Unlock()
	}
}
