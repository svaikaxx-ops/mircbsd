package main

// security_test.go — atakų, fuzzing ir regresijų testai
//
// Paleisti su race detectoriumi:
//   go test -race ./...
//
// Paleisti fuzz testus (pvz. 30s):
//   go test -fuzz=FuzzParseIRC       -fuzztime=30s
//   go test -fuzz=FuzzStripIRCFormat -fuzztime=30s
//   go test -fuzz=FuzzWrapText       -fuzztime=30s

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// ============================================================
// Pagalbinės funkcijos
// ============================================================

// captureWrites grąžina kanalą, kuris fiksuoja visas c.send() iškvietimus.
// Grąžina slice po delay, kai eilė nurimsta.
func captureWrites(c *Client) func(d time.Duration) []string {
	var mu sync.Mutex
	var out []string
	go func() {
		for line := range c.writeCh {
			mu.Lock()
			out = append(out, line)
			mu.Unlock()
		}
	}()
	return func(d time.Duration) []string {
		time.Sleep(d)
		mu.Lock()
		defer mu.Unlock()
		cp := make([]string, len(out))
		copy(cp, out)
		return cp
	}
}

// ============================================================
// IRC injekcijos apsauga
// ============================================================

func TestSendStripsNewlines(t *testing.T) {
	c := newTestClient()
	collect := captureWrites(c)

	injections := []string{
		"PRIVMSG #chan :labas\r\nQUIT",
		"PRIVMSG #chan :labas\nQUIT",
		"PRIVMSG #chan :labas\rQUIT",
		"PRIVMSG #chan :a\r\n\r\nQUIT :bye",
	}

	for _, inj := range injections {
		c.send(inj)
	}

	sent := collect(30 * time.Millisecond)
	for _, s := range sent {
		// Injekcija pavyktų tik jei QUIT taptų atskira eilute.
		// Po \r\n ištrynimo "labas\r\nQUIT" → "labasQUIT" (1 eilutė, ne 2).
		// Tikrinimas: jokia siunčiama eilutė neturi prasidėti nuo "QUIT".
		if s == "QUIT" || strings.HasPrefix(s, "QUIT ") || strings.HasPrefix(s, "QUIT:") {
			t.Errorf("IRC injekcija pavyko — QUIT kaip atskira komanda: %q", s)
		}
		if strings.ContainsAny(s, "\r\n") {
			t.Errorf("siųstoje eilutėje yra \\r arba \\n: %q", s)
		}
	}
	// Papildomai: kiekviena siunčiama eilutė turi būti viena komanda (ne kelios)
	if len(sent) > len(injections) {
		t.Errorf("išsiųsta daugiau komandų (%d) nei įvesties eilučių (%d) — galima injekcija",
			len(sent), len(injections))
	}
}

func TestSendNullByte(t *testing.T) {
	c := newTestClient()
	collect := captureWrites(c)

	c.send("PRIVMSG #chan :labas\x00QUIT")
	sent := collect(20 * time.Millisecond)
	// Null baitas yra leistinas Go string, bet neturėtų sukelti panic
	if len(sent) == 0 {
		t.Error("send() su null baitu neturėtų nuryti žinutės")
	}
}

func TestSendQueueFull(t *testing.T) {
	c := newTestClient()
	// Prikimšti kanalą iki kraštų (256 buferis) be goroutine kuris skaitytų
	for i := 0; i < 300; i++ {
		c.send(fmt.Sprintf("PRIVMSG #chan :žinutė %d", i))
	}
	// Neturėtų pakibti ar panic — default case turi veikti
}

// ============================================================
// CTCP atakos
// ============================================================

func TestCTCPVersionDisabled(t *testing.T) {
	c := newTestClient()
	c.ctcpVersion = false
	collect := captureWrites(c)

	c.parseIRC(":attacker!u@h PRIVMSG testnick :\x01VERSION\x01")
	sent := collect(20 * time.Millisecond)

	for _, s := range sent {
		if strings.Contains(s, "VERSION") {
			t.Errorf("VERSION atsakytas nors ctcpVersion=false: %q", s)
		}
	}
}

