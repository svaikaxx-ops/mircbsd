package main

// Klavišų apdorojimo testai — Ctrl+Home/End alias'ai ir sekų buferiavimas.

import "testing"

func setupScrollClient(t *testing.T) *Client {
	c := newTestClient()
	for i := 0; i < 30; i++ {
		c.channels = append(c.channels, newChannel(newTestChanName(i), 10))
	}
	c.activeIdx = 0
	c.height = 20
	return c
}

func newTestChanName(i int) string {
	return "#kanalas" + string(rune('0'+i%10))
}

func chanScrollOffOf(c *Client) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.chanScrollOff
}

// K1 — Visos Ctrl+Home formos: \x1b[1;5H (xterm), \x1b[1;6H (Ctrl+Shift),
// \x1b[5H (trumpoji) → chanScrollOff = 0.
func TestCtrlHomeForms(t *testing.T) {
	for _, seq := range []string{"\x1b[1;5H", "\x1b[1;6H", "\x1b[5H"} {
		c := setupScrollClient(t)
		// Nukreipiam slinktį į apačią, po to tikriname, kad nukėlė į pradžią.
		c.handleKey([]byte("\x1b[1;5F"))
		if off := chanScrollOffOf(c); off == 0 {
			t.Fatalf("sąranka: prieš %q slinktis jau buvo 0", seq)
		}
		c.handleKey([]byte(seq))
		if off := chanScrollOffOf(c); off != 0 {
			t.Errorf("BUG: %q nepaskatino slinkties į pradžią (off=%d)", seq, off)
		}
	}
}

// K2 — Visos Ctrl+End formos: \x1b[1;5F, \x1b[1;6F, \x1b[5F → slinktis iki
// APAČIOS (sugrubinta), ir Ctrl+PgUp/PgDn (\x1b[5;5~ /\x1b[6;5~).
func TestCtrlEndAndPgForms(t *testing.T) {
	for _, seq := range []string{"\x1b[1;5F", "\x1b[1;6F", "\x1b[5F", "\x1b[6;5~"} {
		c := setupScrollClient(t)
		c.handleKey([]byte(seq))
		c.mu.Lock()
		off := c.chanScrollOff
		rows := c.visibleChannelRowsLocked()
		c.mu.Unlock()
		maxScroll := len(c.channels) - rows
		if off != maxScroll {
			t.Errorf("BUG: %q chanScrollOff = %d, norėtų %d", seq, off, maxScroll)
		}
	}
	// Ctrl+PgUp — į viršūnę.
	c := setupScrollClient(t)
	c.handleKey([]byte("\x1b[1;5F")) // į apačią
	c.handleKey([]byte("\x1b[5;5~")) // Ctrl+PgUp
	if off := chanScrollOffOf(c); off != 0 {
		t.Errorf("BUG: Ctrl+PgUp nepaskatino slinkties į pradžią (off=%d)", off)
	}
}

// K3 — Shift+PgUp/PgDn modifikatoriai NEPAKEIČIA žinutės slinkties
// (tik Ctrl formos (+Pg) valdo kanalų sąrašą).
func TestShiftPgDnStillMessageScroll(t *testing.T) {
	c := setupScrollClient(t)
	// Shift+PgDn (\x1b[6;2~) → žinutės apačion (scrollOff = 0 — jau yra).
	c.handleKey([]byte("\x1b[6;2~"))
	if off := chanScrollOffOf(c); off != 0 {
		t.Errorf("BUG: Shift+PgDn pataikė į kanalų slinktį (off=%d)", off)
	}
}

