package main

// Peržiūros 2026-10-21c (code-review prieš merge) regresijos testai.
//
// Radiniai ir pataisos:
//  1. Skaitiklio buferis 512 → maxReadBuffer (IRCv3 tag'ai viršija 512
//     biudžetą — kitaip kiekviena tag'ų turinti žinutė atmestama).
//  2. chanScrollOff sugrubinamas PRIEŠ naudojimą (Ctrl+End, switchCh,
//     handleMouse) — kitaip pelės kanalų pasirinkimas po overscroll'o
//     pasirenka ne tą kanalą.
//  3. nickEqual(cm=="ascii") suliejimas sutampa su nickSet raktais
//     (asciiFold) — kitaip QUIT/PART palieka „ghost“ nick'us.
//  4. 332 (tema), TOPIC, KICK nebekuria kanalo, kurio nesame — kaip 353.
//  5. QUIT kanalų paieška per rinkinį (O(1)); findNickIdx be alokacijų.
//  7. chanLimitHit įspėjimas sunaudojamas tik tada, kai įsimintas vardas
//     atitinka atmestą kanalą.
//  8. switchCh skaičiuoja matomas eilutes (visibleChannelRowsLocked) taip
//     pat, kaip draw() — su tema ir paieškos juosta.
//  9. isSecretArgument heuristika dabar realiai veikia („pass the“ —
//     pokalbis; „hunter2“ — slaptažodis).
// 10. WHO 352 eilutė: flagas atskirtas tarpu, kanalo stulpelis fitWidth.

import (
	"bufio"
	"fmt"
	"strings"
	"testing"
	"time"
)

// R1 — skaitiklio buferis praleidžia ilgas eilutes su IRCv3 tag'ais.
//
// Anksčiau buferis buvo maxLineBytes (512): serverio pridėti tag'ai
// (server-time) neįeina į eilutės 512 biudžetą, todėl ~550 baitų PRIVMSG
// sukelia ErrBufferFull — eilutė atmestama, pokalbis prarastas.
func TestReadLoopKeepsTaggedLines(t *testing.T) {
	c := newTestClient()
	longText := strings.Repeat("z", 500) // visa eilutė ~550 baitų
	tagged := fmt.Sprintf("@time=2026-10-21T12:00:00.000Z;msgid=abc123 :bob!u@h PRIVMSG #testas :%s", longText)
	if len(tagged) <= maxLineBytes {
		t.Fatalf("testo žinutė turi viršyti %d baitų (dabar %d)", maxLineBytes, len(tagged))
	}
	if len(tagged) > maxReadBuffer {
		t.Fatalf("testo žinutė turi tilpti į %d baitus (dabar %d)", maxReadBuffer, len(tagged))
	}

	reader := bufio.NewReaderSize(strings.NewReader(tagged+"\r\n"), maxReadBuffer)
	done := make(chan struct{})
	go func() {
		c.readLoop(reader)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("readLoop neužbaigė darbo")
	}

	ch := c.findCh("#testas")
	if ch == nil {
		t.Fatal("kanalas #testas nesukurtas")
	}
	found := false
	for _, m := range ch.messages {
		if m.nick == "bob" {
			found = true
		}
	}
	if !found {
		t.Error("BUG: ilga eilutė su tag'ais atmesta (ErrBufferFull) — pokalbis prarastas")
	}
}

// R1 — ilgesnė nei buferis eilutė vis tiek atmestama (DoS apsauga lieka).
func TestReadLoopDropsVeryLongLines(t *testing.T) {
	c := newTestClient()
	huge := ":srv PRIVMSG #testas :" + strings.Repeat("x", 20000)
	reader := bufio.NewReaderSize(strings.NewReader(huge+"\r\n"), maxReadBuffer)
	done := make(chan struct{})
	go func() {
		c.readLoop(reader)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("readLoop neužbaigė darbo")
	}
	if ch := c.findCh("#testas"); ch != nil && len(ch.messages) > 0 {
		t.Error("BUG: 20 KB eilutė pateko (turėtų būti atmesta)")
	}
}