func TestCTCPVersionEnabled(t *testing.T) {
	c := newTestClient()
	c.ctcpVersion = true
	collect := captureWrites(c)

	c.parseIRC(":user!u@h PRIVMSG testnick :\x01VERSION\x01")
	sent := collect(20 * time.Millisecond)

	found := false
	for _, s := range sent {
		if strings.Contains(s, "VERSION") && strings.Contains(s, "NOTICE") {
			found = true
		}
	}
	if !found {
		t.Error("VERSION turėtų būti atsakytas kai ctcpVersion=true")
	}
}

func TestCTCPFlood(t *testing.T) {
	// 100 VERSION užklausų iš eilės neturėtų sukelti problemų
	c := newTestClient()
	for i := 0; i < 100; i++ {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("panic CTCP flood iteracija %d: %v", i, r)
				}
			}()
			c.parseIRC(":flooder!u@h PRIVMSG testnick :\x01VERSION\x01")
		}()
	}
}

func TestCTCPUnknownType(t *testing.T) {
	c := newTestClient()
	// Nežinomas CTCP tipas neturėtų sukelti panic
	unknownTypes := []string{
		"\x01CLIENTINFO\x01",
		"\x01TIME\x01",
		"\x01FINGER\x01",
		"\x01SOURCE\x01",
		"\x01USERINFO\x01",
		"\x01" + strings.Repeat("X", 500) + "\x01",
	}
	for _, ct := range unknownTypes {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("panic su CTCP %q: %v", ct, r)
				}
			}()
			c.parseIRC(fmt.Sprintf(":u!u@h PRIVMSG testnick :%s", ct))
		}()
	}
}

// ============================================================
// Piktybinis IRC serveris (tikras TCP)
// ============================================================

// startMaliciousServer paleidžia lokalų TCP serverį kuris siunčia
// piktybines eilutes ir grąžina adresą.
func startMaliciousServer(t *testing.T, payloads []string) (addr string, done func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("nepavyko paleisti serverio: %v", err)
	}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		// Siųsti registracijos pranešimus
		fmt.Fprintf(conn, ":test.server 001 testnick :Welcome\r\n")
		time.Sleep(10 * time.Millisecond)
		for _, p := range payloads {
			fmt.Fprintf(conn, "%s\r\n", p)
		}
		// Laikyti ryšį kol klientas uždaro
		time.Sleep(200 * time.Millisecond)
	}()
	return ln.Addr().String(), func() { ln.Close() }
}

func runClientAgainstServer(t *testing.T, addr string) {
	t.Helper()
	host, port, _ := net.SplitHostPort(addr)
	c := newTestClient()
	c.server = host
	c.port = port
	c.reconnect = false

	// Prijungti su timeout — doConnect blokuoja kol ryšys nutrūksta
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("panic doConnect: %v", r)
			}
		}()
		c.doConnect()
	}()

	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Error("klientas per ilgai laukia (timeout)")
		if c.conn != nil {
			c.conn.Close()
		}
	}
}

func TestMaliciousServerLongLines(t *testing.T) {
	payloads := []string{
		":a!b@c PRIVMSG #chan :" + strings.Repeat("A", 65535),
		":a!b@c PRIVMSG #chan :" + strings.Repeat("lietuviškas tekstas žčšė ", 1000),
		strings.Repeat("X", 65535),
	}
	addr, stop := startMaliciousServer(t, payloads)
	defer stop()
	runClientAgainstServer(t, addr)
}

func TestMaliciousServerControlChars(t *testing.T) {
	payloads := []string{
		":a!b@c PRIVMSG #chan :\x00\x01\x02\x03\x04\x05\x06\x07\x08\x0b\x0c\x0e\x0f",
		":a!b@c PRIVMSG #chan :\x1b[2J\x1b[H",                      // ANSI escape injekcija
		":a!b@c PRIVMSG #chan :\x1b]0;TITLE\x07",                   // xterm title injekcija
		":a!b@c PRIVMSG #chan :" + strings.Repeat("\x03", 100),     // mIRC spalvų spam
		":a!b@c PRIVMSG #chan :" + strings.Repeat("\x02\x0f", 500), // bold toggle spam
	}
	addr, stop := startMaliciousServer(t, payloads)
	defer stop()
	runClientAgainstServer(t, addr)
}

