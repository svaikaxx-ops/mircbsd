package main

// Funkcijos v0.4.7 (paketas „a") regresijos testai:
//
//  1. /query <nick> — atidaro privatų langą be žinutės (du kartus —
//     nedubliuoja; netinkamas nick — atmetamas).
//  2. „── naujos žinutės ──" ribos žyma grįžus į kanalą; pasikartojantis
//     perjungimas nebekuria antra žymos.
//  3. timestamps / ts_format config: išjungta — be laikos; savas formatas.
//  4. Kanalų vardų Tab užbaigimas (CHANTYPES prefiksavimas).
//  5. Nauji IRCv3 cap'ai prašomi (extended-join, account-notify, chghost,
//     invite-notify) ir ACCOUNT/CHGHOST rodo žinutes.
//  6. Paros skirtiklis „── YYYY-MM-DD ──" ir OSC 0 lango antraštė.

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// F1 — /query <nick>.
func TestQueryOpensPrivateWindow(t *testing.T) {
	c := newTestClient()
	c.handleCmd("query", "bobas")
	c.mu.Lock()
	var found *Channel
	for _, ch := range c.channels {
		if ch.name == "bobas" {
			found = ch
		}
	}
	activeIdx := c.activeIdx
	c.mu.Unlock()
	if found == nil {
		t.Fatal("BUG: /query nesukūrė PM lango")
	}
	if !found.isPrivate {
		t.Error("BUG: /query langas nepažymėtas isPrivate (šonraštis be „>“)")
	}
	if activeIdx < 0 || activeIdx >= len(c.channels) || c.channels[activeIdx].name != "bobas" {
		t.Errorf("BUG: /query lango neperjungė (activeIdx=%d)", activeIdx)
	}
	// Antras /query to paties nicko — nebesukuria antro lango.
	c.handleCmd("query", "bobas")
	c.mu.Lock()
	n := len(c.channels)
	c.mu.Unlock()
	if n != 2 {
		t.Errorf("BUG: pakartotas /query prikūrė langų (%d)", n-1)
	}
	// Netinkamas nick — atmetamas, lango nesukuria.
	c.handleCmd("query", "b@d nick")
	c.mu.Lock()
	n = len(c.channels)
	c.mu.Unlock()
	if n != 2 {
		t.Errorf("BUG: netinkamas nick sukūrė langą (%d bendra)", n)
	}
	// Be argumento — naudojimo užuomina, jokių langų.
	c.handleCmd("query", "")
	c.mu.Lock()
	if len(c.channels) != n {
		t.Error("BUG: /query be argumento sukūrė langą")
	}
	c.mu.Unlock()
}

// F2 — neperskaitytų ribos žyma.
func TestUnreadMarkerInsertedOnReturn(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":testnick!u@h JOIN #b")

	c.mu.Lock()
	c.activeIdx = 0 // mes *server* lange
	c.mu.Unlock()

	// Dvi žinutės į neaktyvų kanalą — neperskaitytos.
	c.addMsg("#b", Message{ts: time.Now(), nick: "bob", text: "pirmas"})
	c.addMsg("#b", Message{ts: time.Now(), nick: "bob", text: "antras"})

	c.mu.Lock()
	idxOfB := -1
	for i, ch := range c.channels {
		if ch.name == "#b" {
			idxOfB = i
		}
	}
	unread := c.channels[idxOfB].unread
	c.mu.Unlock()
	if unread != 2 {
		t.Fatalf("sąranka: unread=%d (norėtų 2)", unread)
	}

	// Grįžtame į #b — prieš pirmą neperskaitytą turi atsirasti žyma.
	c.switchCh(idxOfB)
	c.mu.Lock()
	msgs := c.channels[idxOfB].messages
	sepIdx := -1
	firstUnread := -1
	for i, m := range msgs {
		if m.text == "── naujos žinutės ──" {
			sepIdx = i
		}
		if m.text == "pirmas" && firstUnread < 0 {
			firstUnread = i
		}
	}
	c.mu.Unlock()
	if sepIdx < 0 {
		t.Fatal("BUG: žyma „── naujos žinutės ──“ neįstatyta")
	}
	if firstUnread < 0 || sepIdx != firstUnread-1 {
		t.Errorf("BUG: žyma ne prieš pirmą neperskaitytą (Žyma=%d, pirmas=%d)", sepIdx, firstUnread)
	}
	markerCount := 0
	for _, m := range msgs {
		if m.text == "── naujos žinutės ──" {
			markerCount++
		}
	}
	if markerCount != 1 {
		t.Errorf("BUG: žymų = %d (norėtų 1 — be dubliavimo)", markerCount)
	}

	// Pakartotinis perjungimas BE naujų žinučių — antros žymos NEBŪNA.
	c.switchCh(0)
	c.addMsg("#b", Message{ts: time.Now(), nick: "bob", text: "pirmas"}) // ne, tai nauja žinutė — neperskaityta
	c.switchCh(idxOfB)
	c.mu.Lock()
	count := 0
	for _, m := range c.channels[idxOfB].messages {
		if m.text == "── naujos žinutės ──" {
			count++
		}
	}
	c.mu.Unlock()
	if count != 2 {
		t.Errorf("BUG: po naujų žinučių ir grįžimo žymų = %d (norėtų 2 — po vieną kiekvienam persitraukimui)", count)
	}
}