// R2 — Ctrl+End offsetas SUGRUBINAMAS pagrįstai draw() matomų eilučių skaičiumi.
func TestChanScrollOffClampedOnCtrlEnd(t *testing.T) {
	c := newTestClient()
	for i := 0; i < 30; i++ {
		c.channels = append(c.channels, newChannel(fmt.Sprintf("#kanalas%d", i), 10))
	}
	c.activeIdx = 0
	c.height = 20

	c.handleKey([]byte("\x1b[1;5F")) // Ctrl+End

	c.mu.Lock()
	defer c.mu.Unlock()
	maxScroll := len(c.channels) - c.visibleChannelRowsLocked()
	if maxScroll < 0 {
		t.Fatal("testo sąranka: kanalų turi būti daugiau nei eilučių")
	}
	if c.chanScrollOff != maxScroll {
		t.Errorf("BUG: po Ctrl+End chanScrollOff = %d, norėtų %d — kitaip handleMouse spustelėjimas perskaičiuoja ne tą indeksą", c.chanScrollOff, maxScroll)
	}
	for i := range c.channels {
		// KIEKVIENAS matomas spustelėjimas turi atitikti MATOMĄ kanalą:
		idx := c.chanScrollOff + i
		if idx >= len(c.channels) {
			break
		}
		if idx < 0 || idx >= len(c.channels) {
			t.Errorf("BUG: matomoje eilutėje %d atsirastų neegzistuojantis kanalas (offset=%d)", i, c.chanScrollOff)
		}
	}
}

// R2 — spustelėjus pirmą/paskutinę MATOMĄ eilutę pasirenkamas MATOMAS kanalas.
func TestMouseClickSelectsVisibleChannel(t *testing.T) {
	c := newTestClient()
	for i := 0; i < 30; i++ {
		c.channels = append(c.channels, newChannel(fmt.Sprintf("#kanalas%d", i), 10))
	}
	c.activeIdx = 0
	c.height = 20
	c.mouse = true
	c.msgAreaTop = 5
	c.useTLS = false

	c.handleKey([]byte("\x1b[1;5F")) // Ctrl+End

	c.mu.Lock()
	rows := c.visibleChannelRowsLocked()
	off := c.chanScrollOff
	c.mu.Unlock()
	if off != len(c.channels)-rows {
		t.Fatalf("sąranka: off=%d, norėtų %d", off, len(c.channels)-rows)
	}

	// Paskutinė matoma eilutė (y 1-based) → paskutinis kanalas.
	c.handleMouse([]byte(fmt.Sprintf("\x1b[<0;3;%dm", c.msgAreaTop+rows-1)))
	c.mu.Lock()
	idx := c.activeIdx
	c.mu.Unlock()
	if idx != len(c.channels)-1 {
		t.Errorf("BUG: spustelėjus paskutinę matomą eilutę pasirinktas idx=%d, norėtų %d", idx, len(c.channels)-1)
	}

	// Pirmoji MATOMA eilutė → pirmas matomas kanalas (ne 0!).
	c.handleMouse([]byte(fmt.Sprintf("\x1b[<0;3;%dm", c.msgAreaTop)))
	c.mu.Lock()
	idx = c.activeIdx
	c.mu.Unlock()
	if idx != off {
		t.Errorf("BUG: spustelėjus pirmą matomą eilutę pasirinktas idx=%d, norėtų %d (offset)", idx, off)
	}
}