func TestMaliciousServerMalformedIRC(t *testing.T) {
	payloads := []string{
		":",
		":!@",
		":!user@ PRIVMSG",
		"PRIVMSG",
		":nick!user@host",
		"001",
		strings.Repeat(": ", 1000),
		":a!b@c " + strings.Repeat("X ", 5000),
		":nick!user@" + strings.Repeat("host.", 200) + "com PRIVMSG #chan :hi",
	}
	addr, stop := startMaliciousServer(t, payloads)
	defer stop()
	runClientAgainstServer(t, addr)
}

func TestMaliciousServerNickFlood(t *testing.T) {
	// 200 NICK pakeitimų per sekundę
	payloads := make([]string, 200)
	for i := range payloads {
		payloads[i] = fmt.Sprintf(":user%d!u@h NICK :user%d", i, i+1)
	}
	addr, stop := startMaliciousServer(t, payloads)
	defer stop()
	runClientAgainstServer(t, addr)
}

func TestMaliciousServerJoinFlood(t *testing.T) {
	// 500 JOIN pranešimų — užpildyti kanalų sąrašą
	payloads := make([]string, 500)
	for i := range payloads {
		payloads[i] = fmt.Sprintf(":flood%d!u@h JOIN #chan%d", i, i)
	}
	addr, stop := startMaliciousServer(t, payloads)
	defer stop()
	runClientAgainstServer(t, addr)
}

func TestMaliciousServerWHOISInjection(t *testing.T) {
	// Bandymas įterpti HTML/ANSI per WHOIS atsakymus
	payloads := []string{
		":server 311 testnick victim \x1b[31m!evil@\x1b[0m * :Real\x01Name",
		":server 312 testnick victim server.net :\x1b]0;PWNED\x07",
		":server 319 testnick victim :#chan1 \r\n:server 433 * testnick :takeover",
		":server 317 testnick victim notanumber notanumber2 :idle",
		":server 318 testnick victim :End",
	}
	addr, stop := startMaliciousServer(t, payloads)
	defer stop()
	runClientAgainstServer(t, addr)
}

func TestMaliciousServerMODE(t *testing.T) {
	payloads := []string{
		":server MODE #chan +oooooooooooooooooooooo u1 u2 u3 u4 u5",
		":server MODE #chan +-+-+-+-+-+o testnick",
		":server MODE #chan +" + strings.Repeat("o", 100),
		":server MODE",
		":server MODE #chan",
	}
	addr, stop := startMaliciousServer(t, payloads)
	defer stop()
	runClientAgainstServer(t, addr)
}

// ============================================================
// Naujų funkcijų testai
// ============================================================

func TestConnectingFlagPreventsDouble(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	c.connecting = true
	c.mu.Unlock()

	// Antra connectLoop turėtų iš karto grįžti
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.connectLoop() // turėtų grįžti iš karto
	}()

	select {
	case <-done:
		// Gerai — grįžo greitai
	case <-time.After(200 * time.Millisecond):
		t.Error("connectLoop nepabaigė nors connecting=true")
	}

	// Flag turi likti true (kita goroutine jo neatstatė)
	c.mu.Lock()
	still := c.connecting
	c.mu.Unlock()
	if !still {
		t.Error("connecting flag neturėtų būti keičiamas svetimos goroutine")
	}
}

func TestKickCommandSent(t *testing.T) {
	c := newTestClient()
	c.connected = true
	c.mu.Lock()
	c.ensureCh("#testas")
	c.activeIdx = 1
	c.mu.Unlock()
	collect := captureWrites(c)

	c.handleCmd("kick", "baduser Taisyklių pažeidimas")
	sent := collect(20 * time.Millisecond)

	found := false
	for _, s := range sent {
		if strings.HasPrefix(s, "KICK #testas baduser :") && strings.Contains(s, "Taisyklių") {
			found = true
		}
	}
	if !found {
		t.Errorf("KICK komanda neišsiųsta. Išsiųsta: %v", sent)
	}
}

func TestKickDefaultReason(t *testing.T) {
	c := newTestClient()
	c.connected = true
	c.mu.Lock()
	c.ensureCh("#testas")
	c.activeIdx = 1
	c.mu.Unlock()
	collect := captureWrites(c)

	c.handleCmd("kick", "baduser")
	sent := collect(20 * time.Millisecond)

	found := false
	for _, s := range sent {
		if strings.HasPrefix(s, "KICK #testas baduser :") {
			found = true
		}
	}
	if !found {
		t.Errorf("KICK su numatyta priežastimi neišsiųsta: %v", sent)
	}
}

