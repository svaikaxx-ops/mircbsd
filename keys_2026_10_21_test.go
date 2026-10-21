package main

// Klavišų apdorojimo testai — sekų buferiavimas ir Home/End regresija.

import "testing"

func scrollTestClient(t *testing.T) *Client {
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

// K1 — Ctrl+↑↓ vis dar nick scroll (nieko nepranikę per Ctrl+Home/End
// pašalinimą).
func TestCtrlArrowsStillWork(t *testing.T) {
	c := scrollTestClient(t)
	c.handleKey([]byte("\x1b[1;5B")) // Ctrl+Down — nick scroll žemyn
	c.mu.Lock()
	scrolled := c.nickScrollOff
	c.mu.Unlock()
	if scrolled != 1 {
		t.Errorf("BUG: Ctrl+Down nick scroll neveikia (nickScrollOff=%d)", scrolled)
	}
	c.handleKey([]byte("\x1b[1;5A")) // Ctrl+Up — atgal
	c.mu.Lock()
	scrolled = c.nickScrollOff
	c.mu.Unlock()
	if scrolled != 0 {
		t.Errorf("BUG: Ctrl+Up nick scroll neveikia (nickScrollOff=%d)", scrolled)
	}
}

// K2 — Home/End BEVEIKA nekeičia kanalų slinkties (Ctrl+Home/End pašalinta —
// kanalų slinkties valdymas nebėra klavišuose; sąrašas paskui vartotoją
// suseka automatiškai per switchCh).
func TestHomeEndDoNotScrollChannels(t *testing.T) {
	c := scrollTestClient(t)
	// Sąranka: slinktis apačion (kaip po automatinio susekimo).
	c.mu.Lock()
	c.chanScrollOff = len(c.channels)
	c.clampChanScrollLocked()
	target := c.chanScrollOff
	c.mu.Unlock()

	c.handleKey([]byte("\x1b[1;5H")) // Ctrl+Home — NEBE klausoma
	c.handleKey([]byte("\x1b[1;5F")) // Ctrl+End — NEBE klausoma
	c.handleKey([]byte("\x1b[H"))    // Home
	c.handleKey([]byte("\x1b[F"))    // End
	c.handleKey([]byte("\x1b[5;5~")) // „Ctrl+PgUp“ — NEBE kanalams
	c.handleKey([]byte("\x1b[6;5~")) // „Ctrl+PgDn“ — NEBE kanalams
	if off := chanScrollOffOf(c); off != target {
		t.Errorf("BUG: Home/End/Ctrl+Home/End keičia slinktį (%d → %d), o neturėtų", target, off)
	}
	// Su tekstu Home/End judina žymeklį.
	c.handleInputText(t, "labas pasaulis")
	c.handleKey([]byte("\x1b[H"))
	c.mu.Lock()
	pos := c.inputPos
	c.mu.Unlock()
	if pos != 0 {
		t.Errorf("BUG: Home su tekstu nejudino žymeklio į pradžią (pos=%d)", pos)
	}
	c.handleKey([]byte("\x1b[F"))
	c.mu.Lock()
	pos = c.inputPos
	c.mu.Unlock()
	if pos != len("labas pasaulis") {
		t.Errorf("BUG: End su tekstu nejudino žymeklio į pabaigą (pos=%d)", pos)
	}
}

// K3 — SwitchCh sąrašą suka paskui aktyvų kanalą (atsakingas už „visi
// kanalai pasiekiami“ ir be rankinės slinkties).
func TestSwitchChKeepsChannelVisible(t *testing.T) {
	c := scrollTestClient(t)
	c.switchCh(len(c.channels) - 1) // paskutinis kanalas
	c.mu.Lock()
	defer c.mu.Unlock()
	rows := c.visibleChannelRowsLocked()
	off := c.chanScrollOff
	lastInWindow := off + rows - 1
	if c.activeIdx < off || c.activeIdx > lastInWindow {
		t.Errorf("BUG: paskutinis kanalas (idx=%d) ne matomoje lange (%d..%d)", c.activeIdx, off, lastInWindow)
	}
}

// K4 — shift+PgDn lieka žinutės slinkčiai (nieko neperimama).
func TestShiftPgDnStillMessageScroll(t *testing.T) {
	c := scrollTestClient(t)
	c.handleKey([]byte("\x1b[6;2~"))
	if off := chanScrollOffOf(c); off != 0 {
		t.Errorf("BUG: Shift+PgDn pataikė į kanalų slinktį (off=%d)", off)
	}
}

// K5 — escComplete: nebaigtos sekos — false (laukiame tęsinio).
func TestEscCompleteIncomplete(t *testing.T) {
	incomplete := []string{"\x1b[", "\x1b[1;5", "\x1bO"}
	for _, s := range incomplete {
		if escComplete([]byte(s)) {
			t.Errorf("BUG: escComplete(%q) = true — turi būti false (tęsis)", s)
		}
	}
}

// K6 — escComplete: atskiras ESC IR pilnos sekos — true.
func TestEscCompleteComplete(t *testing.T) {
	for _, s := range []string{"\x1b", "\x1b[1;5H", "\x1b[1;5F", "\x1b[A", "\x1bOP", "\x1bX", "a", "\x1b[0;3;5m"} {
		if !escComplete([]byte(s)) {
			t.Errorf("BUG: escComplete(%q) = false — seka turi būti baigta", s)
		}
	}
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