// K4 — Įvesties eigoje Home/End su TEKSTU judina žymeklį (regresija —
// naujas „tuščios įvesties“ atsarginis negali pakeisti normalios).
func TestHomeEndWithTextKeepsCursor(t *testing.T) {
	c := newTestClient()
	c.handleInputText(t, "labas pasaulis")
	c.handleKey([]byte("\x1b[1;5C")) // Ctrl+Right iki galo
	c.handleKey([]byte("\x1b[H"))
	c.mu.Lock()
	pos := c.inputPos
	off := c.chanScrollOff
	c.mu.Unlock()
	if pos != 0 {
		t.Errorf("BUG: Home su tekstu nejudino žymeklio į pradžią (pos=%d)", pos)
	}
	if off != 0 {
		t.Errorf("BUG: Home su tekstu paveikė kanalų slinktį (off=%d)", off)
	}
	c.handleKey([]byte("\x1b[F"))
	c.mu.Lock()
	pos = c.inputPos
	c.mu.Unlock()
	if pos != len("labas pasaulis") {
		t.Errorf("BUG: End su tekstu nejudino žymeklio į pabaigą (pos=%d)", pos)
	}
}

// K5 — Paprastas Home/End su TUŠČIA įvestimi slenka kanalų sąrašą
// (atsarginė forma terminalams be Ctrl modifikatoriaus).
func TestHomeEndEmptyInputScrollsChannels(t *testing.T) {
	c := setupScrollClient(t)
	c.handleKey([]byte("\x1b[1;5F")) // į apačią
	c.handleKey([]byte("\x1b[H"))    // tuščia įvestis → į pradžią
	if off := chanScrollOffOf(c); off != 0 {
		t.Errorf("BUG: Home (tuščia įvestis) nepaskatino slinkties į pradžią (off=%d)", off)
	}
	c.handleKey([]byte("\x1b[F")) // tuščia įvestis → į apačią
	c.mu.Lock()
	defer c.mu.Unlock()
	maxScroll := len(c.channels) - c.visibleChannelRowsLocked()
	if c.chanScrollOff != maxScroll {
		t.Errorf("BUG: End (tuščia įvestis) chanScrollOff = %d, norėtų %d", c.chanScrollOff, maxScroll)
	}
}

// K6 — Sekos buferiavimas: per skaitymus susiskirčiusi Ctrl+Home seka turi
// veikti (ankstesnė versija prarasdavo pirmą gabalą).
func TestSplitEscapeSequenceBuffered(t *testing.T) {
	// escComplete: nebaigtos — false.
	incomplete := [][]byte{
		[]byte("\x1b"),     // ne — atskiras ESC yra BAIGTAS; čia patikrinsime atskirai
		[]byte("\x1b["),    // CSI be finalo
		[]byte("\x1b[1;5"), // Ctrl modifikatorius be finalo
		[]byte("\x1bO"),    // SS3 be simbolio
	}
	for _, b := range incomplete {
		if b[0] == 0x1b && len(b) == 1 {
			continue // atskiras ESC — baigtas (žr. žemiau)
		}
		if escComplete(b) {
			t.Errorf("BUG: escComplete(%q) = true — turi būti false (tęsis)", b)
		}
	}
	// Atskiras ESC — BAIGTAS.
	if !escComplete([]byte("\x1b")) {
		t.Error("BUG: escComplete(ESC) = false — Esc klavišas dingtų")
	}
	// Pilnos — true.
	for _, s := range []string{"\x1b[1;5H", "\x1b[1;5F", "\x1b[A", "\x1bOP", "\x1bX", "a", "\x1b[0;3;5m"} {
		if !escComplete([]byte(s)) {
			t.Errorf("BUG: escComplete(%q) = false — seka turi būti baigta", s)
		}
	}
}

// K7 — escComplete neprilimpa prie pilno eigoje įrašo (ne ESC seka).
func TestEscCompletePlainBytes(t *testing.T) {
	if !escComplete([]byte("labas\r")) {
		t.Errorf("BUG: escComplete(„labas\\r“) = false — įprasti baitai turi būti baigti")
	}
}

// handleInputText — testo pagalbinė: įrašo tekstą į įvesties eilutę.
func (c *Client) handleInputText(t *testing.T, text string) {
	t.Helper()
	c.mu.Lock()
	c.input = []rune(text)
	c.inputPos = len(c.input)
	c.mu.Unlock()
}