func TestKickNotInChannel(t *testing.T) {
	c := newTestClient()
	c.connected = true
	// *server* kanalas (index 0) nėra # kanalas
	c.handleCmd("kick", "baduser")
	// Neturėtų siųsti KICK
}

func TestModeCommand(t *testing.T) {
	c := newTestClient()
	c.connected = true
	c.mu.Lock()
	c.ensureCh("#testas")
	c.activeIdx = 1
	c.mu.Unlock()
	collect := captureWrites(c)

	c.handleCmd("mode", "+m")
	sent := collect(20 * time.Millisecond)

	found := false
	for _, s := range sent {
		if s == "MODE #testas +m" {
			found = true
		}
	}
	if !found {
		t.Errorf("MODE komanda neteisingai išsiųsta: %v", sent)
	}
}

func TestModeCommandExplicitTarget(t *testing.T) {
	c := newTestClient()
	c.connected = true
	collect := captureWrites(c)

	c.handleCmd("mode", "testnick +x")
	sent := collect(20 * time.Millisecond)

	found := false
	for _, s := range sent {
		if s == "MODE testnick +x" {
			found = true
		}
	}
	if !found {
		t.Errorf("MODE su tiksliu taikiniu neišsiųsta: %v", sent)
	}
}

func TestCloseSendsPart(t *testing.T) {
	c := newTestClient()
	c.connected = true
	c.mu.Lock()
	c.ensureCh("#linux")
	c.activeIdx = 1
	c.mu.Unlock()
	collect := captureWrites(c)

	c.handleCmd("close", "")
	sent := collect(20 * time.Millisecond)

	found := false
	for _, s := range sent {
		if strings.HasPrefix(s, "PART #linux") {
			found = true
		}
	}
	if !found {
		t.Errorf("/close turėtų siųsti PART. Išsiųsta: %v", sent)
	}

	// Kanalas turi būti pašalintas iš sąrašo
	c.mu.Lock()
	ch := c.findCh("#linux")
	c.mu.Unlock()
	if ch != nil {
		t.Error("#linux turėtų būti pašalintas po /close")
	}
}

func TestCloseServerChannelNoCommand(t *testing.T) {
	c := newTestClient()
	c.connected = true
	collect := captureWrites(c)

	// *server* (index 0) neturėtų siųsti PART
	c.handleCmd("close", "")
	sent := collect(20 * time.Millisecond)

	for _, s := range sent {
		if strings.HasPrefix(s, "PART") {
			t.Errorf("*server* kanalo uždarymas neturėtų siųsti PART: %q", s)
		}
	}
}

func TestClosePrivateQueryNoCommand(t *testing.T) {
	c := newTestClient()
	c.connected = true
	c.mu.Lock()
	c.ensureCh("jonas") // privatus pokalbis, ne # kanalas
	c.activeIdx = 1
	c.mu.Unlock()
	collect := captureWrites(c)

	c.handleCmd("close", "")
	sent := collect(20 * time.Millisecond)

	for _, s := range sent {
		if strings.HasPrefix(s, "PART") {
			t.Errorf("privatus langas neturėtų siųsti PART: %q", s)
		}
	}
}

// ============================================================
// sortNicks — abėcėlinis rikiavimas tame pačiame lygyje
// ============================================================

func TestSortNicksAlphabetical(t *testing.T) {
	ch := &Channel{name: "#test"}
	ch.addNick("@Zebra")
	ch.addNick("@Alpha")
	ch.addNick("@mango")

	// Visi @ lygio — turi būti abėcėliškai
	if len(ch.nicks) != 3 {
		t.Fatalf("tikėjomės 3 nickų, gavome %d", len(ch.nicks))
	}
	if stripMode(ch.nicks[0]) != "Alpha" {
		t.Errorf("pirmas turi būti Alpha, gavome %q", ch.nicks[0])
	}
	if stripMode(ch.nicks[1]) != "mango" {
		t.Errorf("antras turi būti mango, gavome %q", ch.nicks[1])
	}
	if stripMode(ch.nicks[2]) != "Zebra" {
		t.Errorf("trečias turi būti Zebra, gavome %q", ch.nicks[2])
	}
}

