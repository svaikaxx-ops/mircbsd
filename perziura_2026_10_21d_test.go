package main

// Peržiūros 2026-10-21d (pilna atskito peržiūros po merge) regresijos
// testai:
//
//  1. findChunk: strings.Index grąžina BAITŲ indeksą, bet
//     plainToOriginalBounds skaičiuoja RUNE indeksu — apvyniotos
//     ne-ASCII (lietuviškos) žinutės tęsinys rodydavo nuslystus gabalus
//     („begalingai" → „galingai š").
//  2. CAP ACK „:-sasl" NEAKTYVUOJA sasl'o ir NEPRADĖJA AUTHENTICATE
//     dialogo be mūsų prašymo (slaptažodžio nutekėjimo kelias uždarytas).
//  3. echo-message: savo /me AIDAS nerodomas dukart (bet kito vartotojo
//     /me vis dar rodomas).
//  5. MODE į neprijungtą kanalą NEBEKURIA lango (kaip 332/353).
//  6. 251 rodomas pilnas galinis parametras, o ne supjausdytas į tris.
//  7. 322 (LIST) stulpeliai pagal LĄSTELIŲ plotį (padRight, ne %-30s).
//  8. statuso juostos statusLeft nukirpiamas iki laisvos vietos
//     (clampStatusLeft), unread badge nebe išlenda už „│".

import (
	"fmt"
	"strings"
	"testing"
)

// D1 — apvyniota lietuviška žinutė: kiekvienos eilutės gabalas atkuria
// TĄ PATĮ tekstą, o ne „nuslystus" rašmenis.
func TestFindChunkLithuanianWrapped(t *testing.T) {
	text := "žąsis begalingai šokinėja"
	chunks := wrapText(text, 12)
	if len(chunks) < 2 {
		t.Fatalf("testo sąranka: turėtų būti >=2 eilutės (gavo %d)", len(chunks))
	}
	rebuilt := ""
	for i, chunk := range chunks {
		if i > 0 {
			rebuilt += " "
		}
		rebuilt += findChunk(text, chunk)
	}
	if rebuilt != text {
		t.Errorf("BUG: apvyniota lietuviška žinutė praranda raides:\n  originalas: %q\n  gauta:      %q", text, rebuilt)
	}
}

// D1 — tas pat su formatavimo kodus (spalvos kodas turi likti su savo tekstu).
func TestFindChunkLithuanianWithColor(t *testing.T) {
	text := "\x033žąsis begalingai šokinėja\x0f"
	chunks := wrapText(stripIRCFormat(text), 12)
	if len(chunks) < 2 {
		t.Fatalf("testo sąranka: turėtų būti >=2 eilutės (gavo %d)", len(chunks))
	}
	for i, chunk := range chunks {
		rendered := findChunk(text, chunk)
		plain := stripIRCFormat(rendered)
		if plain != strings.Replace(chunk, "\n", "", -1) && plain != strings.TrimSpace(chunk) {
			t.Errorf("BUG: eilutė %d prarado raides: chunk %q → rodo %q", i, strings.TrimSpace(chunk), plain)
		}
	}
}

// D2 — atsisakymo ACK „CAP * ACK :-sasl" PAŠALINA cap'ą (neaktyvuoja) ir
// NEPRADĖJA AUTHENTICATE dialogo be mūsų prašymo.
func TestCapAckRejectPrefixRemovesCap(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	c.activeCaps["sasl"] = true // kažkada buvo įjungtas — dabar atsisakoma
	c.mu.Unlock()

	c.parseIRC(":srv CAP * ACK :-sasl")

	c.mu.Lock()
	saslActive := c.activeCaps["sasl"]
	saslPending := c.saslPending
	c.mu.Unlock()
	if saslActive {
		t.Error("BUG: atsisakymo ACK („-sasl“) paliko sasl'o AKTYVIU — turi pašalinti")
	}
	if saslPending {
		t.Error("BUG: atsisakymo ACK įjungė saslPending — AUTHENTICATE būtų išsiųstas")
	}
}

// D2 — serverio savavališkas „CAP * ACK :sasl" BE mūsų prašymo (capSASL
// nustatomas tik kai patys užsakome ir turim slaptažodį) NEPRADĖJA SASL.
func TestCapAckUnsolicitedSaslDoesNotAuthenticate(t *testing.T) {
	c := newTestClient() // be saslPass, be capSASL
	c.parseIRC(":srv CAP * ACK :sasl")

	c.mu.Lock()
	saslPending := c.saslPending
	c.mu.Unlock()
	if saslPending {
		t.Error("BUG: serverio savavališkas „ACK sasl“ be mūsų prašymo įjungė saslPending")
	}
}