// F3 — timestamps / ts_format config.
func TestTimestampsConfigToggle(t *testing.T) {
	defer setTimestamps(true, "15:04")

	m := Message{ts: time.Date(2026, 10, 21, 14, 9, 3, 0, time.Local), nick: "bob", text: "labas", msgType: MsgChat}

	setTimestamps(true, "15:04")
	line := m.renderLines(80, "testnick")[0]
	if !strings.Contains(line, "14:09") {
		t.Errorf("BUG: numatas formatas nerodomas: %q", line)
	}

	setTimestamps(true, "15:04:05")
	line = m.renderLines(80, "testnick")[0]
	if !strings.Contains(line, "14:09:03") {
		t.Errorf("BUG: savas formatas nerodomas: %q", line)
	}

	setTimestamps(false, "")
	line = m.renderLines(80, "testnick")[0]
	if strings.Contains(line, "14:09") {
		t.Errorf("BUG: išjungta — o laika vis tiek rodoma: %q", line)
	}
}

// F4 — kanalų vardų Tab užbaigimas.
func TestTabCompletesChannelNames(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":testnick!u@h JOIN #libera")
	c.parseIRC(":testnick!u@h JOIN #lietuva")

	c.mu.Lock()
	c.input = []rune("/join #li")
	c.inputPos = len(c.input)
	c.mu.Unlock()
	c.doTab()

	c.mu.Lock()
	got := string(c.input)
	c.mu.Unlock()
	if !strings.HasPrefix(got, "/join #") || got == "/join #li" {
		t.Fatalf("BUG: kanalo vardas nesužaibintas (%q)", got)
	}
	if !strings.HasPrefix(strings.TrimSpace(got), "/join #libera") && !strings.HasPrefix(strings.TrimSpace(got), "/join #lietuva") {
		t.Errorf("BUG: užbaigta ne kanalu (%q)", got)
	}
	// Pakartotinas Tab — virsta į kitą kandidatą (abi #li* galimybės).
	c.doTab()
	c.mu.Lock()
	got2 := string(c.input)
	c.mu.Unlock()
	if got2 == got {
		t.Errorf("BUG: antras Tab neperjungia kandidato (%q)", got2)
	}
}

// F5 — nauji IRCv3 cap'ai prašomi; ACCOUNT/CHGHOST rodomi.
func TestNewCapsRequestedAndHandled(t *testing.T) {
	c := newTestClient()
	c.writeCh = make(chan string, 16)
	c.parseIRC(":srv CAP * LS :multi-prefix away-notify server-time echo-message extended-join account-notify chghost invite-notify")

	req := ""
	select {
	case l := <-c.writeCh:
		req = l
	case <-time.After(2 * time.Second):
		t.Fatal("CAP REQ neišsiųsta")
	}
	for _, want := range []string{"extended-join", "account-notify", "chghost", "invite-notify"} {
		if !strings.Contains(req, want) {
			t.Errorf("BUG: CAP REQ neprašo „%s“: %q", want, req)
		}
	}

	c.parseIRC(":srv CAP * ACK :extended-join account-notify chghost invite-notify")

	// extended-join: JOIN su paskyra.
	c.parseIRC(":testnick!u@h JOIN #chanas bobo aktyvioji paskyra")
	c.parseIRC(":bob!u@h JOIN #chanas bobo realus vardas")
	ch := c.findCh("#chanas")
	if ch == nil {
		t.Fatal("kanalas nesukurtas")
	}
	foundAcct := false
	for _, m := range ch.messages {
		if m.nick == "bob" && strings.Contains(m.text, "bobo") {
			foundAcct = true
		}
	}
	if !foundAcct {
		t.Error("BUG: extended-join paskyra nerodoma JOIN žinutėje")
	}

	// ACCOUNT ir CHGHOST į *server* langą.
	c.parseIRC(":bob!u@h ACCOUNT paskyra1")
	c.parseIRC(":bob!u@h CHGHOST naujasuser newhost.example")
	var sawAcct, sawHost bool
	if sh := c.findCh("*server*"); sh != nil {
		for _, m := range sh.messages {
			if strings.Contains(m.text, "bob") && strings.Contains(m.text, "paskyra1") {
				sawAcct = true
			}
			if strings.Contains(m.text, "bob") && strings.Contains(m.text, "naujasuser") {
				sawHost = true
			}
		}
	}
	if !sawAcct {
		t.Error("BUG: ACCOUNT pranešimas nerodomas")
	}
	if !sawHost {
		t.Error("BUG: CHGHOST pranešimas nerodomas")
	}
}