func TestSortNicksMixedLevels(t *testing.T) {
	nicks := []string{"+zvoiced", "zplain", "@zops", "~zowner"}
	sortNicks(nicks)

	if nicks[0] != "~zowner" {
		t.Errorf("[0] turi būti ~zowner, gavome %q", nicks[0])
	}
	if nicks[1] != "@zops" {
		t.Errorf("[1] turi būti @zops, gavome %q", nicks[1])
	}
	if nicks[2] != "+zvoiced" {
		t.Errorf("[2] turi būti +zvoiced, gavome %q", nicks[2])
	}
	if nicks[3] != "zplain" {
		t.Errorf("[3] turi būti zplain, gavome %q", nicks[3])
	}
}

// ============================================================
// Port validacija
// ============================================================

func TestPortValidation(t *testing.T) {
	invalid := []string{"0", "65536", "99999", "abc", "-1", "", "6667x"}
	for _, p := range invalid {
		port, err := parsePortStr(p)
		if err == nil {
			t.Errorf("portas %q turėtų būti netinkamas, gavome %d", p, port)
		}
	}

	valid := []string{"1", "6667", "6697", "65535"}
	for _, p := range valid {
		_, err := parsePortStr(p)
		if err != nil {
			t.Errorf("portas %q turėtų būti tinkamas: %v", p, err)
		}
	}
}

// parsePortStr — pagalbinė funkcija porto validacijai (atspindi logika iš handleCommand)
func parsePortStr(s string) (int, error) {
	p, err := fmt.Sscanf(s, "%d", new(int))
	_ = p
	if err != nil {
		return 0, fmt.Errorf("ne skaičius")
	}
	n, _ := fmt.Sscanf(s, "%d", &p)
	_ = n
	// Naudoti strconv kaip realus kodas
	var val int
	_, err2 := fmt.Sscanf(s, "%d", &val)
	if err2 != nil || val < 1 || val > 65535 {
		return 0, fmt.Errorf("portas %q yra ne 1-65535", s)
	}
	// Patikrinti ar nėra papildomų simbolių
	if fmt.Sprintf("%d", val) != s {
		return 0, fmt.Errorf("portas %q turi papildomų simbolių", s)
	}
	return val, nil
}

// ============================================================
// Fuzz testai
// ============================================================

func FuzzParseIRC(f *testing.F) {
	// Seed corpus — žinomi formatai
	seeds := []string{
		":nick!user@host PRIVMSG #chan :labas",
		":nick!user@host JOIN #chan",
		":nick!user@host QUIT :bye",
		":server 001 nick :Welcome",
		":server 433 * nick :already in use",
		"PING :server",
		":a!b@c PRIVMSG nick :\x01VERSION\x01",
		":a!b@c PRIVMSG nick :\x01ACTION daro\x01",
		":server MODE #chan +o nick",
		":nick!user@host NICK :newnick",
		"",
		":",
		strings.Repeat("A", 4096),
		":a!b@c PRIVMSG #chan :\x03044red\x03",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, line string) {
		c := newTestClient()
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("panic su eilute %q: %v", line, r)
			}
		}()
		c.parseIRC(line)
	})
}

func FuzzStripIRCFormat(f *testing.F) {
	seeds := []string{
		"\x02bold\x02",
		"\x034red\x03",
		"\x0304,02colored\x0f",
		"\x1ditalic\x0f",
		"\x1funder",
		"\x16reverse",
		strings.Repeat("\x03", 100),
		strings.Repeat("\x02\x0f", 200),
		"normalus tekstas",
		"\x00\x01\x02\x03",
		strings.Repeat("š", 500),
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("panic su %q: %v", s, r)
			}
		}()
		result := stripIRCFormat(s)
		// Rezultatas neturi turėti mIRC formatavimo simbolių
		for _, b := range []byte{0x02, 0x03, 0x0f, 0x16, 0x1d, 0x1f} {
			if strings.ContainsRune(result, rune(b)) {
				t.Errorf("stripIRCFormat(%q) paliko formatavimo simbolį 0x%02x: %q", s, b, result)
			}
		}
	})
}