// R3 — CASEMAPPING=ascii: rinkinio raktai ir lygybė naudoja TĄ PATĮ fold'ą
// (asciiFold). QUIT pagal kitą raidę (ne fold'ą) nick'o NEPAŠALINA ir
// NEPALIEKA „ghost“ — įvyksta TIKSLIAI tai, ką rinkinys sako.
func TestAsciiCasemappingNickSetConsistency(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":srv 005 testnick CHANTYPES=# PREFIX=(qaohv)~&@%+ CASEMAPPING=ascii :is supported")
	c.parseIRC(":srv 001 testnick :welcome")
	c.parseIRC(":Žilvinas!u@h JOIN #testas")
	c.parseIRC(":žilvinas!u@h JOIN #testas")
	c.parseIRC(":žilvinas!u@h QUIT :bye") // tik „žilvinas“ (ascii fold išlaiko Ž)

	ch := c.findCh("#testas")
	if ch == nil {
		t.Fatal("kanalas nesukurtas")
	}
	if !ch.hasNick("Žilvinas") {
		t.Error("BUG: „Žilvinas“ pašalintas pagal KITĄ fold'ą — rinkinys ir lygybė sutampa, turi likti (CASEMAPPING=ascii)")
	}
	if ch.hasNick("žilvinas") {
		t.Error("BUG: „žilvinas“ QUIT nepašalintas (rinkinys laiko, lygybė atmesta — ghost nick'as)")
	}
	// RFC 1459 (default): didžiosios → mažosios suliejamos.
	c2 := newTestClient()
	c2.parseIRC(":B!u@h JOIN #chanas")
	c2.parseIRC(":b!u@h QUIT :bye")
	if ch2 := c2.findCh("#chanas"); ch2 != nil && ch2.hasNick("b") {
		t.Error("BUG: RFC 1459 fold'as sugedo — „b“ QUIT nepašalintas")
	}
}

// R4 — 332 (tema) nebekuria kanalo, kurio nesame.
func TestTopic332DoesNotCreateChannel(t *testing.T) {
	c := newTestClient()
	n := len(c.channels)
	for i := 0; i < 5; i++ {
		c.parseIRC(fmt.Sprintf(":srv 332 testnick #never-joined-%d :priešiška tema", i))
	}
	if len(c.channels) != n {
		t.Error("BUG: 332 srautas prikūrė kanalų")
	}
	// O egzistuojančio kanalo tema vis tiek pakeičiama.
	c.parseIRC(":usr!u@h JOIN #realas")
	c.parseIRC(":srv 332 testnick #realas :tikra tema")
	ch := c.findCh("#realas")
	if ch == nil {
		t.Fatal("kanalas #realas dingus")
	}
	if ch.topic != "tikra tema" {
		t.Errorf("BUG: egzistuojančio kanalo tema neatsidėjo (%q)", ch.topic)
	}
}

// R4 — TOPIC ir KICK taip pat nebekuria kanalų (ta pati srauto šeima).
func TestTopicKickDoNotCreateChannel(t *testing.T) {
	c := newTestClient()
	n := len(c.channels)
	c.parseIRC(":hostile!u@h TOPIC #klastotas :tema")
	c.parseIRC(":hostile!u@h KICK #klastotas aukas :priežastis")
	if len(c.channels) != n {
		t.Error("BUG: TOPIC/KICK prikūrė kanalų")
	}
}

// R5 — 5 000 QUIT'ų 5 000-nick kanale turi įvykti greitai (rinkinys, ne O(n²)
// su alokacija kas elementui).
func TestQuitFloodStaysFast(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":srv 005 testnick CHANTYPES=# PREFIX=(qaohv)~&@%+ :is supported")
	c.parseIRC(":me!u@h JOIN #bigas")
	ch := c.findCh("#bigas")
	if ch == nil {
		t.Fatal("kanalas nesukurtas")
	}
	for i := 0; i < 5000; i++ {
		ch.addNick(fmt.Sprintf("nick%04d", i))
	}
	// Dar 40 tuščių kanalų — kiekvienas QUIT perbėgs VISŲ kanalų sąrašą:
	for i := 0; i < 40; i++ {
		c.channels = append(c.channels, newChannel(fmt.Sprintf("#kitas%d", i), 10))
	}
	start := time.Now()
	for i := 0; i < 5000; i++ {
		c.parseIRC(fmt.Sprintf(":nick%04d!u@h QUIT :bai", i))
	}
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Errorf("BUG: 5 000 QUIT'ų užėmė %v — rinkinio paieška neveikia (O(n²))", elapsed)
	}
	if len(ch.nicks) >= 5000 {
		t.Errorf("BUG: QUIT'ai nepašalino nick'ų (liko %d)", len(ch.nicks))
	}
}