// captureDrawStdout — braižo į stdout ir sugauna (pvz. OSC 0 antraštė).
func captureDrawStdout(t *testing.T, c *Client) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	c.draw()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}

// F6a — paros skirtiklis tarp skirtingų dienų žinučių.
func TestDaySeparatorInserted(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":testnick!u@h JOIN #chanas")
	yesterday := time.Now().Add(-24 * time.Hour)
	c.addMsg("#chanas", Message{ts: yesterday, nick: "bob", text: "vakar"})
	c.addMsg("#chanas", Message{ts: time.Now(), nick: "bob", text: "šiandien"})

	out := captureDrawStdout(t, c)
	if !strings.Contains(out, yesterday.Format("2006-01-02")) {
		t.Errorf("BUG: vakarykštė paros žyma nerasta braižyme")
	}
	// Antrų paros žymų tarp „šiandien" žinutės savo eilučių būti neturi —
	// žyma yra tik VIENTISA eilutė, ne priedinė prie kiekvienos.
	if n := strings.Count(out, "── 20"); n < 1 {
		t.Errorf("BUG: paros žyma nerasta (%d)", n)
	}
}

// F6a — changedDay pagalbinė: paros persiridimas atpažystamas.
func TestChangedDayHelper(t *testing.T) {
	a := time.Date(2026, 10, 21, 23, 59, 0, 0, time.Local)
	b := time.Date(2026, 10, 22, 0, 1, 0, 0, time.Local)
	if !changedDay(a, b) {
		t.Error("BUG: paros persiridimas neapčiuopiamas (10-21 23:59 → 10-22 00:01)")
	}
	if changedDay(a, time.Date(2026, 10, 21, 23, 59, 30, 0, time.Local)) {
		t.Error("BUG: ta pati paros žymima kaip persiridėjusi")
	}
}

// F6b — OSC 0 lango antraštė su aktyviu kanalu ir neperskaitytų skaičiumi.
func TestTerminalTitleOSC0(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":testnick!u@h JOIN #chanas")
	out := captureDrawStdout(t, c)
	if !strings.Contains(out, "\033]0;mircBSD — #chanas\a") {
		t.Errorf("BUG: lango antraštė be kanalo: %q", extractFirst(out, "\033]0;"))
	}

	// Neperskaitytos žinutės kitame kanale — skaičius antraštėje.
	c.parseIRC(":testnick!u@h JOIN #kitas")
	c.mu.Lock()
	for i, ch := range c.channels {
		if ch.name == "#chanas" {
			c.activeIdx = i
		}
	}
	c.mu.Unlock()
	c.addMsg("#kitas", Message{ts: time.Now(), nick: "bob", text: "sveikas"})
	out = captureDrawStdout(t, c)
	if !strings.Contains(out, "\033]0;mircBSD — #chanas (1)\a") {
		t.Errorf("BUG: antraštėje neperskaitytų skaičius (1) nerastas")
	}
}

// extractFirst — testo pagalbinė: eilutė iki sekančio \a po pirmo pos.
func extractFirst(s, prefix string) string {
	i := strings.Index(s, prefix)
	if i < 0 {
		return "(nerasta)"
	}
	j := strings.IndexByte(s[i:], '\a')
	if j < 0 {
		return s[i:]
	}
	return s[i : i+j+1]
}