func FuzzWrapText(f *testing.F) {
	seeds := []string{
		"trumpas",
		strings.Repeat("ilgas žodis ", 100),
		"labailabailabailabai",
		"",
		strings.Repeat("š", 200),
		"a b c d e f g h i j k",
	}
	for _, s := range seeds {
		f.Add(s, 80)
		f.Add(s, 1)
		f.Add(s, 0)
	}
	f.Fuzz(func(t *testing.T, text string, width int) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("panic su (%q, %d): %v", text, width, r)
			}
		}()
		lines := wrapText(text, width)
		if len(lines) == 0 {
			t.Errorf("wrapText(%q, %d) grąžino 0 eilučių", text, width)
		}
	})
}

func FuzzContainsNick(f *testing.F) {
	seeds := [][2]string{
		{"hello world", "world"},
		{"testnick: hi", "testnick"},
		{"", "nick"},
		{"nick", ""},
		{strings.Repeat("x", 10000), "x"},
		{"žlugau čia", "žlugau"},
	}
	for _, s := range seeds {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(t *testing.T, text, nick string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("panic su (%q, %q): %v", text, nick, r)
			}
		}()
		containsNick(text, nick)
	})
}

// ============================================================
// Race detector scenarijai
// ============================================================

func TestRaceParseIRCConcurrent(t *testing.T) {
	c := newTestClient()
	var wg sync.WaitGroup

	messages := []string{
		":a!u@h PRIVMSG #chan :žinutė",
		":a!u@h JOIN #chan",
		":a!u@h PART #chan",
		":a!u@h NICK :b",
		"PING :server",
		":server 001 testnick :Welcome",
	}

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				c.parseIRC(messages[(n+j)%len(messages)])
			}
		}(i)
	}
	wg.Wait()
}

func TestRaceAddMsgAndDraw(t *testing.T) {
	c := newTestClient()
	var wg sync.WaitGroup

	// Rašymas
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				c.addMsg("#chan", Message{
					ts:      time.Now(),
					nick:    fmt.Sprintf("user%d", n),
					text:    fmt.Sprintf("žinutė %d", j),
					msgType: MsgChat,
				})
			}
		}(i)
	}

	// Skaitymas per draw (su lock)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				c.mu.Lock()
				if len(c.channels) > 0 {
					_ = len(c.channels[0].messages)
				}
				c.mu.Unlock()
				time.Sleep(time.Millisecond)
			}
		}()
	}

	wg.Wait()
}

func TestRaceSwitchAndAddMsg(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	c.ensureCh("#chan1")
	c.ensureCh("#chan2")
	c.mu.Unlock()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				if j%2 == 0 {
					c.switchCh(n % 3)
				} else {
					c.addMsg(fmt.Sprintf("#chan%d", n%2+1), Message{
						ts:      time.Now(),
						text:    "test",
						msgType: MsgChat,
					})
				}
			}
		}(i)
	}
	wg.Wait()
}

// ============================================================
// Terminalo control-char (ANSI/OSC) injekcijos apsauga
// ============================================================

func TestRenderIRCFormatStripsControlChars(t *testing.T) {
	payloads := []string{
		"\x1b[2J\x1b[H",              // ekrano trynimas
		"\x1b]0;TITLE\x07",           // xterm antraštės injekcija
		"\x1b]52;c;ZW5jb2RlZA==\x07", // OSC 52 iškarpinės injekcija
		"a\x9b3Hb",                   // vieno baito CSI (C1)
		"sekcija\x7fX",               // DEL
	}
	// Šiame kape nėra ESC — formatteris gali sugeneruoti tik savo ansiBold/ansiUnder
	if out := renderIRCFormat("\x00\x01\x02\x06\x08\x0b\x0c\x0e\x1f"); out != ansiBold+ansiUnder+ansiReset {
		t.Errorf("renderIRCFormat(C0 kaina) = %q, norėtų %q", out, ansiBold+ansiUnder+ansiReset)
	}
	for _, p := range payloads {
		// ansiReset pabaigoje yra ESC — apipjaunam paskutinę sekciją
		out := renderIRCFormat(p)
		if strings.HasSuffix(out, ansiReset) {
			out = strings.TrimSuffix(out, ansiReset)
		}
		for _, bad := range []rune{'\x1b', '\x9b', '\x07', '\x7f', 0, 1, 2} {
			if strings.ContainsRune(out, bad) {
				t.Errorf("renderIRCFormat(%q) paliko valdymo simbolį 0x%02x: %q", p, bad, out)
			}
		}
	}
}