// R5 — prefikso turintis nick'as QUIT metu pašalinamas IŠ TEISINGOS vietos
// (rinkinys → indeksas; ne „pats pirmas atsitiktinis“).
func TestQuitRemovesPrefixedNick(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":a!u@h JOIN #chanas")
	c.parseIRC(":srv 353 testnick = #chanas :a @b c")
	c.parseIRC(":b!u@h QUIT :bye")
	ch := c.findCh("#chanas")
	if ch == nil {
		t.Fatal("kanalas nesukurtas")
	}
	if ch.hasNick("b") || ch.hasNick("@b") {
		t.Error("BUG: prefiksuoto nick'o („@b“) QUIT nepašalino")
	}
	if !ch.hasNick("a") || !ch.hasNick("c") {
		t.Error("BUG: kiti nick'ai nukentėjo")
	}
}

// R7 — klastoto tikslo atmetimas NESUNAUDOJA kito kanalo limito įspėjimo.
func TestLimitHitNotConsumedByForeignName(t *testing.T) {
	c := newTestClient()
	// Įsimintas įspėjimas (kaip /join kelyje palieka ensureCh).
	c.mu.Lock()
	c.chanLimitHit = "#tikraslimitas"
	n0 := len(c.channels)
	c.mu.Unlock()

	// Klastotas targas (tarpas → atmetimas ne dėl ribos).
	c.addMsg("#klastotas x", Message{ts: time.Now(), nick: "p", text: "labas"})

	c.mu.Lock()
	left := c.chanLimitHit
	c.mu.Unlock()
	if left != "#tikraslimitas" {
		t.Fatalf("BUG: klastoto tikslo atmetimas sunaudojo kito kanalo įspėjimą (liko %q)", left)
	}
	if got := len(c.channels) - n0; got != 0 {
		t.Fatalf("BUG: klastotas targas sukūrė kanalo (%d naujų)", got)
	}

	// Tas PATS kanalas: riba dabar pasiekiama — įspėjimas SUNAUDOJAMAS.
	// Pripildome REALIŲ kanalų iki maxChannels („*server*“ neskelabiamas:
	// jo išorės biudžetas neapskaito).
	c.mu.Lock()
	for countChannelsLockedTest(c) < maxChannels {
		c.channels = append(c.channels, newChannel(fmt.Sprintf("#pildymas%d", len(c.channels)), 10))
	}
	c.mu.Unlock()
	c.addMsg("#tikraslimitas", Message{ts: time.Now(), nick: "a", text: "labas"})
	c.mu.Lock()
	left = c.chanLimitHit
	c.mu.Unlock()
	if left != "" {
		t.Errorf("BUG: tą patį kanalą atmetus dėl ribos, įspėjimas nesunaudojtas (liko %q)", left)
	}
	// Ir įspėjimas apie „#tikraslimitas“ parašytas į *server* langą.
	warned := false
	if chS := c.findCh("*server*"); chS != nil {
		for _, m := range chS.messages {
			if strings.Contains(m.text, "#tikraslimitas") && strings.Contains(m.text, "limitas") {
				warned = true
			}
		}
	}
	if !warned {
		t.Error("BUG: ribos įspėjimas apie #tikraslimitas neparašytas")
	}
}

// countChannelsLockedTest — testo pagalbinė: realių kanalų skaičius
// („*server*“ neapskaitomas), kaip ensureCh naudoja.
func countChannelsLockedTest(c *Client) int {
	real, _ := c.countChannelsLocked()
	return real
}