// D3 — echo-message: savo ACTION aidas nespausdinamas dukart; kito
// vartotojo ACTION vis tiek rodomas.
func TestEchoOwnActionNotDuplicated(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	c.activeCaps["echo-message"] = true
	c.nick = "testnick"
	c.mu.Unlock()

	c.parseIRC(":me!u@h JOIN #chanas")
	c.parseIRC(":testnick!u@h PRIVMSG #chanas :\x01ACTION kavos puodelis\x01")
	c.parseIRC(":bob!u@h PRIVMSG #chanas :\x01ACTION arbata\x01")

	ch := c.findCh("#chanas")
	if ch == nil {
		t.Fatal("kanalas nesukurtas")
	}
	own := 0
	others := false
	for _, m := range ch.messages {
		if m.text == "kavos puodelis" && m.nick == "testnick" {
			own++
		}
		if m.text == "arbata" && m.nick == "bob" {
			others = true
		}
	}
	if own != 0 {
		t.Errorf("BUG: savo ACTION aidas atspausdintas %d kartus (echo-message aktyvus — turi būti 0)", own)
	}
	if !others {
		t.Error("BUG: kito vartotojo ACTION dingsta (per didelis filtras)")
	}
}

// D5 — MODE į neprijungtą kanalą NEBEKURIA lango.
func TestModeUnknownChannelDoesNotCreateWindow(t *testing.T) {
	c := newTestClient()
	n := len(c.channels)
	for i := 0; i < 5; i++ {
		c.parseIRC(fmt.Sprintf(":hostile!u@h MODE #naujas%d +o kažkas", i))
	}
	if len(c.channels) != n {
		t.Errorf("BUG: MODE srautas prikūrė %d langų", len(c.channels)-n)
	}
}

// D6 — 251: pilnas galinis parametras rodomas kaip yra, be „paskaidymo".
func TestLuserClient251ShowsFullMessage(t *testing.T) {
	c := newTestClient()
	c.parseIRC(":srv 251 testnick :There are 24 users and 8 invisible on 3 servers")

	var line string
	if ch := c.findCh("*server*"); ch != nil {
		for _, m := range ch.messages {
			if strings.Contains(m.text, "users") {
				line = m.text
			}
		}
	}
	if line == "" {
		t.Fatal("251 žinutė nerasta")
	}
	if strings.Contains(line, "vartotojų") {
		t.Errorf("BUG: 251 vis dar paskaidytas į tuščius laukus („vartotojų, ,  kanalų“): %q", line)
	}
}

// D7 — 322 (LIST): stulpeliai pagal LĄSTELIŲ plotį — CJK kanalo vardas
// (kurių VIENA runę užima 2 ląsteles) nepastums kiekio stulpelio.
func TestList322ColumnWidthCells(t *testing.T) {
	c := newTestClient()
	wide := "#日本語" // 3 runės, 6 ląstelės
	c.parseIRC(fmt.Sprintf(":srv 322 testnick %s 42 :tema", wide))

	var line string
	if ch := c.findCh("*server*"); ch != nil {
		for _, m := range ch.messages {
			if strings.Contains(m.text, wide) {
				line = m.text
			}
		}
	}
	if line == "" {
		t.Fatal("322 žinutė nerasta")
	}
	countIdx := strings.Index(line, "42")
	if countIdx < 0 {
		t.Fatal("kiekio stulpelis nerastas")
	}
	// Tikslus formatas: „  " + 30 ląstelių + " " + %5s („  42“).
	cells := stringWidth(line[:countIdx])
	if cells != 36 {
		t.Errorf("BUG: ląstelių iki kiekio stulpelio = %d, norėtų 36 (30 ląstelių vardui): %q — stulpelis skaičiuojamas runes, ne ląsteles", cells, line)
	}
}

// D8 — clampStatusLeft: ilgas statuso kairysis kraštas nukirpiamas, eilutė
// telpa plote.
func TestClampStatusLeftFitsWidth(t *testing.T) {
	right := " PgUp/Dn:scroll Ctrl+N/P:kan Ctrl+←→:žodis Tab:AC /help "
	left := " mircBSD | nick @ serveris | " + strings.Repeat("Ž", 80) + " "
	w := 100
	got, pad := clampStatusLeft(left, right, w)
	total := stringWidth(got) + pad + stringWidth(right)
	if total > w {
		t.Errorf("BUG: statuso juosta %d ląstelių, o ekranas %d — perlenkta", total, w)
	}
	if strings.Count(left, "Ž") == 0 || got == left {
		// Kirpimas turi įvykti (80 „Ž“ ženkliai per didelė 100 ekrane prie
		// statusRight).
		if got == left {
			t.Errorf("BUG: statusLeft nenukirptas po %d ląstelių juostoje", stringWidth(got))
		}
	}
}