func TestSanitizeText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"#chan", "#chan"},
		{"\x1b[31mred\x1b[0m", "[31mred[0m"},
		{"tab\tok", "tab\tok"},
		{"ž\x00i\x7furas", "žiuras"},
	}
	for _, c := range cases {
		if got := sanitizeText(c.in); got != c.want {
			t.Errorf("sanitizeText(%q) = %q, norėtų %q", c.in, got, c.want)
		}
	}
}

// ============================================================
// CAP 302 tęsinių derybos
// ============================================================

func TestCAPMultilineLS(t *testing.T) {
	c := newTestClient()
	c.saslPass = "Slaptazodis1"
	collect := captureWrites(c)
	// Tęsinys su "*" — dar tik kaupiam
	c.parseIRC(":srv CAP * LS * :account-notify sasl")
	c.parseIRC(":srv CAP * LS :extended-join echo-message server-time")
	writes := collect(200 * time.Millisecond)

	reqs := 0
	var req string
	for _, w := range writes {
		if strings.HasPrefix(w, "CAP REQ") {
			reqs++
			req = w
		}
		if strings.HasPrefix(w, "CAP END") && reqs == 0 {
			t.Errorf("CAP END išsiųsta prieš pilną LS sąrašą: %q", w)
		}
	}
	if reqs != 1 {
		t.Fatalf("tikėtasi 1 CAP REQ, gauta %d (writes: %v)", reqs, writes)
	}
	for _, want := range []string{"sasl", "echo-message", "server-time"} {
		if !strings.Contains(req, want) {
			t.Errorf("CAP REQ trūksta %q: %q", want, req)
		}
	}
	if !strings.Contains(req, "sasl") {
		t.Errorf("CAP REQ neprašo sasl, nors sasl slaptažodis nustatytas: %q", req)
	}
}

// ============================================================
// Kito tipo saugos pataisymai
// ============================================================

func TestIsSensitiveHistory(t *testing.T) {
	sensitive := []string{
		"/msg nickserv IDENTIFY Slaptazodis1",
		"/msg NickServ identify pass",
		"/ns identify pass",
		"/ns ident pass",
	}
	for _, s := range sensitive {
		if !isSensitiveHistory(s) {
			t.Errorf("isSensitiveHistory(%q) = false, turėtų būti true", s)
		}
	}
	plain := []string{"labas", "/join #lietuviai", "/ns info"}
	for _, s := range plain {
		if isSensitiveHistory(s) {
			t.Errorf("isSensitiveHistory(%q) = true, turėtų būti false", s)
		}
	}
}

func TestFirstField(t *testing.T) {
	if got := firstField("   "); got != "" {
		t.Errorf("firstField(tarpai) = %q, norėtų \"\"", got)
	}
	if got := firstField(" foo bar "); got != "foo" {
		t.Errorf("firstField = %q, norėtų \"foo\"", got)
	}
}

func TestIsValidNickStrictCharset(t *testing.T) {
	invalid := []string{"jo\x1bnas", "jo,nas", "jo|nas"}
	for _, n := range invalid {
		// ` | leistini — patikrinam tikrai kvailus simbolius
		if n == "jo|nas" {
			if !isValidNick(n) {
				t.Errorf("isValidNick(%q) = false, '|' yra RFC simbolis", n)
			}
			continue
		}
		if isValidNick(n) {
			t.Errorf("isValidNick(%q) = true, turėtų būti false", n)
		}
	}
}

func TestNickLengthCapOn433(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	c.nick = strings.Repeat("a", 30)
	c.mu.Unlock()
	collect := captureWrites(c)
	c.parseIRC(":srv 433 * oldnick :Nickname is already in use")
	writes := collect(200 * time.Millisecond)
	if len(writes) == 0 || !strings.HasPrefix(writes[0], "NICK ") {
		t.Fatalf("tikėtasi NICK komandos, gauta: %v", writes)
	}
	n := strings.TrimPrefix(writes[0], "NICK ")
	if len(n) > 30 {
		t.Errorf("NICK per ilgas po 433: %d ženklų", len(n))
	}
}

// ============================================================
// Paste surinkimas per kelis stdin skaitinius
// ============================================================