// R8 — switchCh matomų eilučių skaičiavimas SUTAMPA su draw(): su tema —
// viena eilutė mažiau (msgAreaTop pasislenka); su paieška — dar viena.
func TestVisibleChannelRowsRespectsBars(t *testing.T) {
	c := newTestClient()
	for i := 0; i < 30; i++ {
		c.channels = append(c.channels, newChannel(fmt.Sprintf("#kanalas%d", i), 10))
	}
	c.activeIdx = 0
	c.height = 20

	c.mu.Lock()
	rowsPlain := c.visibleChannelRowsLocked()
	c.channels[0].topic = "ilga tema kuriai reikia savo juostos"
	rowsTopic := c.visibleChannelRowsLocked()
	c.searchMode = true
	rowsSearch := c.visibleChannelRowsLocked()
	c.mu.Unlock()

	if rowsPlain-rowsTopic != 1 {
		t.Errorf("BUG: su tema eilučių skaičius nesumažėjo per 1 (%d → %d)", rowsPlain, rowsTopic)
	}
	if rowsTopic-rowsSearch != 1 {
		t.Errorf("BUG: su paiešką eilučių skaičius nesumažėjo per 1 (%d → %d)", rowsTopic, rowsSearch)
	}

	// Ir switchCh sukomponuoja slinktį TARP pagal TIKRĄ juostų būseną:
	c.searchMode = false
	c.mu.Lock()
	c.chanScrollOff = 0
	c.mu.Unlock()
	c.handleKey([]byte("\x1b[F"))
	c.mu.Lock()
	off := c.chanScrollOff
	rows := c.visibleChannelRowsLocked()
	c.mu.Unlock()
	if off > len(c.channels)-rows {
		t.Errorf("BUG: slinktis po Ctrl+End = %d, bet paskutinis kanalas matomas tik kai off ≤ %d", off, len(c.channels)-rows)
	}
	if len(c.channels)-1 < off+rows && off != len(c.channels)-rows {
		t.Errorf("BUG: paskutinis kanalas ne matomoje lange (off=%d, rows=%d)", off, rows)
	}
}

// R9 — slaptažodžio heuristika: pokalbis NE slaptas, slaptažodis — slaptas.
func TestIsSecretArgumentHeuristics(t *testing.T) {
	notSecret := []string{"", "the", "me", "salt", "it", "labas", "pass the salt please"}
	for _, s := range notSecret {
		if isSecretArgument(s) {
			t.Errorf("isSecretArgument(%q) = true — įprastas trumpas žodis NETURĖTŲ būti slaptas", s)
		}
	}
	secret := []string{"hunter2", "Secret1", "my!pass", "labas123", "myverysecretpass"}
	for _, s := range secret {
		if !isSecretArgument(s) {
			t.Errorf("isSecretArgument(%q) = false — atrodo kaip slaptažodis", s)
		}
	}
	// ŽURNALO lygis: dviejų žodžių pokalbis „pass the“ dabar ĮRAŠOMAS.
	if isSensitiveLogLine("pass the") {
		t.Error("BUG: „pass the“ žurnale slaptinamas — pokalbis prarastas")
	}
	if !isSensitiveLogLine("pass hunter2") {
		t.Error("BUG: „pass hunter2“ žurnale NESLAPTINAMAS")
	}
}

// R10 — WHO 352: flagas atskirtas tarpu, stulpeliai nesulipę.
func TestWHOREPLYColumnSeparation(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":me!u@h JOIN #chanas")
	c.mu.Lock()
	c.whoChan = "#chanas"
	c.mu.Unlock()
	c.parseIRC(":srv 352 testnick #chanas b user host bob H :0 Bobas")
	ch := c.findCh("#chanas")
	if ch == nil {
		t.Fatal("kanalas nesukurtas")
	}
	var line string
	for _, m := range ch.messages {
		if strings.Contains(m.text, "bob") {
			line = m.text
		}
	}
	if line == "" {
		t.Fatal("WHO atsakymas nerastas")
	}
	if strings.Contains(line, "Hbob") {
		t.Errorf("BUG: flagas suliptas su nick'u: %q", line)
	}
	if !strings.Contains(line, " H bob") {
		t.Errorf("BUG: flagas ir nick turi būti atskirti tarpu: %q", line)
	}
}