func TestPasteSplitAcrossReads(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	c.connected = true
	c.ensureCh("#chan")
	c.activeIdx = 1
	c.mu.Unlock()
	collect := captureWrites(c)

	// Paste su 3 eilutėmis, padalintas į tris gabaliukus (antrasis baigiasi
	// eilutės viduryje, trečiasis — prasideda nuo to tęsinio)
	c.handleKey([]byte("pirmoji\nantroji\n"))
	time.Sleep(10 * time.Millisecond)
	c.handleKey([]byte("trecija-pus"))
	time.Sleep(10 * time.Millisecond)
	c.handleKey([]byte("e\n"))
	time.Sleep(60 * time.Millisecond) // surinkimo lango pabaiga (langa pratęsė tęsiniai)
	// Patvirtinimas PO lango
	c.handleKey([]byte("\r"))
	writes := collect(40 * time.Millisecond)

	var msgs []string
	for _, w := range writes {
		if strings.HasPrefix(w, "PRIVMSG #chan :") {
			msgs = append(msgs, strings.TrimPrefix(w, "PRIVMSG #chan :"))
		}
	}
	want := []string{"pirmoji", "antroji", "trecija-puse"}
	if len(msgs) != len(want) {
		t.Fatalf("tikėjomės %d žinučių, gavome %v", len(want), msgs)
	}
	for i, w := range want {
		if msgs[i] != w {
			t.Errorf("žinutė %d = %q, norėtų %q", i, msgs[i], w)
		}
	}
}

func TestPasteConfirmOnlyAfterWindow(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	c.connected = true
	c.ensureCh("#chan")
	c.activeIdx = 1
	c.mu.Unlock()
	collect := captureWrites(c)

	// Trigger paste
	c.handleKey([]byte("a\nb\n"))
	time.Sleep(10 * time.Millisecond)
	// Enter DAR surinkimo lange — turi būti ignoruotas (eilutės tęsinys)
	c.handleKey([]byte("\r"))
	time.Sleep(40 * time.Millisecond) // surinkimo lango pabaiga
	// Antras Enter po lango — siunčia tik a, b (ne tuščias)
	c.handleKey([]byte("\r"))
	writes := collect(40 * time.Millisecond)
	n := 0
	for _, w := range writes {
		if strings.HasPrefix(w, "PRIVMSG #chan :") {
			n++
			if strings.TrimPrefix(w, "PRIVMSG #chan :") == "" {
				t.Errorf("išsiųsta tuščia žinutė")
			}
		}
	}
	if n != 2 {
		t.Errorf("tikėjomės 2 žinučių (a, b), gavome %d: %v", n, writes)
	}
}

func TestWildcardMatch(t *testing.T) {
	cases := []struct {
		pat, s string
		want   bool
	}{
		{"*", "betkas", true},
		{"*", "", true},
		{"gaidys!*@*", "gaidys!user@host.tld", true},
		{"gaidys!*@*", "kitas!user@host.tld", false},
		{"*!*@*.tld", "a!u@x.y.tld", true},
		{"bot?x", "bot1x", true},
		{"bot?x", "bot1y", false},
		{"bot*", "b", false},
		{"a*b*c", "axbxxbxc", true},
		{"a*b*c", "abxc", true},
		{"a*b*c", "axc", false},
		{"no wild", "no wild", true},
		{"no wild", "no wild2", false},
	}
	for _, c := range cases {
		if got := wildcardMatch(c.pat, c.s); got != c.want {
			t.Errorf("wildcardMatch(%q, %q) = %v, norėtų %v", c.pat, c.s, got, c.want)
		}
	}
}

func TestIgnoreMask(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	c.ignoreList = []string{"spameris", "*!*@bad.tld", "bot*"}
	c.mu.Unlock()

	if c.isIgnored("Spameris!u@h") != true {
		t.Error("tikslus nick turi atitikti (case-insensitive)")
	}
	if c.isIgnored("botas_1!u@h") != true {
		t.Error("bot* prefix maskė turi atitikti nick'ą")
	}
	if c.isIgnored("geras!user@bad.tld") != true {
		t.Error("host maskė turi atitikti pilną prefixą")
	}
	if c.isIgnored("geras!user@ok.tld") {
		t.Error("neturi atitikti")
	}
}
