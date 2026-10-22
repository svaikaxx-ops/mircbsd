package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ---- Klavišai ----

// readKeys skaito terminalo įvestį ir perduoda handleKey.
//
// SEKŲ BUFERIAVIMAS: read() grąžina baitus, kurie pasiskirsto atsitiktinai
// — kai kurie terminalai ilgą valdymo seką (pvz. Ctrl+Home "\x1b[1;5H")
// gali persiųsti DALIMIS: pirmame skaityme "\x1b[", kitame "1;5H". Be
// buferio pirmas gabalas ("ESC[") būtų prarastas (ESC — atšaukimo
// klavišas) — dažna „klavišas neveikia“ priežastis.
func (c *Client) readKeys() {
	// Panika klaviatūros apdoravime (pvz. neteisingas indeksas) negali palikti
	// terminalo raw režimu — atkursime jį ir sustabdysime klientą.
	defer c.recoverPanic("klaviatūros įvestis")
	buf := make([]byte, 64)
	var pending []byte // pusiau gauta valdymo seka
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			// stdin uždarytas/nutrūkęs — klaviatūra nebeveikia, saugiai išeiti
			c.stop()
			return
		}
		b := buf[:n]
		if len(pending) > 0 {
			b = append(pending, b...)
			pending = pending[:0]
		}
		if !escComplete(b) && len(b) < 24 {
			// Nepabaigta valdymo seka — laukiame tęsinio kitoje skaitymoje.
			pending = append(pending, b...)
			continue
		}
		c.handleKey(b)
	}
}

// escComplete — ar baitų seka (pradedanti ESC) jau BAIGTA.
//
// false reiškia „laukiame tęsinio“ (sekų buferiavimas readKeys). Atskiras
// ESC — BAIGTAS (vartotojas nuspaudė Esc).
func escComplete(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	if b[0] != 0x1b {
		return true
	}
	switch {
	case len(b) == 1:
		// Vienintelis ESC — atskiras klavišas (paieškos išeitis ir kt.).
		return true
	case b[1] == '[':
		// CSI seka („\x1b[<params><final>“): baigiasi finalo baitu 0x40–0x7E.
		if len(b) == 2 {
			return false
		}
		return b[len(b)-1] >= 0x40 && b[len(b)-1] <= 0x7e
	case b[1] == 'O':
		// SS3 seka („\x1bOX“): vienas simbolis po „O“.
		return len(b) >= 3
	default:
		// „\x1bX“ — Alt+X (du baitai).
		return len(b) >= 2
	}
}

func (c *Client) handleKey(b []byte) {
	// c.keysDebug skaitoma SU RAKINU — /keys komanda ją rašo parseIRC
	// gorutinoje (handleCmd → input.go), o handleKey veikia klaviatūros
	// gorutinoje. Be rakino — duomenų lenktynė.
	c.mu.Lock()
	keysDebug := c.keysDebug
	c.mu.Unlock()
	if keysDebug {
		parts := make([]string, len(b))
		for i, x := range b {
			if x >= 32 && x < 127 {
				parts[i] = fmt.Sprintf("%d('%c')", x, x)
			} else {
				parts[i] = fmt.Sprintf("0x%02x", x)
			}
		}
		c.serverMsg(fmt.Sprintf("[KEYS] %d baitai: %s", len(b), strings.Join(parts, " ")))
		if len(b) == 1 && b[0] == 'q' {
			c.mu.Lock()
			c.keysDebug = false
			c.mu.Unlock()
			c.serverMsg("[KEYS] debug išjungtas")
		}
		return
	}

	// Paste patvirtinimo režimas
	c.mu.Lock()
	hasPaste := len(c.pasteBuf) > 0
	collecting := hasPaste && time.Now().Before(c.pasteCollectUntil)
	c.mu.Unlock()
	if hasPaste {
		if collecting {
			// Paste dar tebesrūva (ateina ~64 B gabaliukais) — renkam eilutes,
			// Enter dar neprivalo reikšti patvirtinimo
			c.mu.Lock()
			c.collectPaste(string(b))
			c.mu.Unlock()
			c.queueRedraw()
			return
		}
		if len(b) == 1 {
			switch b[0] {
			case '\r', '\n':
				c.mu.Lock()
				lines := c.pasteBuf
				c.pasteBuf = nil
				// c.mu jau užrakintas — Locked variantas.
				active := c.activeChanLocked()
				activeName := ""
				if active != nil {
					activeName = active.name
				}
				c.mu.Unlock()
				if c.isConnected() && activeName != "" && activeName != "*server*" {
					for _, l := range lines {
						if l == "" {
							continue
						}
						c.send(fmt.Sprintf("PRIVMSG %s :%s", active.name, l))
						c.addMsg(active.name, Message{ts: time.Now(), nick: c.myNick(), text: l, msgType: MsgChat})
					}
				}
			case 0x1b:
				c.mu.Lock()
				c.pasteBuf = nil
				c.mu.Unlock()
				c.serverMsg("Paste atšauktas")
			}
		}
		c.queueRedraw()
		return
	}

	// Daugiaeilio paste aptikimas
	nlCount := 0
	for _, bb := range b {
		if bb == '\n' || bb == '\r' {
			nlCount++
		}
	}
	if nlCount > 1 {
		lines, partial := splitPasteLines(string(b))
		nonempty := make([]string, 0, len(lines))
		for _, l := range lines {
			if l != "" {
				nonempty = append(nonempty, l)
			}
		}
		if len(nonempty) > 1 {
			c.mu.Lock()
			c.pasteBuf = nonempty
			c.pastePartial = partial
			c.pasteCollectUntil = time.Now().Add(30 * time.Millisecond)
			total := len(nonempty)
			c.mu.Unlock()
			c.serverMsg(fmt.Sprintf("⚠ Paste: %d eilutės. Enter=siųsti, Esc=atšaukti", total))
			c.queueRedraw()
			return
		}
	}

	// Paieškos režimas
	c.mu.Lock()
	searchMode := c.searchMode
	c.mu.Unlock()
	if searchMode {
		c.handleSearchKey(b)
		return
	}

	// Klavišų perrašymas (keys failas, v0.4.8): jei gauta seka yra
	// vartotojo perrašyta (configDir/keys), ją PAKEIČIAME į kanoninę
	// veiksmo seką (keyActions) ir toliau įprastai apdorojame.
	// Ciklas (seka→veiksmas→ta pati seka) apsaugotas iki 3 iteracijų.
	// customBinds — po c.mu: jį RAŠO loadKeys (per /keys arba
	// perform'e) kita gorutina — be spynos tai buvo data race.
	for depth := 0; depth < 3; depth++ {
		c.mu.Lock()
		act, ok := c.customBinds[string(b)]
		c.mu.Unlock()
		if !ok {
			break
		}
		canon, known := keyActions[act]
		if !known || string(canon) == string(b) {
			break
		}
		b = canon
	}

	// Paieškos rezultatų navigacija (v0.4.8): „n“ — kitas rezultatas
	// žemyn, „p“ — ankstesnis. TIK su TUŠČIA įvestimi (su tekstu eilutės
	// „n“/„p“ liko raide), kitaip būtų neįmanoma rašyti.
	c.mu.Lock()
	emptyInput := len(c.input) == 0
	lastQuery := c.lastSearchQuery
	c.mu.Unlock()
	if emptyInput && lastQuery != "" && len(b) == 1 && (b[0] == 'n' || b[0] == 'p') {
		if b[0] == 'n' {
			c.searchForward()
		} else {
			c.mu.Lock()
			active := c.activeChanLocked()
			c.mu.Unlock()
			if active != nil {
				c.doSearch(active, lastQuery)
			}
		}
		return
	}

	// Ctrl (+Shift) rodyklės — nick scroll (↑↓), žodžių navigacija (←→).
	//
	// PASTABA: anksčiau čia buvo ir Ctrl+Home/End (kanalų sąrašo slinktis)
	// — PANAUKTA (v0.4.5): būsenos juostoje ir taip pakanka kanalų
	// valdymo. Sąrašas VISADA automatiškai suseka paskui aktyvų kanalą
	// (žr. switchCh), o rankinis slinkimas — pelės spustelėjimais.
	if len(b) >= 6 && b[0] == 0x1b && b[1] == '[' && b[2] == '1' && b[3] == ';' && b[4] == '5' {
		switch b[5] {
		case 'A': // Ctrl+Up — nick scroll aukštyn
			c.mu.Lock()
			if c.nickScrollOff > 0 {
				c.nickScrollOff--
			}
			c.mu.Unlock()
			c.queueRedraw()
		case 'B': // Ctrl+Down — nick scroll žemyn
			c.mu.Lock()
			c.nickScrollOff++
			c.mu.Unlock()
			c.queueRedraw()
		case 'C': // Ctrl+Right — žodis pirmyn
			c.mu.Lock()
			pos := c.inputPos
			for pos < len(c.input) && c.input[pos] == ' ' {
				pos++
			}
			for pos < len(c.input) && c.input[pos] != ' ' {
				pos++
			}
			c.inputPos = pos
			c.mu.Unlock()
			c.tabCandidates = nil
			c.queueRedraw()
		case 'D': // Ctrl+Left — žodis atgal
			c.mu.Lock()
			pos := c.inputPos
			for pos > 0 && c.input[pos-1] == ' ' {
				pos--
			}
			for pos > 0 && c.input[pos-1] != ' ' {
				pos--
			}
			c.inputPos = pos
			c.mu.Unlock()
			c.tabCandidates = nil
			c.queueRedraw()
		}
		return
	}

	if len(b) >= 3 && b[0] == 0x1b && b[1] == '[' {
		// Pelės įvykis (SGR 1006): \x1b[<b;x;yM arba m
		if b[2] == '<' {
			c.handleMouse(b)
			return
		}
		switch b[2] {
		case 'A':
			c.mu.Lock()
			empty := len(c.input) == 0
			c.mu.Unlock()
			if empty {
				c.mu.Lock()
				c.scrollOff += 3
				c.chaseUntil = time.Time{} // slinkimas aukštyn — vėl užšaldom
				c.mu.Unlock()
				c.queueRedraw()
			} else {
				c.historyUp()
			}
			return
		case 'B':
			c.mu.Lock()
			empty := len(c.input) == 0
			c.mu.Unlock()
			if empty {
				c.mu.Lock()
				if c.scrollOff > 0 {
					c.scrollOff -= 3
					if c.scrollOff < 0 {
						c.scrollOff = 0
					}
					c.chaseUntil = time.Now().Add(1500 * time.Millisecond)
				}
				c.mu.Unlock()
				c.queueRedraw()
			} else {
				c.historyDown()
			}
			return
		case '5':
			c.mu.Lock()
			c.scrollOff += c.height - 4
			c.chaseUntil = time.Time{} // slinkimas aukštyn — vėl užšaldom vaizdą
			c.mu.Unlock()
			c.queueRedraw()
			return
		case '6':
			c.mu.Lock()
			if len(b) >= 4 && b[3] == ';' {
				// Shift+PgDn — grįžti apačion
				c.scrollOff = 0
				c.chaseUntil = time.Time{}
			} else {
				c.scrollOff -= c.height - 4
				if c.scrollOff < 0 {
					c.scrollOff = 0
				}
				// slinktis žemyn: kol gyva, užšaldymas praleidžiamas,
				// kad plūstančios žinutės neatstumtų vaizdo atgal
				c.chaseUntil = time.Now().Add(1500 * time.Millisecond)
			}
			c.mu.Unlock()
			c.queueRedraw()
			return
		case 'D':
			c.mu.Lock()
			if c.inputPos > 0 {
				c.inputPos--
			}
			c.mu.Unlock()
			c.tabCandidates = nil
			c.queueRedraw()
			return
		case 'C':
			c.mu.Lock()
			if c.inputPos < len(c.input) {
				c.inputPos++
			}
			c.mu.Unlock()
			c.tabCandidates = nil
			c.queueRedraw()
			return
		case 'H':
			c.mu.Lock()
			c.inputPos = 0
			c.mu.Unlock()
			c.tabCandidates = nil
			c.queueRedraw()
			return
		case 'F':
			c.mu.Lock()
			c.inputPos = len(c.input)
			c.mu.Unlock()
			c.tabCandidates = nil
			c.queueRedraw()
			return
		}
		fkeys := map[string]int{
			"15~": 4, "17~": 5, "18~": 6, "19~": 7,
			"20~": 8, "21~": 9, "23~": 10, "24~": 11,
		}
		if len(b) >= 4 {
			if idx, ok := fkeys[string(b[2:])]; ok {
				c.switchCh(idx)
			}
		}
		return
	}

	if len(b) == 3 && b[0] == 0x1b && b[1] == 'O' {
		switch b[2] {
		case 'P':
			c.switchCh(0)
		case 'Q':
			c.switchCh(1)
		case 'R':
			c.switchCh(2)
		case 'S':
			c.switchCh(3)
		}
		return
	}

	// Alt+1..Alt+9 — kanalų perjungimas
	if len(b) == 2 && b[0] == 0x1b && b[1] >= '1' && b[1] <= '9' {
		c.switchCh(int(b[1] - '1'))
		return
	}

	if len(b) == 1 && b[0] == 0x1b {
		c.tabCandidates = nil
		return
	}

	for i := 0; i < len(b); {
		r, size := decodeRune(b[i:])
		i += size

		switch r {
		case '\r', '\n':
			c.mu.Lock()
			text := strings.TrimSpace(string(c.input))
			c.input = c.input[:0]
			c.inputPos = 0
			c.mu.Unlock()
			c.tabCandidates = nil
			if text != "" {
				// Slaptažodžių komandos (pvz. IDENTIFY) nesaugomos istorijoje
				if !isSensitiveHistory(text) {
					c.mu.Lock()
					c.history = append(c.history, text)
					if len(c.history) > 1000 {
						c.history = c.history[len(c.history)-1000:]
					}
					c.historyIdx = -1
					c.mu.Unlock()
				}
				c.handleInput(text)
			}
			c.queueRedraw()

		case '\t':
			c.doTab()
			return

		case 127, '\b':
			c.mu.Lock()
			if c.inputPos > 0 {
				c.input = append(c.input[:c.inputPos-1], c.input[c.inputPos:]...)
				c.inputPos--
			}
			c.mu.Unlock()
			c.tabCandidates = nil
			c.queueRedraw()

		case 14: // Ctrl+N
			c.mu.Lock()
			cur := c.activeIdx
			if cur < 0 {
				cur = 0
			}
			idx := (cur + 1) % len(c.channels)
			c.mu.Unlock()
			c.switchCh(idx)

		case 16: // Ctrl+P
			c.mu.Lock()
			cur := c.activeIdx
			if cur < 0 {
				cur = 0
			}
			idx := cur - 1
			if idx < 0 {
				idx = len(c.channels) - 1
			}
			c.mu.Unlock()
			c.switchCh(idx)

		case 12: // Ctrl+L — ekrano atnaujinimas
			fmt.Print("\033[2J\033[H")
			c.queueRedraw()

		case 18: // Ctrl+R - paieška
			c.mu.Lock()
			c.searchMode = true
			c.searchQuery = ""
			c.mu.Unlock()
			c.queueRedraw()

		case 21: // Ctrl+U
			c.mu.Lock()
			c.input = c.input[:0]
			c.inputPos = 0
			c.historyIdx = -1
			c.mu.Unlock()
			c.tabCandidates = nil
			c.queueRedraw()

		case 1: // Ctrl+A
			c.mu.Lock()
			c.inputPos = 0
			c.mu.Unlock()
			c.tabCandidates = nil
			c.queueRedraw()

		case 5: // Ctrl+E
			c.mu.Lock()
			c.inputPos = len(c.input)
			c.mu.Unlock()
			c.tabCandidates = nil
			c.queueRedraw()

		case 11: // Ctrl+K — ištrinti nuo kursoriaus iki galo
			c.mu.Lock()
			c.input = c.input[:c.inputPos]
			c.mu.Unlock()
			c.tabCandidates = nil
			c.queueRedraw()

		case 23: // Ctrl+W — ištrinti žodį prieš kursorių
			c.mu.Lock()
			if c.inputPos > 0 {
				end := c.inputPos
				pos := end
				for pos > 0 && c.input[pos-1] == ' ' {
					pos--
				}
				for pos > 0 && c.input[pos-1] != ' ' {
					pos--
				}
				c.input = append(c.input[:pos], c.input[end:]...)
				c.inputPos = pos
			}
			c.mu.Unlock()
			c.tabCandidates = nil
			c.queueRedraw()

		default:
			if r >= 32 {
				c.mu.Lock()
				c.input = append(c.input, 0)
				copy(c.input[c.inputPos+1:], c.input[c.inputPos:])
				c.input[c.inputPos] = r
				c.inputPos++
				c.mu.Unlock()
				c.tabCandidates = nil
				c.queueRedraw()
			}
		}
	}
}

func (c *Client) handleSearchKey(b []byte) {
	if len(b) == 1 {
		switch b[0] {
		case 0x1b: // ESC
			c.mu.Lock()
			c.searchMode = false
			c.searchQuery = ""
			c.mu.Unlock()
			c.queueRedraw()
			return
		case '\r', '\n': // Enter - atlikti paiešką
			c.mu.Lock()
			q := c.searchQuery
			// c.mu jau užrakintas — Locked variantas.
			active := c.activeChanLocked()
			c.mu.Unlock()
			if q != "" && active != nil {
				c.doSearch(active, q)
			} else {
				c.mu.Lock()
				c.searchMode = false
				c.mu.Unlock()
			}
			c.queueRedraw()
			return
		case 127: // Backspace
			c.mu.Lock()
			if len(c.searchQuery) > 0 {
				r := []rune(c.searchQuery)
				c.searchQuery = string(r[:len(r)-1])
			}
			c.mu.Unlock()
			c.queueRedraw()
			return
		}
	}
	// Pridėti simbolį į paieškos užklausą
	for i := 0; i < len(b); {
		r, size := decodeRune(b[i:])
		i += size
		if r >= 32 {
			c.mu.Lock()
			if len(c.searchQuery) < 500 {
				c.searchQuery += string(r)
			}
			c.mu.Unlock()
		}
	}
	c.queueRedraw()
}

func (c *Client) doSearch(ch *Channel, query string) {
	queryLow := strings.ToLower(query)
	c.mu.Lock()
	defer c.mu.Unlock()

	// Jei ta pati užklausa - ieškoti toliau atgal nuo paskutinio rezultato
	startFrom := len(ch.messages) - 1
	if query == c.lastSearchQuery && c.searchResultIdx > 0 {
		startFrom = c.searchResultIdx - 1
	}

	for i := startFrom; i >= 0; i-- {
		m := ch.messages[i]
		textLow := strings.ToLower(stripIRCFormat(m.text) + " " + m.nick)
		if strings.Contains(textLow, queryLow) {
			c.scrollToMessageLocked(ch, i)
			c.searchResultIdx = i
			c.lastSearchQuery = query
			c.searchMode = false
			return
		}
	}
	// Nerasta - palikti paieškos režimą (gal pradžia pasiekta)
	c.searchResultIdx = -1
}

// searchForward — „n“ (su TUŠČIA įvestimi): kitas paieškos rezultatas
// ŽEMYN (naujentes) — doSearch ieško atgal (aukštyn), šis eina prieki.
// „p“ atveju doSearch įprasta tęsiama tos pačios užklausos atgal.
func (c *Client) searchForward() {
	c.mu.Lock()
	defer c.mu.Unlock()
	active := c.activeChanLocked()
	if active == nil || c.lastSearchQuery == "" || c.searchMode {
		return
	}
	q := strings.ToLower(c.lastSearchQuery)
	start := 0
	if c.searchResultIdx >= 0 {
		start = c.searchResultIdx + 1
	}
	if start >= len(active.messages) {
		return
	}
	for i := start; i < len(active.messages); i++ {
		m := active.messages[i]
		textLow := strings.ToLower(stripIRCFormat(m.text) + " " + m.nick)
		if strings.Contains(textLow, q) {
			c.scrollToMessageLocked(active, i)
			c.searchResultIdx = i
			return
		}
	}
	// Pabaiga pasiekta — nieko nerasta.
}

// scrollToMessageLocked astatę langą taip, kad rastos žinutės paskutinė
// eilutė atsirastų matomo lango apačioje. TIK SU c.mu užrakintu — naudota
// doSearch ir searchForward (du kartai buvo identinės eilutės).
func (c *Client) scrollToMessageLocked(ch *Channel, i int) {
	msgW := c.width - chanListWidth - 2
	if c.isChannel(ch.name) {
		msgW -= nickListWidth + 1
	}
	if msgW < 10 {
		msgW = 10
	}
	myNick := c.nick

	foundCount := len(ch.messages[i].renderLines(msgW, myNick))
	firstLine := 0
	for j := 0; j < i; j++ {
		firstLine += len(ch.messages[j].renderLines(msgW, myNick))
	}
	totalLines := firstLine + foundCount
	for j := i + 1; j < len(ch.messages); j++ {
		totalLines += len(ch.messages[j].renderLines(msgW, myNick))
	}
	c.scrollOff = totalLines - (firstLine + foundCount)
	if c.scrollOff < 0 {
		c.scrollOff = 0
	}
}

// searchAllChannels — „/search all <žodis>“ (v0.4.8): paieška VISUOSE
// languose, rezultatai į *server* langą. Ribojama iki 50 atitikmenų, kad
// milžiniški istorijos kiekiai neužtvindyti langą.
func (c *Client) searchAllChannels(query string) {
	type hit struct{ chanName, line string }
	q := strings.ToLower(query)
	var results []hit

	c.mu.Lock()
	for _, ch := range c.channels {
		for _, m := range ch.messages {
			textLow := strings.ToLower(stripIRCFormat(m.text))
			if strings.Contains(textLow, q) || strings.Contains(strings.ToLower(m.nick), q) {
				line := strings.TrimSpace(sanitizeText(stripIRCFormat(m.text)))
				results = append(results, hit{ch.name, "<" + sanitizeText(m.nick) + "> " + line})
				if len(results) >= 50 {
					break
				}
			}
		}
		if len(results) >= 50 {
			break
		}
	}
	c.mu.Unlock()

	if len(results) == 0 {
		c.errMsg("Paieška „" + sanitizeText(query) + "“: nieko nerasta")
		return
	}
	for _, h := range results {
		c.serverMsg(h.chanName + " " + h.line)
	}
	c.serverMsg(fmt.Sprintf("Paieška „%s“: %d atitikmenų%s", sanitizeText(query), len(results),
		map[bool]string{true: " (rodoma pirmi 50)", false: ""}[len(results) >= 50]))
}

// logSearch — „/logsearch [kanalas] <žodis>“ (v0.4.8): paieška ĮRAŠO
// žurnalo failuose (logDir/<serveris>/<kanalas>-YYYY-MM-DD.log).
// Rezultatai į *server* langą. Skaitymas apribotas (10 MB faile, 500
// atitikmenų), kad milžiniški failai nesustogtų UI.
//
// Be aktyvaus serverio — ieškome tiesiogiai pagrindiniame logDir
// (naudotojas gali žurnalus laikyti ir be serverių hierarchijos).
func (c *Client) logSearch(chanFilter, query string) {
	if c.logDir == "" {
		c.errMsg("Žurnalai išjungti (nėra log katalogo)")
		return
	}
	q := strings.ToLower(query)

	// c.server — po c.mu (jį rašo /connect kitu momentu).
	c.mu.Lock()
	srv := c.server
	c.mu.Unlock()

	dir := c.logDir
	if srv != "" {
		dir = filepath.Join(c.logDir, sanitizePath(srv))
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		c.errMsg("Žurnalų katalogas „" + sanitizeText(dir) + "“ nerastas")
		return
	}

	// Failų rinkinys: su kanalo filtru — tik to kanalo failai; be — visi.
	var files []string
	if chanFilter != "" {
		// Sanitizuojame KAIP ir žurnale rašoma — kitu atveju „#“/„..“
		// glob'e nesutaptų su tikrais failais arba kelias būtų pavojingas.
		// Dar globQuote'ame: sanitizePath nevalo „[“/„]“ (ir nereikia —
		// kanalas tokių turi mažai), o filepath.Glob juose matytų
		// SIMBOLIŲ RINKINIUS („[chan]“ → vienas iš c/h/a/n) ir tyliai
		// nerastų nieko.
		prefix := globQuote(sanitizePath(chanFilter))
		matches, err := filepath.Glob(filepath.Join(dir, prefix+"-*.log"))
		if err != nil || len(matches) == 0 {
			c.errMsg("Žurnalų failų kanalui „" + sanitizeText(chanFilter) + "“ nerasta")
			return
		}
		files = matches
	} else {
		matches, err := filepath.Glob(filepath.Join(dir, "*.log"))
		if err != nil || len(matches) == 0 {
			c.errMsg("Žurnalų failų nerasta")
			return
		}
		files = matches
	}

	// searchedFiles — kiek failų TIKRAI ieškoėme (su limito nutrūkimu
	// „%d failuose“ anksčiau skaičiavo VISO glob'e esančius).
	total, searchedFiles, skippedFiles := 0, 0, 0
	for _, path := range files {
		if total >= 500 {
			break
		}
		if err := c.logSearchFile(path, q, &total); err != nil {
			skippedFiles++
			continue
		}
		searchedFiles++
	}
	if total == 0 && skippedFiles == 0 {
		c.errMsg("Žurnaluose nieko nerasta: " + sanitizeText(query))
		return
	}
	if total > 0 {
		c.serverMsg(fmt.Sprintf("Žurnalų paieška: %d atitikmenų (maks. 500), %d failuose", total, searchedFiles))
	}
	if skippedFiles > 0 {
		c.errMsg(fmt.Sprintf("%d failų net įskaitoma — praleista", skippedFiles))
	}
}

// globQuote — eilutėje atslinkti filepath.Glob'o metasimbolius
// („\\ [ ] * ?“) atgaliniu brūkšniu, kad glob'e eilutė atitiktų TIK
// save. Svarbiausia „[“/„]“ — kitaip „[chan]“ būtų simbolių rinkinys
// ir tyliai nerastų nieko (filepath.QuoteMeta daro tą patį, bet čia
// trumpiau ir aiškiau, kokius simbolius saugome).
func globQuote(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\\', '[', ']', '*', '?':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// logSearchFile — vieno žurnalo failo nuskaitymas ir atitikmenų spausdinimas.
// Failas apribotas iki 10 MB (lieka praleidus); atitikmenys per total.
func (c *Client) logSearchFile(path string, q string, total *int) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.Size() > 10*1024*1024 {
		return errLogSearchSkipped
	}
	base := filepath.Base(path)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 64*1024)
	for sc.Scan() {
		if *total >= 500 {
			break
		}
		line := sc.Text()
		// Atitikmenys TIK ŽINUTĖS TEKSTE (po „[HH:MM:SS] “): kitaip paieška
		// "10:0" arba "2026:10" atitiktų LAIKO ŽYMA, o ne žinutę.
		body := line
		if i := strings.IndexByte(line, ']'); i > 0 && len(line) > i+2 {
			body = strings.TrimSpace(line[i+1:])
		}
		if strings.Contains(strings.ToLower(body), q) {
			// Rodome be laiko žymos — ji ilgai ir mažai naudoinga.
			c.serverMsg(base + "> " + body)
			*total++
		}
	}
	return sc.Err()
}

// errLogSearchSkipped — per didelis failas (10 MB riba) praleidžiamas.
var errLogSearchSkipped = errors.New("failas per didelis")

// listSettings — /set be argumentų: sąrašas su dabartinėmis reikšmėmis.
func (c *Client) listSettings() {
	for _, k := range settableKeys {
		c.showSetting(k)
	}
	c.serverMsg("/set <raktas> <reikšmė> — keisti (perrašoma į config failą)")
}

// loadKeys — keys failas (configDir/keys, v0.4.8): klavišų perrašymas.
// Įspėjimai (nevalidos eilutos) rodomi paleidime.
func (c *Client) loadKeys() []string {
	binds, warns := parseKeysFile(keysPath())
	c.mu.Lock()
	if c.customBinds == nil {
		c.customBinds = map[string]string{}
	}
	c.customBinds = binds
	c.mu.Unlock()
	return warns
}

// showKeyBindings — /keys be argumentų: aktyvūs perrašymai + veiksmų vardai.
func (c *Client) showKeyBindings() {
	c.mu.Lock()
	binds := make(map[string]string, len(c.customBinds))
	for seq, act := range c.customBinds {
		binds[seq] = act
	}
	c.mu.Unlock()
	if len(binds) == 0 {
		c.serverMsg("Nėra perrašytų klavišų. Failas: " + orDefault(keysPath(), "(nėra config katalogo)"))
	} else {
		c.serverMsg("— Perrašyti klavišai (configDir/keys) —")
		for _, name := range keyActionNames() {
			for seq, act := range binds {
				if name == string(act) {
					c.serverMsg("  " + name + " = " + escapeKeySeq(seq))
				}
			}
		}
	}
	known := strings.Join(keyActionNames(), ", ")
	c.serverMsg("Veiksmų vardai: " + known)
	c.serverMsg("Sekos sintaksė: \\e (ESC), \\xHH, \\n, \\r, \\t, \\\\ — pvz.: search = \\eS")
}

// escapeKeySeq — sekos atvaizdavimas keys failo sintaksėje.
func escapeKeySeq(seq string) string {
	out := ""
	for i := 0; i < len(seq); i++ {
		switch b := seq[i]; {
		case b == 0x1b:
			out += "\\e"
		case b == '\n':
			out += "\\n"
		case b == '\r':
			out += "\\r"
		case b == '\t':
			out += "\\t"
		case b == '\\':
			out += "\\\\"
		case b == ' ':
			// Tarpas — \x20: parseKeysFile naudoja TrimSpace, kuris
			// „pliką“ tarpą sekos gale nukirptų (ir jis dingtų iš failo).
			out += "\\x20"
		case b >= 32 && b < 127:
			out += string(rune(b))
		default:
			out += fmt.Sprintf("\\x%02x", b)
		}
	}
	return out
}

// orDefault — testo pagalbinė tuščios eilutės substitucijai.
func orDefault(s string, def string) string {
	if s == "" {
		return def
	}
	return s
}

// showSetting — /set <key>: vieno rakto dabartinė reikšmė.
func (c *Client) showSetting(key string) {
	c.mu.Lock()
	v := ""
	switch key {
	case "mouse":
		v = boolCfg(c.mouse)
	case "notifications":
		v = boolCfg(c.notify)
	case "ctcpversion":
		v = boolCfg(c.ctcpVersion)
	case "reconnect":
		v = boolCfg(c.reconnect)
	case "timestamps":
		v = boolCfg(timestampsEnabled)
	case "ts_format":
		v = tsFormat
	case "tscolor":
		v = tsColorName
	case "highlight_color":
		v = hlColorName
	case "nick_colors":
		v = boolCfg(nickColoring)
	case "maxmessages":
		v = strconv.Itoa(c.maxMessages)
	case "theme":
		if themeDarkMode {
			v = "dark"
		} else {
			v = "light"
		}
	}
	c.mu.Unlock()
	c.serverMsg(key + " = " + v)
}

// boolCfg — bool reikšmės žodinis atvaizdavimas (config failo stiliumi).
func boolCfg(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// applySetting — /set <key> <value>: patikrinimas, GYVAS pritaikymas ir
// įrašymas atgal į config failą. Gyvas pritaikymas VAPRAŠOMAS — blogos
// reikšmės NEĮrašoma į failą, jeigu ji neatitinka.
func (c *Client) applySetting(key, value string) {
	okMsg := ""
	// bool reikšmės žinomumas TRIJŲ būsenų: nežinomas žodis (pvz. „taip“)
	// — KLAIDA, ne tylus false (seniau „on“ būdavo pritaikytas kaip false
	// ir dar įrašytas į config failą). Grąžina true, kai reikšmė bloga.
	boolError := func(v string) bool {
		if _, known := parseBoolValue(v); !known {
			c.errMsg("Reikšmė turi būti true/false (on, off, yes, no, 1, 0): " + sanitizeText(v))
			return true
		}
		return false
	}
	switch key {
	case "mouse":
		if boolError(value) {
			return
		}
		b, _ := parseBoolValue(value)
		c.mu.Lock()
		c.mouse = b
		c.mu.Unlock()
		if b {
			fmt.Print("\033[?1000h\033[?1002h\033[?1006h")
		} else {
			fmt.Print("\033[?1002l\033[?1000l\033[?1006l")
		}
	case "notifications":
		if boolError(value) {
			return
		}
		b, _ := parseBoolValue(value)
		c.mu.Lock()
		c.notify = b
		c.mu.Unlock()
	case "ctcpversion":
		if boolError(value) {
			return
		}
		b, _ := parseBoolValue(value)
		c.mu.Lock()
		c.ctcpVersion = b
		c.mu.Unlock()
	case "reconnect":
		if boolError(value) {
			return
		}
		b, _ := parseBoolValue(value)
		c.mu.Lock()
		c.reconnect = b
		c.mu.Unlock()
	case "timestamps":
		if boolError(value) {
			return
		}
		b, _ := parseBoolValue(value)
		setTimestamps(b, "")
	case "ts_format":
		if !isValidTSFormat(value) {
			c.errMsg("Nevalidus laikmatis (pvz. „15:04“, „15:04:05“): " + sanitizeText(value))
			return
		}
		setTimestamps(timestampsEnabled, value)
	case "tscolor":
		if !validNamedColor(value) {
			c.errMsg("Nežinoma spalva (gray, blue, green, cyan, none): " + sanitizeText(value))
			return
		}
		setTSColor(value)
	case "highlight_color":
		if !validHighlightColor(value) {
			c.errMsg("Nežinoma spalva (red, blue, green, magenta, cyan, yellow, none): " + sanitizeText(value))
			return
		}
		setHighlightColor(value)
	case "nick_colors":
		if boolError(value) {
			return
		}
		b, _ := parseBoolValue(value)
		setNickColors(b)
	case "theme":
		if !validTheme(value) {
			c.errMsg("Nežinoma tema (dark, light): " + sanitizeText(value))
			return
		}
		setTheme(value)
	case "maxmessages":
		n, err := strconv.Atoi(value)
		if err != nil || n < 100 || n > 100000 {
			c.errMsg("Reikšmė turi būti sveikas skaičius 100–100000: " + sanitizeText(value))
			return
		}
		c.mu.Lock()
		c.maxMessages = n
		for _, ch := range c.channels {
			ch.maxMessages = n
		}
		c.mu.Unlock()
		okMsg = " (esamiems langams)"
	default:
		return
	}
	// Įrašome ATGAL į config failą (atominis perrašymas).
	if err := saveConfigSetting(key, value); err != nil {
		c.errMsg("Pritaikyta gyvai, bet į failą NEĮRAŠYTA: " + err.Error())
		return
	}
	c.serverMsg("nustatymas: " + key + " = " + sanitizeText(value) + okMsg)
}

// validNamedColor — vardinės spalvos (ts_color rinkinis).
func validNamedColor(v string) bool {
	switch strings.ToLower(v) {
	case "gray", "blue", "green", "cyan", "none":
		return true
	}
	return false
}

// validHighlightColor — highlight fono spalvos rinkinys (platesnis).
func validHighlightColor(v string) bool {
	switch strings.ToLower(v) {
	case "red", "blue", "green", "magenta", "cyan", "yellow", "none":
		return true
	}
	return false
}

// validTheme — temų rinkinys (v0.4.8).
func validTheme(v string) bool {
	switch strings.ToLower(v) {
	case "dark", "light":
		return true
	}
	return false
}

// isValidTSFormat — laiko žymos formatas: tik skaitmenys, „:“, „.“, „-“
// ir vienas tarpas (pvz. „15:04“, „15:04:05“, „2006-01-02 15:04“).
// Konservatyvus leidimas — bet kas kita (kabliataškiai, ESC simboliai,
// anotacijos „MST“) atmetama: formatas eina tiesiai į m.ts.Format().
func isValidTSFormat(v string) bool {
	if v == "" || len(v) > 16 {
		return false
	}
	if strings.Contains(v, "  ") {
		return false
	}
	for _, r := range v {
		if !(r >= '0' && r <= '9') && r != ':' && r != '.' && r != '-' && r != ' ' {
			return false
		}
	}
	return true
}

// exportJSON — /exportjson <langas> (v0.4.8): visos lango žinutės į JSON
// failą (logDir/<serveris>/<kanalas>-YYYY-MM-DD.json). Kelią valome
// sanitizePath (ta taisyklė, kaip žurnalams) — katalogų kelio išėjimas
// („../“) neįmanomas.
func (c *Client) exportJSON(chanName string) {
	if c.logDir == "" {
		c.errMsg("Nėra log katalogo (logdir)")
		return
	}
	c.mu.Lock()
	var ch *Channel
	for _, chc := range c.channels {
		if c.nickEqual(chc.name, chanName) {
			ch = chc
			break
		}
	}
	c.mu.Unlock()
	if ch == nil {
		c.errMsg("Langas „" + sanitizeText(chanName) + "“ nerastas")
		return
	}

	c.mu.Lock()
	copyMsgs := make([]Message, len(ch.messages))
	copy(copyMsgs, ch.messages)
	name := sanitizePath(ch.name)
	srv := c.server
	c.mu.Unlock()

	// Katalogas: logDir/<serveris> (kaip ir žurnalams).
	dir := c.logDir
	if srv != "" {
		dir = filepath.Join(c.logDir, sanitizePath(srv))
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		c.errMsg("Nepavyko sukurti katalogo: " + err.Error())
		return
	}
	path := filepath.Join(dir, name+"-"+time.Now().Format("2006-01-02")+".json")
	buf, err := json.MarshalIndent(copyMsgs, "", "  ")
	if err != nil {
		c.errMsg("JSON klaida: " + err.Error())
		return
	}
	// encoding/json visada HTML-escape'ina („<“ → „<“) —
	// HTML injekcija per eksportą neįmanoma.
	if err := os.WriteFile(path, buf, 0600); err != nil {
		c.errMsg("Nebepavyko išsaugoti: " + err.Error())
		return
	}
	c.serverMsg(fmt.Sprintf("Eksportas: %d žinučių → %s", len(copyMsgs), path))
}

func (c *Client) historyUp() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.history) == 0 {
		return
	}
	if c.historyIdx == -1 {
		c.historyIdx = len(c.history) - 1
	} else if c.historyIdx > 0 {
		c.historyIdx--
	}
	line := []rune(c.history[c.historyIdx])
	c.input = make([]rune, len(line))
	copy(c.input, line)
	c.inputPos = len(c.input)
	c.queueRedraw()
}

func (c *Client) historyDown() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.historyIdx == -1 {
		return
	}
	c.historyIdx++
	if c.historyIdx >= len(c.history) {
		c.historyIdx = -1
		c.input = c.input[:0]
		c.inputPos = 0
	} else {
		line := []rune(c.history[c.historyIdx])
		c.input = make([]rune, len(line))
		copy(c.input, line)
		c.inputPos = len(c.input)
	}
	c.queueRedraw()
}

// ---- Tab autocomplete ----

var allCommands = []string{
	"/away", "/back", "/clear", "/clearlogs", "/close", "/connect", "/cycle", "/exit",
	"/ignore", "/ignores", "/invite", "/join", "/j",
	"/list", "/leave", "/me", "/msg", "/m", "/mode",
	"/kick", "/k", "/names", "/notice",
	"/nick", "/open", "/op", "/deop", "/voice", "/devoice",
	"/part", "/query", "/quit", "/q",
	"/raw", "/reconnect", "/search", "/time", "/topic",
	"/unignore", "/urls", "/w", "/wc", "/whois", "/wi", "/win", "/window",
	"/help", "/keys",
}

// isChannelWord — ar žodis prasideda kanalo tipo simboliu # (CHANtypes).
// Tuščias chanTypes — standartinis „#“.
func isChannelWord(word, chanTypes string) bool {
	if word == "" {
		return false
	}
	for _, r := range chanTypes {
		if strings.HasPrefix(word, string(r)) {
			return true
		}
	}
	return strings.HasPrefix(word, "#")
}

func (c *Client) doTab() {
	c.mu.Lock()
	inputStr := string(c.input[:c.inputPos])
	// c.mu jau užrakintas — Locked variantas.
	active := c.activeChanLocked()
	// Nukopijuojame nick'ų ir žinučių sąrašus — parseIRC gorutina tuo tarpu
	// gali juos modifikuoti (sort.Nicks keičia vietomis), o be kopijos būtų
	// duomenų lenktynė (race).
	var nicks []string
	var msgNicks []string
	var chanNames []string
	if active != nil {
		nicks = append(nicks, active.nicks...)
		for _, m := range active.messages {
			if m.nick != "" {
				msgNicks = append(msgNicks, m.nick)
			}
		}
	}
	// Kanalų pavadinimai — Tab užbaigimui (v0.4.7): „/join #li<Tab>“.
	chanTypes := ""
	for _, ch := range c.channels {
		chanNames = append(chanNames, ch.name)
	}
	if len(chanNames) > 0 {
		ct, _, _, _, _ := c.isupSnapshot()
		chanTypes = ct
	}
	prefixSigns := ""
	if active != nil {
		prefixSigns = active.prefixSigns()
	}
	c.mu.Unlock()
	if active == nil {
		return
	}

	if len(c.tabCandidates) > 0 {
		c.tabIdx = (c.tabIdx + 1) % len(c.tabCandidates)
		chosen := c.tabCandidates[c.tabIdx]
		c.mu.Lock()
		prefix := stripLastWord(c.tabPrefix)
		suffix := ""
		if !strings.HasPrefix(c.tabPrefix, "/") {
			if prefix == "" {
				suffix = ": "
			} else {
				suffix = " "
			}
		}
		total := len(c.tabCandidates)
		hint := ""
		if total > 1 {
			hint = fmt.Sprintf(" [%d/%d]", c.tabIdx+1, total)
		}
		newInput := prefix + chosen + suffix + hint
		c.input = []rune(newInput)
		c.inputPos = len([]rune(prefix + chosen + suffix))
		c.mu.Unlock()
		c.queueRedraw()
		return
	}

	word := wordBefore(inputStr)
	if word == "" {
		return
	}
	wordLow := strings.ToLower(word)

	var candidates []string
	if strings.HasPrefix(word, "/") {
		for _, cmd := range allCommands {
			if strings.HasPrefix(cmd, wordLow) {
				candidates = append(candidates, cmd)
			}
		}
	} else if isChannelWord(word, chanTypes) {
		// Kanalų vardų užbaigimas: žodis prasideda CHANTYPES simboliu —
		// papildo iš ATIDARYTŲ langų (v0.4.7).
		seen := map[string]bool{}
		for _, name := range chanNames {
			bare := sanitizeText(name)
			if !seen[bare] && strings.HasPrefix(strings.ToLower(bare), wordLow) {
				seen[bare] = true
				candidates = append(candidates, bare)
			}
		}
	} else {
		seen := map[string]bool{}
		for _, n := range nicks {
			// SANITIZUOJAME: nick'as atėjo iš serverio (JOIN/NAMES) ir gali
			// turėti ESC/OSC seką. Be to jis įrašiamas į c.input, kuris
			// (be sanitizavimo) pasiekia terminalą — per Tab-completion būtų
			// gyvas OSC 52 (rašymas į iškarpinę) sinkas.
			bare := sanitizeText(stripMode(n, prefixSigns))
			if !seen[bare] && strings.HasPrefix(strings.ToLower(bare), wordLow) {
				seen[bare] = true
				candidates = append(candidates, bare)
			}
		}
		for _, m := range msgNicks {
			m = sanitizeText(m)
			if !seen[m] {
				seen[m] = true
				if strings.HasPrefix(strings.ToLower(m), wordLow) {
					candidates = append(candidates, m)
				}
			}
		}
	}

	if len(candidates) == 0 {
		return
	}

	c.tabCandidates = candidates
	c.tabIdx = 0
	c.tabPrefix = inputStr

	chosen := candidates[0]
	c.mu.Lock()
	prefix := stripLastWord(inputStr)
	suffix := ""
	if !strings.HasPrefix(word, "/") {
		if prefix == "" {
			suffix = ": "
		} else {
			suffix = " "
		}
	}
	total := len(candidates)
	hint := ""
	if total > 1 {
		hint = fmt.Sprintf(" [1/%d]", total)
	}
	c.input = []rune(prefix + chosen + suffix + hint)
	c.inputPos = len([]rune(prefix + chosen + suffix))
	c.mu.Unlock()
	c.queueRedraw()
}

// stripLastWord grąžina s be paskutinio žodžio (iki jo pradžios).
//
// Anksčiau buvo `s[:len([]rune(s))-len([]rune(word))]` — tai BYTE slicing
// su RUNE'ų skaičiumi: „žinutė kai“ nukirpo iki „žinut\xc4“ (nutrūkusi
// UTF-8 seka). Dabar skaičiuojama rune'ais.
func stripLastWord(s string) string {
	r := []rune(s)
	word := []rune(wordBefore(s))
	if len(word) >= len(r) {
		return ""
	}
	return string(r[:len(r)-len(word)])
}

func wordBefore(s string) string {
	s = strings.TrimRight(s, " ")
	i := strings.LastIndex(s, " ")
	if i == -1 {
		return s
	}
	return s[i+1:]
}

// firstField grąžina pirmąjį ne-tarpų lauką arba "" jei jų nėra.
// Saugi vietoj strings.Fields(s)[0] (kuri panikuoja tuščiam/tarpų tekstui).
func firstField(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

// isSensitiveHistory – komandos, kuriose gali būti slaptažodis, nesaugomos
// ĮVEDIMO ISTORIJOS faile.
//
// ATKREIPKITE: ši funkcija tikrina VARTOJO ĮVEDIMO eilutę – todėl ji gali
// būti pli (žodis „register“ ar „pass“ komandos pradžioje yra tikras
// komandos pavadinimas). ŽURNALUI naudokite `isSensitiveLogLine` – ten
// reikia atpažinti slaptažodį TIK komandų kontekste, nes į žurnalą
// rašomi ir įprasti pokalbiai.
//
// Apimamos formos:
//   - NickServ: /msg NickServ identify <pass>, /ns register <pass> <email>,
//     /ns confirm <kodas>, /ns set password <pass>, /ns auth <pass>
//   - Tiesioginės formos (be „nickserv“ prefikso): „register <pass> …“,
//     „set password <pass>“, „confirm <kodas>“ – serveris gali atsiųsti
//     atsakymą be prefikso, o vartotojas gali rašyti komandą tiesiogiai.
//   - Serverio slaptažodis: PASS <pass> (serverio slaptažodis / bouncer)
//   - OPER: OPER <pass> (operatoriaus slaptažodis)
func isSensitiveHistory(line string) bool {
	low := strings.ToLower(strings.TrimSpace(line))
	if low == "" {
		return false
	}
	// Serverio ir operatoriaus slaptažodžiai – visada slapti, nepaisant
	// konteksto. „/raw PASS x“ (arba „/raw OPER x“) turi slėpti.
	if isRawSecretCommand(low) {
		return true
	}
	// /msg nickserv identify <pass> / register <pass> <email> / confirm <kodas> /
	// set password <pass>
	const nsPrefix = "msg nickserv"
	nickCmd := strings.HasPrefix(low, "/ns ") ||
		strings.HasPrefix(low, "/nickserv ") ||
		strings.HasPrefix(low, "/squery ") ||
		strings.HasPrefix(low, "nickserv ") ||
		strings.Contains(low, nsPrefix)
	if !nickCmd {
		// Ne NickServ komanda – tikriname, ar eilutė pati nėra slaptažodį
		// turinti komanda. Tikriname žodį pagal BAIGTĄ (atskirame lauku),
		// kad „identification“ ar „re-register“ žodžio viduryje neatpažintume
		// komandos. Bet „identify“ – visada slapta (anksčiau taip buvo).
		if containsWord(low, "identify") {
			return true
		}
		// Tiesioginės formos: eilutė PRADEDA komanda (pvz. „register …“,
		// „set password …“, „confirm …“, „auth …“). Tokios komandos dažnai
		// atsiunčiamos iš serverio be „nickserv“ prefikso – anksčiau jos
		// lėkė į žurnalą plaintext.
		return isBareSecretCommand(low)
	}
	for _, kw := range []string{"identify", "ident ", "register", "confirm", "set password", "auth"} {
		if strings.Contains(low, kw) {
			return true
		}
	}
	// Trumpinta forma: „set p <slaptažodis>“. NickServ leidžia rašyti „set p
	// password“ trumpai – anksčiau ši forma nebuvo aptinkama, todėl
	// slaptažodis likdavo įvedimo istoriją.
	if isSetPasswordShort(low) {
		return true
	}
	return false
}

// isSensitiveLogLine – ar ŽURNALO eilutė turi slėpti slaptažodį.
//
// SKIRTIS NUO isSensitiveHistory: žurnale yra ir ĮPRASTI POKALBIAI (kitų
// žmonių žinutės). Platus „pirmas žodis“ tikrinimas („register …“,
// „pass …“, „auth …“) ten slėpdų VISKĄ, kas pradžiasi šiais žodžiais –
// pvz. „register your vote today“ ar „pass the salt please“ – t. y.
// žurnalas tyliai prarado realų pokalbių tekstą.
//
// Čia taikome TIK komandų KONTEKSTĄ:
//   - „msg nickserv …“ (ir „/ns …“, „/nickserv …“, „nickserv …“)
//   - komanda, PRISIŠTA prie žinutės kaip komanda (pradžia su „/“)
//   - serverio/operatoriaus slaptažodžiai („PASS x“, „OPER x“, „/raw …“)
//
// Sąmoningas kompromisas: serverio atsiųstas „register <kodas>“ (be
// prefikso) į žurnalą nebepateks – komanda be konteksto neatpažįstama,
// bet įprastas pokalbis lieka įrašytas.
func isSensitiveLogLine(line string) bool {
	low := strings.ToLower(strings.TrimSpace(line))
	if low == "" {
		return false
	}
	// Serverio ir operatoriaus slaptažodžiai – visada slapti, bet TIK
	// komandų forma: „/raw pass x“, „/pass x“ arba grynas komandų pavadinimas
	// („pass x“ – serverio atsiųsta komanda). Pokalbyje žodį „pass“ naudojame
	// paprastai – jis nėra komanda.
	if isSensitiveLogRawSecret(low) {
		return true
	}
	// Vartotojo komanda („/ns identify …“, „/register …“) – aiškus komandos
	// pavadinimas, ne pokalbis.
	if strings.HasPrefix(low, "/") {
		return isSensitiveHistory(low)
	}
	// NickServ komanda su prefiksu.
	const nsPrefix = "msg nickserv"
	if strings.HasPrefix(low, "/ns ") ||
		strings.HasPrefix(low, "/nickserv ") ||
		strings.HasPrefix(low, "/squery ") ||
		strings.HasPrefix(low, "nickserv ") ||
		strings.Contains(low, nsPrefix) {
		return isSensitiveHistory(low)
	}
	// Be konteksto – NIEKO nesuslapiname: „register your vote“,
	// „identify the problem“ ir „pass the salt“ yra įprasti pokalbiai, ne
	// komandos. Tik aiškios komandų formos (kurių kontekstas matomas)
	// slėpinamos.
	return false
}

// isSensitiveLogRawSecret – ar eilutė yra serverio/operatoriaus slaptažodžio
// KOMANDA (tik komandų forma, ne pokalbis).
//
// ŽURNALO lygis: „pass the salt please“ – pokalbis, NE komanda, todėl
// įrašomas. „/raw pass x“, „/pass x“ ir grynas „pass x“ (serverio komanda
// į serverio langą) – slapti.
func isSensitiveLogRawSecret(low string) bool {
	norm := strings.Join(strings.Fields(low), " ")
	if norm == "" {
		return false
	}
	for _, cmd := range []string{"pass", "oper"} {
		if strings.HasPrefix(norm, "/"+cmd+" ") || strings.HasPrefix(norm, "/raw "+cmd+" ") {
			return true
		}
		// Grynos komandos forma: „pass x“ / „oper x“ — serverio atsiųsta
		// komanda (jos atsiunčiamos į *server* langą, ne kaip pokalbis).
		// Bet „pass the salt“ — pokalbis…?
		//
		// SKIRTIS: komanda neturi reikšmingo žodžio PO komandos
		// pavadinimo — t. y. argumentas yra trumpas ir be tarpų
		// (slaptažodis). „pass the salt please“ turi 3 žodžius — pokalbis.
		if strings.HasPrefix(norm, cmd+" ") && isSecretArgument(norm[len(cmd)+1:]) {
			return true
		}
	}
	return false
}

// isSecretArgument – ar komandos argumentas atrodo kaip slaptažodis.
//
// Kriterijai: argumentas neturi tarpų (vienas „žodis“) ir turi
// SLAPTAŽODŽIO FORMĄ — skaičių, didžiąją/mišrią raistą arba specialų
// simbolį („hunter2“, „Secret1!“, „my!pass“). Tai atskiria „pass hunter2“
// (slaptažodis) nuo „pass the“ (pokalbis).
//
// Anksčiau KIEKVIENAS vieno žodžio argumentas buvo laikomas slaptu
// (skaitmenų/raidžių patikros buvo negyvas kodas — visi keliai grąžindavo
// true): pokalbio eilutė „pass the“ žurnale taptų „[slaptažodis
// paslėptas]“ — tikras pokalbio tekstas prarastas.
//
// KOMPROMISAS: visos mažųjų raidžių slaptažodis („hunter2“ be skaitukos...
// t. y. „mysecret“, trumpas) atpažįstamas kaip slaptažodis tik tada, kai
// jis ilgesnis nei žodžiuose įprastai (slaptu, jei ilgesnis nei 10) —
// trumpi žodžiai („the“, „me“, „salt“) yra pokalbis.
func isSecretArgument(arg string) bool {
	if arg == "" || len(arg) > 64 {
		return false
	}
	if strings.ContainsAny(arg, " \t\r\n") {
		return false
	}
	// Slaptažodžio FORMA — skaitmenys, didžiosios raidės (arba mišinys)
	// arba specialūs simboliai visada kelia įtarimą.
	if strings.ContainsAny(arg, "0123456789") {
		return true
	}
	if arg != strings.ToLower(arg) {
		return true
	}
	if len(arg) != len(strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return -1
	}, arg)) {
		// Yra specialių simbolių („my!pass“) — greičiausiai slaptažodis.
		return true
	}
	// Visos mažosios raidės — įprastas žodis (pokalbis), NE slaptažodis;
	// išimtis — ilgos eilutės („myverysecretpass“), kurios žodžių sąraše
	// nebetelpa.
	return len([]rune(arg)) > 10
}

// isRawSecretCommand aptinka serverio/operatoriaus slaptažodžio komandas —
// „PASS <pass>“, „OPER <pass>“ (ir su „/raw “ prefiksu). Tokios komandos
// niekada neturi būti įrašomos į istoriją ar žurnalą.
//
// DVI TAISYKLĖS:
//
//  1. Atpažįstamos tik KOMANDŲ formos — su „/“ arba „/raw “ prefiksu, arba
//     kaip atskira komanda („pass x“). Pokalbyje žodį „pass“ naudojame
//     paprastai („pass the salt please“) — jis nėra komanda, todėl neturi
//     būti slaptinamas. ŽURNALE tai yra tikras duomenų praradimas.
//
//  2. Tarpai normalizuojami: „/raw  PASS x“ (du tarpai) turi būti
//     atpažįstamas lygiai kaip „/raw pass x“ — anksčiau papildomas tarpas
//     apeikdavo patikrinimą, o slaptažodis lėkė į žurnalą plaintext.
//
// Istorijos lygis: „pass x“ čia TIKRAI yra komanda — vartotojas pats ją
// įvedė (žurnalui žr. isSensitiveLogRawSecret, kur pokalbio forma
// įvertinama atskirai).
func isRawSecretCommand(low string) bool {
	// Normalizuojame tarpus — „/raw  PASS x“ ir „/raw pass x“ yra ta
	// pati komanda.
	norm := strings.Join(strings.Fields(low), " ")
	if norm == "" {
		return false
	}
	for _, cmd := range []string{"pass", "oper"} {
		// Formos: „pass x“, „/pass x“, „/raw pass x“, „oper x“, „/oper x“,
		// „/raw oper x“.
		//
		// ANKSTESNĖ VERSIJA tikrino tik tris iš šių šešių — todėl
		// „/oper slaptažodis“ lėkė į žurnalą plaintext.
		for _, prefix := range []string{cmd + " ", "/" + cmd + " ", "/raw " + cmd + " "} {
			if strings.HasPrefix(norm, prefix) {
				return true
			}
		}
		if norm == cmd || norm == "/"+cmd {
			return true
		}
	}
	return false
}

// containsWord tikrina, ar žodis yra atskiras (ne kito žodžio viduje).
func containsWord(low, word string) bool {
	idx := 0
	for {
		i := strings.Index(low[idx:], word)
		if i == -1 {
			return false
		}
		i += idx
		before := i == 0 || isWordSep(rune(low[i-1]))
		afterIdx := i + len(word)
		after := afterIdx >= len(low) || isWordSep(rune(low[afterIdx]))
		if before && after {
			return true
		}
		idx = i + len(word)
		if idx >= len(low) {
			return false
		}
	}
}

// isWordSep tikrina, ar simbolis yra atskiras žodžio riba.
func isWordSep(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}

// isBareSecretCommand aptinka slaptažodį turinčias komandas, kurios eilutę
// PRADEDA (be „nickserv“ / „/msg“ prefikso):
//
//	„register <pass> <email>“
//	„confirm <kodas>“
//	„set password <pass>“ (ir „set p <pass>“)
//	„auth <pass>“
//	„identify <pass>“
//
// Serverio atsakymai dažnai būna be prefikso — anksčiau jie lėkė į žurnalą
// plaintext.
func isBareSecretCommand(low string) bool {
	fields := strings.Fields(low)
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "register", "confirm", "auth", "identify", "ident", "password":
		return len(fields) > 1
	case "set":
		// „set password <pass>“ / „set p <pass>“ — bet „set away“ ar
		// „set quiet“ nėra slaptažodis.
		if len(fields) < 3 {
			return false
		}
		switch fields[1] {
		case "p", "pass", "password":
			return true
		}
		return false
	}
	return false
}

// isSetPasswordShort tikrina NickServ komandą „set p <slaptažodis>“.
//
// Atpažįstamos formos: „/msg NickServ set p <pass>“, „/ns set p <pass>“
// (ir bet koks kitas NickServ komandos pavadinimas).
func isSetPasswordShort(low string) bool {
	// Ieškome žodžio „set“ ir po jo — raidės „p“ (arba „pass“).
	idx := 0
	for {
		i := strings.Index(low[idx:], "set ")
		if i == -1 {
			return false
		}
		i += idx
		rest := strings.TrimSpace(low[i+4:])
		if rest == "" {
			return false
		}
		// Pirmas žodis po „set“ turi būti slaptažodžio parametras.
		word := rest
		if j := strings.IndexAny(rest, " 	"); j != -1 {
			word = rest[:j]
		}
		switch word {
		case "p", "pass", "password":
			return true
		}
		idx = i + 4
	}
}

func decodeRune(b []byte) (rune, int) {
	if len(b) == 0 {
		return 0, 1
	}
	r, size := utf8.DecodeRune(b)
	if r == utf8.RuneError && size == 1 {
		return rune(b[0]), 1
	}
	return r, size
}

// ---- Įvedimo apdorojimas ----

func (c *Client) handleInput(text string) {
	if strings.HasPrefix(text, "/") {
		parts := strings.SplitN(strings.TrimPrefix(text, "/"), " ", 2)
		cmd := strings.ToLower(parts[0])
		args := ""
		if len(parts) > 1 {
			args = parts[1]
		}
		c.handleCmd(cmd, args)
		return
	}
	c.mu.Lock()
	active := c.activeChanLocked()
	activeName := ""
	if active != nil {
		activeName = active.name
	}
	c.mu.Unlock()
	if activeName == "" || activeName == "*server*" {
		c.serverMsg("Rašyk /join #kanalas kad prisijungtum")
		return
	}
	if !c.isConnected() {
		c.errMsg("Neprisijungta")
		return
	}
	for _, part := range splitForPrivmsg(active.name, text) {
		c.send(fmt.Sprintf("PRIVMSG %s :%s", active.name, part))
		c.addMsg(active.name, Message{ts: time.Now(), nick: c.myNick(), text: part, msgType: MsgChat})
	}
}

func (c *Client) handleCmd(cmd, args string) {
	switch cmd {
	case "connect", "server":
		parts := strings.Fields(args)
		if len(parts) < 2 {
			c.errMsg("Naudojimas: /connect <serveris> <portas> [nick] [tls]")
			return
		}
		if p, err := strconv.Atoi(parts[1]); err != nil || p < 1 || p > 65535 {
			c.errMsg(fmt.Sprintf("Netinkamas porto numeris %q (leistina: 1–65535)", parts[1]))
			return
		}
		if len(parts) >= 3 && !isValidNick(parts[2]) {
			c.errMsg("Netinkamas slapyvardis (leistini simboliai: raidės, skaičiai, _-[]\\^{}|, max 30)")
			return
		}
		// Serverio pavadinimas įrašomas į IRC komandas (NICK, JOIN, USER) ir
		// naudojamas kaip žurnalų katalogo vardas — todėl turi būti
		// apribotas: be tarpų, be CRLF (komandų injekcija), ne per ilgas.
		if !isValidServer(parts[0]) {
			c.errMsg("Netinkamas serverio pavadinimas (leistini simboliai: raidės, skaičiai, . - _ max 100)")
			return
		}
		c.mu.Lock()
		c.server = parts[0]
		c.port = parts[1]
		if len(parts) >= 3 {
			c.nick = parts[2]
		}
		c.useTLS = len(parts) >= 4 && parts[3] == "tls"
		if c.nick == "" {
			c.nick = "mircbsd"
		}
		c.mu.Unlock()
		// Nutraukti seną ryšį ir pradėti naują jungimo ciklą (connectGen)
		c.startConnect()

	case "reconnect":
		c.serverMsg("Jungiamasi iš naujo...")
		c.startConnect()

	case "nick":
		if args == "" {
			c.mu.Lock()
			n := c.nick
			c.mu.Unlock()
			c.serverMsg("Dabartinis slapyvardis: " + n)
			return
		}
		newNick := firstField(args)
		if newNick == "" || !isValidNick(newNick) {
			c.errMsg("Netinkamas slapyvardis (leistini simboliai: raidės, skaičiai, _-[]\\^{}|, max 30)")
			return
		}
		// Serverio NICKLEN riba (kai pranešta 005).
		if !c.isValidNickLen(newNick) {
			c.errMsg(fmt.Sprintf("Slapyvardis per ilgas — serveris leidžia %d simbolius", c.nickLenLimit()))
			return
		}
		c.mu.Lock()
		c.nick = newNick
		conn := c.conn
		c.mu.Unlock()
		if conn != nil {
			c.send("NICK " + newNick)
		}

	case "join", "j":
		if args == "" {
			c.errMsg("Naudojimas: /join #kanalas [slaptažodis]")
			return
		}
		parts := strings.SplitN(args, " ", 2)
		chName := parts[0]
		if !c.isChannel(chName) {
			chName = "#" + chName
		}
		// Kanalo pavadinimas įrašomas į JOIN komandą — turi būti
		// skaidriai ribojamas (tarpai, CRLF, valdymo simboliai
		// neleistini), kad būtų išvengta komandų injekcijos.
		// Prefiksas tikrinamas pagal serverio CHANTYPES — kai serveris
		// naudoja „#$“, kanalas „$kanalas“ yra galiojantis.
		ct, _, _, _, _ := c.isupSnapshot()
		if !isValidChannelFor(chName, ct) {
			c.errMsg("Netinkamas kanalo pavadinimas (raidės, skaičiai ir .-_ ; max 50)")
			return
		}
		// Serverio CHANNELLEN riba (kai pranešta 005).
		if len([]rune(chName)) > c.channelLenLimit() {
			c.errMsg(fmt.Sprintf("Kanalo pavadinimas per ilgas — serveris leidžia %d simbolius", c.channelLenLimit()))
			return
		}
		c.mu.Lock()
		if ch := c.ensureCh(chName); ch == nil {
			// Atmetimas (ribose ar negaliojantis vardas) — įspėjimą
			// sunaudojame TIK kai įsimintas vardas atitinka ŠITĄ kanalą;
			// kitaip (kai priežastis kita) palikume įspėjimą nevalytą —
			// jis priklauso savam kanalui (žr. addMsg).
			if hit := c.chanLimitHit; hit != "" && hit == sanitizeChanName(chName) {
				c.chanLimitHit = ""
				c.mu.Unlock()
				c.errMsg(fmt.Sprintf("ĮSPĖJIMAS: pasiektas kanalų limitas — kanalas „%s“ neatidarytas", hit))
			} else {
				c.mu.Unlock()
			}
		} else {
			c.mu.Unlock()
		}
		if c.isConnected() {
			if len(parts) == 2 && parts[1] != "" {
				// KANALO RAKTAS — jis įrašomas į JOIN komandą kaip
				// parametras. Tarpas yra IRC parametrų skirtukas: rakte
				// su tarpais serveris gavo „JOIN #kanalas raktas papildomas“
				// — papildomą (netiesioginį) kanalą, kurio serveris nekvietė.
				// Rakto NEGALI būti su tarpais, CRLF ar komandų skyrimo
				// simboliais.
				key := parts[1]
				if !isValidChannelKey(key) {
					c.errMsg("Netinkamas kanalo raktas (be tarpų ir specialių simbolių; max 64)")
					return
				}
				c.send("JOIN " + chName + " " + key)
			} else {
				c.send("JOIN " + chName)
			}
		}

	case "part", "leave":
		chName := c.activeName()
		msg := args
		if msg == "" {
			msg = "Viso gero!"
		}
		if c.isConnected() && c.isChannel(chName) {
			c.send(fmt.Sprintf("PART %s :%s", chName, msg))
		}

	case "query":
		// /query <nick> — atidaro PRIVATŲ langą be žinutės (v0.4.7).
		// Anksčiau „query“ buvo tik /msg pseudonimas ir PRIVATŲ langą
		// visą laiką buvo galima atidaryti tik /msg <nick> <žinutė> —
		// bet be žinutės langas atsiveria tik tada, kai kitas jau
		// parašė pirmas.
		if args == "" {
			c.errMsg("Naudojimas: /query <nick> — atidaro privatų langą")
			return
		}
		target := strings.Fields(args)[0]
		if !isValidTarget(target) {
			c.errMsg("Netinkamas nick'as (raidės, skaičiai ir .-_ ; max 100)")
			return
		}
		c.mu.Lock()
		ch := c.ensureCh(target)
		if ch != nil {
			// Kaip PRIVMSG gaunant PM (parseIRC): žymim privačiu —
			// šoninėje sąraše „>nick“.
			ch.isPrivate = true
		}
		// Indeksą imame PAGAL RODUKLĮ toje pačioje rakinoje — eilė gali
		// keistis iš kitų gorutinų tik per lock.
		idx := -1
		for i, chc := range c.channels {
			if chc == ch {
				idx = i
				break
			}
		}
		var hit string
		if ch == nil {
			// Atmetimas (limitas ar negaliojantis vardas) — įspėjimo
			// sunaudojimas tas pats, kaip /join (žr. ten).
			hit = c.chanLimitHit
			if hit != "" && hit == sanitizeChanName(target) {
				c.chanLimitHit = ""
			}
		}
		c.mu.Unlock()
		if ch == nil {
			if hit != "" && hit == sanitizeChanName(target) {
				c.errMsg(fmt.Sprintf("ĮSPĖJIMAS: pasiektas kanalų limitas — langas „%s“ neatidarytas", hit))
			}
			return
		}
		if idx >= 0 {
			c.switchCh(idx)
		}
		c.serverMsg(fmt.Sprintf("Privatus „%s“ langas atidarytas — rašyk „/msg %s <žinutė>“ arba „/me <veiksmas>“", target, target))

	case "msg", "m":
		parts := strings.SplitN(args, " ", 2)
		if len(parts) < 2 {
			c.errMsg("Naudojimas: /msg <nick> <žinutė>")
			return
		}
		target, text := parts[0], parts[1]
		if !isValidTarget(target) {
			c.errMsg("Netinkamas gavėjas (raidės, skaičiai ir .-_ ; max 100)")
			return
		}
		if c.isConnected() {
			for _, part := range splitForPrivmsg(target, text) {
				c.send(fmt.Sprintf("PRIVMSG %s :%s", target, part))
			}
		}
		c.mu.Lock()
		c.ensureCh(target)
		c.mu.Unlock()
		c.addMsg(target, Message{ts: time.Now(), nick: c.myNick(), text: text, msgType: MsgChat})

	case "notice":
		parts := strings.SplitN(args, " ", 2)
		if len(parts) < 2 {
			c.errMsg("Naudojimas: /notice <nick> <žinutė>")
			return
		}
		if !isValidTarget(parts[0]) {
			c.errMsg("Netinkamas gavėjas (raidės, skaičiai ir .-_ ; max 100)")
			return
		}
		if c.isConnected() {
			c.send(fmt.Sprintf("NOTICE %s :%s", parts[0], parts[1]))
			c.serverMsg(fmt.Sprintf("-> -%s- %s", parts[0], parts[1]))
		}

	case "invite":
		parts := strings.Fields(args)
		if len(parts) < 1 {
			c.errMsg("Naudojimas: /invite <nick> [#kanalas]")
			return
		}
		nick := parts[0]
		// Nick'as įrašomas į INVITE komandą — be validacijos jame gali būti
		// tarpas (papildomas parametras) arba CRLF (komandų injekcija).
		if !isValidNick(nick) {
			c.errMsg("Netinkamas nick'as (raidės, skaičiai ir specialūs simboliai; max 30)")
			return
		}
		chName := ""
		if len(parts) >= 2 {
			chName = parts[1]
			// Kanalas įrašomas į komandą kaip TIKSLAS — turi būti
			// serverio CHANTYPES atitinkantis pavadinimas.
			ct, _, _, _, _ := c.isupSnapshot()
			if !isValidChannelFor(chName, ct) {
				c.errMsg("Netinkamas kanalo pavadinimas (raidės, skaičiai ir .-_ ; max 50)")
				return
			}
		} else {
			chName = c.activeName()
		}
		if c.isConnected() {
			c.send(fmt.Sprintf("INVITE %s %s", nick, chName))
			c.serverMsg(fmt.Sprintf("Pakviestas %s į %s", nick, chName))
		}

	case "cycle":
		chName := c.activeName()
		if !c.isChannel(chName) {
			c.errMsg("/cycle galima tik kanale")
			return
		}
		if c.isConnected() {
			msg := args
			if msg == "" {
				msg = "cycle"
			}
			c.send(fmt.Sprintf("PART %s :%s", chName, msg))
			c.send("JOIN " + chName)
		}

	case "op":
		if args == "" {
			c.errMsg("Naudojimas: /op <nick>")
			return
		}
		chName := c.activeName()
		// Nick'as įrašomas į MODE komandą — be validacijos jame gali būti
		// tarpas (papildomas parametras) arba CRLF (komandų injekcija).
		nick := firstField(args)
		if !isValidNick(nick) {
			c.errMsg("Netinkamas nick'as (raidės, skaičiai ir specialūs simboliai; max 30)")
			return
		}
		if c.isConnected() {
			c.send(fmt.Sprintf("MODE %s +o %s", chName, nick))
		}

	case "deop":
		if args == "" {
			c.errMsg("Naudojimas: /deop <nick>")
			return
		}
		chName := c.activeName()
		// Nick'as įrašomas į MODE komandą — be validacijos jame gali būti
		// tarpas (papildomas parametras) arba CRLF (komandų injekcija).
		nick := firstField(args)
		if !isValidNick(nick) {
			c.errMsg("Netinkamas nick'as (raidės, skaičiai ir specialūs simboliai; max 30)")
			return
		}
		if c.isConnected() {
			c.send(fmt.Sprintf("MODE %s -o %s", chName, nick))
		}

	case "voice":
		if args == "" {
			c.errMsg("Naudojimas: /voice <nick>")
			return
		}
		chName := c.activeName()
		// Nick'as įrašomas į MODE komandą — be validacijos jame gali būti
		// tarpas (papildomas parametras) arba CRLF (komandų injekcija).
		nick := firstField(args)
		if !isValidNick(nick) {
			c.errMsg("Netinkamas nick'as (raidės, skaičiai ir specialūs simboliai; max 30)")
			return
		}
		if c.isConnected() {
			c.send(fmt.Sprintf("MODE %s +v %s", chName, nick))
		}

	case "devoice":
		if args == "" {
			c.errMsg("Naudojimas: /devoice <nick>")
			return
		}
		chName := c.activeName()
		// Nick'as įrašomas į MODE komandą — be validacijos jame gali būti
		// tarpas (papildomas parametras) arba CRLF (komandų injekcija).
		nick := firstField(args)
		if !isValidNick(nick) {
			c.errMsg("Netinkamas nick'as (raidės, skaičiai ir specialūs simboliai; max 30)")
			return
		}
		if c.isConnected() {
			c.send(fmt.Sprintf("MODE %s -v %s", chName, nick))
		}

	case "me":
		activeName := c.activeName()
		if c.isConnected() && activeName != "" && activeName != "*server*" {
			c.send(fmt.Sprintf("PRIVMSG %s :\x01ACTION %s\x01", activeName, args))
			c.addMsg(activeName, Message{ts: time.Now(), nick: c.myNick(), text: args, msgType: MsgAction})
		}

	case "topic":
		activeName := c.activeName()
		if c.isConnected() && activeName != "" {
			if args == "" {
				c.send("TOPIC " + activeName)
			} else {
				// TOPICLEN riba (kai pranešta 005) — per ilga tema
				// serveris atmetins, o kai kurie serveriai nutrūksta.
				if len([]rune(args)) > c.topicLenLimit() {
					c.errMsg(fmt.Sprintf("Tema per ilga — serveris leidžia %d simbolius", c.topicLenLimit()))
					return
				}
				c.send(fmt.Sprintf("TOPIC %s :%s", activeName, args))
			}
		}

	case "kick", "k":
		parts := strings.SplitN(args, " ", 2)
		if !c.isConnected() || len(parts) < 1 || parts[0] == "" {
			c.errMsg("Naudojimas: /kick <nick> [priežastis]")
			return
		}
		chName := c.activeName()
		if !c.isChannel(chName) {
			c.errMsg("/kick galima tik kanale")
			return
		}
		// Nick'as įrašomas į KICK komandą — be validacijos jame gali būti
		// tarpas (papildomas parametras) arba CRLF (komandų injekcija).
		if !isValidNick(parts[0]) {
			c.errMsg("Netinkamas nick'as (raidės, skaičiai ir specialūs simboliai; max 30)")
			return
		}
		reason := "Kicked"
		if len(parts) == 2 && parts[1] != "" {
			reason = parts[1]
		}
		// KICKLEN riba (kai pranešta 005).
		if c.kickLenLimit() > 0 && len([]rune(reason)) > c.kickLenLimit() {
			c.errMsg(fmt.Sprintf("Priežastis per ilga — serveris leidžia %d simbolius", c.kickLenLimit()))
			return
		}
		c.send(fmt.Sprintf("KICK %s %s :%s", chName, parts[0], reason))

	case "mode":
		if !c.isConnected() || args == "" {
			c.errMsg("Naudojimas: /mode <target|+/-mode> [parametrai]")
			return
		}
		fields := strings.Fields(args)
		if len(fields) == 0 {
			c.errMsg("Naudojimas: /mode <target|+/-mode> [parametrai]")
			return
		}
		// Jei pirmasis argumentas prasideda +/-, taikome aktyviam kanalui
		if strings.HasPrefix(fields[0], "+") || strings.HasPrefix(fields[0], "-") {
			chName := c.activeName()
			if !c.isChannel(chName) {
				c.errMsg("/mode galima tik kanale")
				return
			}
			if !isValidModeSpec(fields[1:]) {
				c.errMsg("Netinkami mode parametrai (per daug parametrų arba specialūs simboliai)")
				return
			}
			c.send("MODE " + chName + " " + strings.Join(fields, " "))
		} else {
			// TIKSLAS (kanalas, nick'as ar serverio kaukė) — jis įrašomas
			// į komandą kaip pirmas parametras. Be validacijos galėjo būti
			// „*server*“-style kaukė su papildomais parametrais.
			if !isValidModeTarget(fields[0]) {
				c.errMsg("Netinkamas MODE tikslas (kanalas, nick'as ar serverio kaukė)")
				return
			}
			if !isValidModeSpec(fields[1:]) {
				c.errMsg("Netinkami mode parametrai (per daug parametrų arba specialūs simboliai)")
				return
			}
			c.send("MODE " + strings.Join(fields, " "))
		}

	case "whois", "wi":
		if w := firstField(args); c.isConnected() && w != "" {
			// Nick'as įrašomas į WHOIS komandą — be validacijos jame gali
			// būti tarpas (papildomas parametras).
			if !isValidNick(w) {
				c.errMsg("Netinkamas nick'as (raidės, skaičiai ir specialūs simboliai; max 30)")
				return
			}
			// WHOIS atsakymai rodomi aktyviame kanale (jei esame kanale) —
			// anksčiau jie visada eidavo į *server* langą.
			if act := c.activeName(); c.isChannel(act) {
				c.setWhoisTarget(act)
			} else {
				c.setWhoisTarget("")
			}
			c.send("WHOIS " + w)
		}

	case "away":
		if args == "" {
			c.mu.Lock()
			c.away = ""
			c.mu.Unlock()
			if c.isConnected() {
				c.send("AWAY")
			}
			c.serverMsg("Away išjungtas")
		} else {
			c.mu.Lock()
			c.away = args
			c.mu.Unlock()
			if c.isConnected() {
				c.send("AWAY :" + args)
			}
			c.serverMsg("Away: " + args)
		}
		c.queueRedraw()

	case "time":
		if c.isConnected() {
			c.send("TIME")
		} else {
			c.errMsg("Neprisijungta")
		}

	case "clear":
		c.mu.Lock()
		// c.mu jau užrakintas — Locked variantas.
		activeCh := c.activeChanLocked()
		if activeCh == nil {
			c.mu.Unlock()
			return
		}
		activeCh.messages = nil
		// Svarbu: nuplauti ir aktyvios sesijos slinktį — kitaip buvusi
		// pozicija nuo apačios lieka „kabanti" tuščioje istorijoje, o
		// naujos žinutės dar labiau jos nesumažina (langas lieka tuščias).
		c.scrollOff = 0
		c.chaseUntil = time.Time{}
		activeCh.scrollOff = 0
		activeCh.renderGen++
		// Cache'ą reikia IŠTRINTI, ne tik „panaikinti“: senos eilutės
		// liko cache'e, o žinutės buvo ištrintos — inkrementalus cache'as
		// tikėtųsi, kad cache'e yra N žinučių, kurių jau nėra.
		activeCh.rendered = nil
		activeCh.renderedGen = -1
		activeCh.renderedMsgCount = 0
		c.mu.Unlock()
		c.queueRedraw()

	case "clearlogs":
		// Be argumentų — trinami VISI šio serverio logai (kaip nurodo /help).
		// Su argumentu — tik to kanalo logai.
		chanName := args
		if chanName != "" {
			// Kanalas gali NEBŪTI šiame seanse — jo žurnalai vis tiek
			// gali egzistuoti (anksčiau buvome prisijungę, arba serveris
			// siuntė žinutes į kanalą, kurį vėliau palikome). Tokiu atveju
			// ankstesnė elgsena atmetdavo komandą su klaida — o tai yra
			// neteisinga: vartotojas nori IŠTRINTI tuos failus.
			//
			// Saugumas: pavadinimas sanitizuojamas clearLogs() viduje
			// (sanitizePath — be „..“, be kelio separatorių), todėl
			// komandos tikslo klastojimas neįmanomas.
			//
			// Čia NEsanitizuojame — sanituoja clearLogs(), o etiketė
			// (rodoma vartotojui) turi likti originali.
			c.mu.Lock()
			target := c.findCh(chanName)
			c.mu.Unlock()
			_ = target // nėra šiame seanse — bet žurnalai gali būti diske
		}
		n := c.clearLogs(chanName)
		label := chanName
		if label == "" {
			label = "šis serveris"
		}
		if n == 0 {
			c.serverMsg("Log failų („" + label + "“) nerasta")
		} else {
			c.serverMsg(fmt.Sprintf("Išvalyta %d log failas(-ai) („%s“)", n, label))
		}

	case "names":
		c.mu.Lock()
		// c.mu jau užrakintas — Locked variantas.
		active := c.activeChanLocked()
		chName := ""
		if active != nil {
			chName = active.name
		}
		do := c.connected && c.isChannel(chName)
		if do {
			active.nicks = nil
			// Žemėlapis turi būti išvalomas TOGIU PAT — priešingu atveju
			// liko seni indeksai ir addNick/removeNick nebesutaptų su
			// tikrais nick'ais.
			active.nickSet = nil
		}
		c.mu.Unlock()
		if do {
			c.send("NAMES " + chName)
		}

	case "back":
		c.mu.Lock()
		c.away = ""
		c.mu.Unlock()
		if c.isConnected() {
			c.send("AWAY")
		}
		c.serverMsg("Away išjungtas")
		c.queueRedraw()

	case "ignore":
		nick := firstField(args)
		if nick == "" {
			c.errMsg("Naudojimas: /ignore <nick>")
			return
		}
		c.mu.Lock()
		c.ignoreList = append(c.ignoreList, nick)
		c.mu.Unlock()
		c.serverMsg("Ignoruojamas: " + nick)
		go c.saveIgnore()

	case "unignore":
		nick := firstField(args)
		if nick == "" {
			c.errMsg("Naudojimas: /unignore <nick>")
			return
		}
		c.mu.Lock()
		// NAUJA kopija, ne filtravimas in-place: senas backing array
		// galėjo būti perskaitytas kito goroutine (isIgnored) — in-place
		// filtravimas padaro ją laikinai klaida (dubliuoti elementai).
		newList := make([]string, 0, len(c.ignoreList))
		for _, ig := range c.ignoreList {
			if !strings.EqualFold(ig, nick) {
				newList = append(newList, ig)
			}
		}
		c.ignoreList = newList
		c.mu.Unlock()
		c.serverMsg("Ignoravimas pašalintas: " + nick)
		go c.saveIgnore()

	case "ignores":
		c.mu.Lock()
		list := make([]string, len(c.ignoreList))
		copy(list, c.ignoreList)
		c.mu.Unlock()
		if len(list) == 0 {
			c.serverMsg("Ignoruojamų sąrašas tuščias")
		} else {
			c.serverMsg("Ignoruojami: " + strings.Join(list, ", "))
		}

	case "list":
		if c.isConnected() {
			if args != "" {
				// LIST filtras įrašomas į komandą kaip parametras —
				// be validacijos jame gali būti tarpas (papildomas
				// parametras) arba CRLF.
				if !isValidListFilter(args) {
					c.errMsg("Netinkamas LIST filtras (be tarpų ir specialių simbolių; max 64)")
					return
				}
				c.send("LIST " + args)
			} else {
				c.send("LIST")
			}
		}

	case "search":
		// „/search all <žodis>“ — mažosiomis/be didžiųjų („ALL“, „All“);
		// ir „/search all“ BE tarpo paskui — toliau įprasta paieška žodžiui
		// „all“ (seniau jo ieškoti NEgalėjai — „all“ buvo atsvertas).
		low := strings.ToLower(args)
		if low == "all " || strings.HasPrefix(low, "all ") {
			// /search all <žodis> — paieška VISUOSE languose, rezultatai
			// į *server* langą (v0.4.8).
			q := ""
			if len(args) > 4 {
				q = strings.TrimSpace(args[4:])
			}
			if q == "" {
				c.errMsg("Naudojimas: /search all <žodis>")
				return
			}
			c.searchAllChannels(q)
			return
		}
		if args == "" {
			c.mu.Lock()
			c.searchMode = true
			c.searchQuery = ""
			c.mu.Unlock()
		} else {
			c.mu.Lock()
			// c.mu jau užrakintas — Locked variantas.
			active := c.activeChanLocked()
			c.mu.Unlock()
			if active != nil {
				c.doSearch(active, args)
			}
		}
		c.queueRedraw()

	case "set":
		// /set <key> [value] (v0.4.8): žiūrėti / keisti nustatymą gyvai,
		// reikšmė įrašoma ATGAL į konfigūracijos failą (atominis perrašymas).
		if args == "" {
			c.listSettings()
			return
		}
		parts := strings.Fields(args)
		key := strings.ToLower(parts[0])
		if !isSettable(key) {
			c.errMsg("Nežinomas arba nekeičiamas raktas: " + sanitizeText(parts[0]))
			return
		}
		if len(parts) == 1 {
			c.showSetting(key)
			return
		}
		value := strings.TrimSpace(strings.TrimPrefix(args, parts[0]))
		c.applySetting(key, value)
		return

	case "notify":
		// /notify add|del|list|be-args (v0.4.8) — MONITOR stebimų nick'ų
		// sąrašas. Serveriui MONITOR ± (nick1,nick2). Būsena rodoma
		// po 730 (online) / 731 (offline).
		if args == "" || strings.EqualFold(strings.TrimSpace(args), "list") {
			c.mu.Lock()
			list := make([]string, len(c.monitorList))
			copy(list, c.monitorList)
			online := make(map[string]bool, len(c.monitorOnline))
			seen := make(map[string]bool, len(c.monitorOnline))
			for k, v := range c.monitorOnline {
				online[k] = v
				seen[k] = true
			}
			c.mu.Unlock()
			if len(list) == 0 {
				c.serverMsg("/notify sąrašas tuščias. Naudojimas: /notify add <nick>  |  /notify del <nick>")
				return
			}
			// Būsenos žemėlapis raktas — FOLDINTAS nick'as (monKey):
			// serverio „Bob“ ir sąraše esantis „bob“ — tas patis.
			for _, n := range list {
				key := c.monKey(n)
				if online[key] {
					c.serverMsg("notify: " + n + " — online")
				} else if seen[key] {
					c.serverMsg("notify: " + n + " — offline")
				} else {
					// Kol dar nėra MONITOR atsakymų (tik po prisijungimo
					// 730/731) būsena nežinoma.
					c.serverMsg("notify: " + n + " — nežinoma")
				}
			}
			return
		}
		sub := strings.Fields(args)
		if len(sub) < 2 {
			c.errMsg("Naudojimas: /notify add <nick>  |  /notify del <nick>  |  /notify list")
			return
		}
		switch strings.ToLower(sub[0]) {
		case "add":
			added := make([]string, 0)
			for _, nick := range sub[1:] {
				nick = strings.TrimSuffix(nick, ",")
				if !isValidNick(nick) {
					c.errMsg("Nevalidus nick: " + sanitizeText(nick))
					continue
				}
				c.mu.Lock()
				exists := false
				for _, n := range c.monitorList {
					if c.nickEqual(n, nick) {
						exists = true
						break
					}
				}
				// Serverio MONITOR limitas (005 „MONITOR=N“): anksčiau jis
				// buvo skaitomas, bet NIEKADA nenaudotas — serveris likučius
				// tyliai atmetė per 734. Dabar ribojame patys.
				c.isupMu.RLock()
				monLimit := c.limits.Monitor
				c.isupMu.RUnlock()
				if !exists && monLimit > 0 && len(c.monitorList) >= monLimit {
					c.mu.Unlock()
					c.errMsg("MONITOR sąrašas pilnas (limitas " + strconv.Itoa(monLimit) + ") — „" + sanitizeText(nick) + "“ nepridėtas")
					continue
				}
				if !exists {
					c.monitorList = append(c.monitorList, nick)
				}
				c.mu.Unlock()
				if exists {
					c.serverMsg("notify: " + nick + " jau sąraše")
				} else {
					c.serverMsg("notify: " + nick + " pridėtas")
					added = append(added, nick)
				}
			}
			if len(added) > 0 {
				c.send("MONITOR + " + strings.Join(added, ","))
			}
			c.saveMonitor()
		case "del", "del_", "delete", "remove":
			removed := make([]string, 0)
			for _, nick := range sub[1:] {
				nick = strings.TrimSuffix(nick, ",")
				c.mu.Lock()
				for i, n := range c.monitorList {
					if c.nickEqual(n, nick) {
						c.monitorList = append(c.monitorList[:i], c.monitorList[i+1:]...)
						removed = append(removed, n)
						break
					}
				}
				// monitorOnline raktas — FOLDINTAS (monKey), kitaip
				// „Bob“ dingęs iš sąrašo, bet liktų „online“.
				delete(c.monitorOnline, c.monKey(nick))
				c.mu.Unlock()
			}
			if len(removed) == 0 {
				c.serverMsg("notify: niekos iš „" + sanitizeText(strings.Join(sub[1:], ", ")) + "“ nerasta sąraše")
			} else {
				c.send("MONITOR - " + strings.Join(removed, ","))
				c.saveMonitor()
			}
		default:
			c.errMsg("Naudojimas: /notify add <nick>  |  /notify del <nick>  |  /notify list")
		}

	case "exportjson":
		// /exportjson [kanalas] (v0.4.8): eksportuoti lango žinutes į JSON
		// (logDir/<serveris>/<kanalas>-YYYY-MM-DD.json). Failas 0600,
		// katalogas 0700. Be argumento — aktyvus langas (jei tai *server*);
		// „*server*“ eksportui leidžiama).
		target := strings.TrimSpace(args)
		if target == "" {
			c.mu.Lock()
			active := c.activeChanLocked()
			c.mu.Unlock()
			if active == nil {
				c.errMsg("Nėra aktyvaus lango")
				return
			}
			target = active.name
		}
		c.exportJSON(target)
		return

	case "logsearch":
		// /logsearch [kanalas] <žodis> — paieška ĮŠAKOMUOSE žurnalo failuose
		// (v0.4.8). Rezultatai į *server* langą.
		if args == "" {
			c.errMsg("Naudojimas: /logsearch [kanalas] <žodis>")
			return
		}
		fields := strings.Fields(args)
		query := ""
		chanFilter := ""
		if len(fields) == 1 {
			query = fields[0]
		} else {
			// Pirmas laukas — kanalas, jei atrodo kaip kanalas, kitaip visas
			// tekstas yra užklausa.
			chanFilter = fields[0]
			q2 := strings.TrimSpace(strings.TrimPrefix(args, fields[0]))
			if !c.isChannel(chanFilter) {
				chanFilter = ""
				query = args
			} else {
				query = q2
			}
		}
		if query == "" {
			c.errMsg("Naudojimas: /logsearch [kanalas] <žodis>")
			return
		}
		c.logSearch(chanFilter, query)
		return

	case "open":
		c.mu.Lock()
		urls := make([]string, len(c.lastURLs))
		copy(urls, c.lastURLs)
		c.mu.Unlock()
		if len(urls) == 0 {
			c.errMsg("Nėra URL. Naudok /urls kad pamatytum istoriją")
			return
		}
		url := urls[len(urls)-1]
		if args != "" {
			if n, err := strconv.Atoi(args); err == nil && n >= 1 && n <= len(urls) {
				url = urls[n-1]
			} else {
				c.errMsg(fmt.Sprintf("/open [n] — n nuo 1 iki %d", len(urls)))
				return
			}
		}
		c.serverMsg("Atidaroma: " + url)
		go func(u string) {
			if err := openURL(u); err != nil {
				c.errMsg(fmt.Sprintf("Nepavyko atidaryti naršyklėje: %v", err))
			}
		}(url)

	case "urls":
		c.mu.Lock()
		urls := make([]string, len(c.lastURLs))
		copy(urls, c.lastURLs)
		c.mu.Unlock()
		if len(urls) == 0 {
			c.serverMsg("URL istorija tuščia")
		} else {
			for i, u := range urls {
				c.serverMsg(fmt.Sprintf("  %d: %s", i+1, u))
			}
		}

	case "raw":
		if c.isConnected() && args != "" {
			c.send(args)
		}

	case "w":
		// /w — WHO (kaip visuose IRC klientuose). Anksčiau /w rodė langų
		// sąrašą, todėl WHO komanda niekada nesiunčiama, o 352/354/315
		// numerikai liko neapdoroti.
		target := ""
		if args != "" {
			target = firstField(args)
			// WHO tikslas įrašomas į komandą kaip parametras — be
			// validacijos jame gali būti tarpas (papildomas parametras)
			// arba CRLF.
			if !isValidWhoTarget(target) {
				c.errMsg("Netinkamas WHO tikslas (kanalas, nick'as arba „*“)")
				return
			}
		}
		if target == "" {
			c.mu.Lock()
			// c.mu jau užrakintas — Locked variantas.
			if ch := c.activeChanLocked(); ch != nil {
				target = ch.name
			}
			c.mu.Unlock()
		}
		if target == "" {
			target = "*"
		}
		// Atsakymus rodykime kanale, kuriame užklausta (jei tai kanalas) —
		// anksčiau jie visada eidavo į *server* langą ir kanale buvo nematomi.
		if c.isChannel(target) {
			c.setWhoTarget(target)
		} else {
			c.setWhoTarget("")
		}
		c.send("WHO " + target)

	case "win", "window":
		if args == "" {
			c.mu.Lock()
			type winRow struct {
				idx  int
				name string
			}
			rows := make([]winRow, len(c.channels))
			for i, ch := range c.channels {
				rows[i] = winRow{i, ch.name}
			}
			activeIdx := c.activeIdx
			c.mu.Unlock()
			for _, r := range rows {
				marker := "  "
				if r.idx == activeIdx {
					marker = "> "
				}
				c.serverMsg(fmt.Sprintf("%s%d: %s", marker, r.idx+1, r.name))
			}
			return
		}
		if w := firstField(args); w != "" {
			if n, err := strconv.Atoi(w); err == nil {
				c.switchCh(n - 1)
			}
		}

	case "close", "wc":
		c.mu.Lock()
		idx := c.activeIdx
		chName := ""
		if args != "" {
			// /close <kanalas> — uždaro nurodytą langą (jei egzistuoja)
			for i, ch := range c.channels {
				if strings.EqualFold(ch.name, sanitizeText(args)) {
					idx = i
					break
				}
			}
		}
		if idx <= 0 || idx >= len(c.channels) {
			// 0 = *server* langas (neuždarytinas); neegalius indeksas — ignoruoti
			c.mu.Unlock()
			return
		}
		chName = c.channels[idx].name
		c.removeCh(idx)
		c.mu.Unlock()
		if c.isConnected() && c.isChannel(chName) {
			c.send("PART " + chName + " :Langas uždarytas")
		}
		c.queueRedraw()

	case "quit", "exit", "q":
		msg := args
		if msg == "" {
			msg = "mircBSD"
		}
		if c.isConnected() {
			c.send(fmt.Sprintf("QUIT :%s", msg))
			// Palaukiame, kol rašytoja goroutine QUIT realiai išsiuntė ir
			// flush'ino — kitaip procesas gali pasibaigti dar prieš send'o
			// buferio įrašymą ir kiti vartotojai QUIT nematys (atrodys,
			// kad nutrūko ryšys). Daugiausia 300 ms — ir taip eilė ištuštėja
			// akimirksniu, laukimas tik apsauginis.
			c.finishWrites(300 * time.Millisecond)
		}
		c.stop()

	case "keys":
		// /keys (v0.4.8): be argumentų — per naujo skaitome keys failą
		// (taip pakeitimai įgauna galvai neperstataus paleidimo) ir
		// rodomi aktyvūs perrašymai. /keys debug — sekų debug režimas.
		if strings.TrimSpace(args) == "debug" {
			c.mu.Lock()
			c.keysDebug = true
			c.mu.Unlock()
			c.serverMsg("[KEYS] debug įjungtas. 'q' - išjungti")
			return
		}
		for _, w := range c.loadKeys() {
			c.errMsg(w)
		}
		c.showKeyBindings()

	case "help":
		lines := []string{
			"══ mircBSD v" + version + " komandos ════════════════════════",
			"/connect <srv> <port> [nick] [tls]  prisijungti",
			"/reconnect                           jungti iš naujo",
			"/join #kanalas [slaptažodis]         įeiti į kanalą",
			"/part [priežastis]                   išeiti",
			"/cycle [priežastis]                  išeiti ir vėl įeiti",
			"/close [kanalas]                    uždaryti langą (+ PART)",
			"/msg <nick> <žinutė>                 privati žinutė",
			"/query <nick>                        atidaryti privatų langą",
			"/notice <nick> <žinutė>              notice žinutė",
			"/invite <nick> [#kanalas]            pakviesti į kanalą",
			"/me <veiksmas>                       veiksmas",
			"/nick <vardas>                       keisti nick",
			"/topic [tema]                        rodyti/keisti temą",
			"/kick <nick> [priežastis]            išmesti iš kanalo",
			"/op <nick>                           suteikti op",
			"/deop <nick>                         atimti op",
			"/voice <nick>                        suteikti voice",
			"/devoice <nick>                      atimti voice",
			"/mode [target] <+/-mode> [param]     nustatyti mode",
			"/away [priežastis]                   away / grįžti",
			"/back                                grįžti iš away",
			"/ignore <nick>                       ignoruoti",
			"/unignore <nick>                     atšaukti",
			"/ignores                             ignoruojamų sąrašas",
			"/list [filtras]                      kanalų sąrašas",
			"/names                               atnaujinti nick sąrašą",
			"/search [žodis]                      paieška (Ctrl+R)",
			"/search all <žodis>                  paieška visose langoje",
			"/logsearch [kanalas] <žodis>         paieška žurnaluose (failuose)",
			"/set <raktas> [reikšmė]              nustatymas (+ į config)",
			"/exportjson [kanalas]                eksportas JSON",
			"/notify add|del|list <nick>          MONITOR žiūrimi nick'ai",
			"/keys                                klavišų perrašymas (keys failas)",
			"/time                                serverio laikas",
			"/whois <nick>                        informacija",
			"/raw <IRC komanda>                   tiesioginė",
			"/urls                                URL istorija",
			"/open [n]                            atidaryti URL naršyklėje",
			"/clear                               išvalyti kanalo istoriją",
			"/clearlogs [kanalas]                 ištrinti log failus (numatytai: visi šiame serveryje)",
			"/w [mask]                            WHO (serverio vartotojai)",
			"/window [n]                          langų sąrašas / perjungti",
			"/quit                                išeiti",
			"───────────────────────────────────────────────────",
			"PgUp/PgDn       scroll      ↑↓   scroll / istorija",
			"Shift+PgDn      grįžti apačion (šuolis iš istorijos)",
			"Ctrl+N/P        kanalai     Ctrl+R   paieška",
			"Ctrl+L          ekrano atnaujinimas  Ctrl+W  ištrinti žodį",
			"Ctrl+←/→        žodžio navigacija",
			"F1-F12 / Alt+1-9  kanalas pagal numerį",
			"Tab             autocomplete   Ctrl+↑↓  nick scroll",
			"───────────────────────────────────────────────────",
			"/ignore dabar palaiko maskes: nick!user@host, *!user@*, *",
		}
		for _, l := range lines {
			c.serverMsg(l)
		}

	default:
		c.errMsg(fmt.Sprintf("Nežinoma komanda: /%s  (naudok /help)", cmd))
	}
}

// splitPasteLines skaido paste gabaliuką į pilnas eilutes + galimą nepilną
// „uodegą" (gabaliukas dažnai baigiasi eilutės viduryje, vėlesnis tęsinys
// ateina atskirame skaityme).
func splitPasteLines(raw string) (lines []string, partial string) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	if raw != "" && !strings.HasSuffix(raw, "\n") {
		i := strings.LastIndex(raw, "\n")
		if i == -1 {
			return nil, raw
		}
		return strings.Split(raw[:i], "\n"), raw[i+1:]
	}
	return strings.Split(strings.TrimRight(raw, "\n"), "\n"), ""
}

// collectPaste prideda paste tęsinį į buferį ir pratęsia surinkimo langą.
func (c *Client) collectPaste(chunk string) {
	if c.pastePartial != "" {
		chunk = c.pastePartial + chunk
	}
	lines, partial := splitPasteLines(chunk)
	added := 0
	for _, l := range lines {
		if l == "" {
			continue
		}
		if len(c.pasteBuf) >= 200 {
			break
		}
		c.pasteBuf = append(c.pasteBuf, l)
		added++
	}
	if added > 0 || partial != "" {
		c.pasteCollectUntil = time.Now().Add(30 * time.Millisecond)
	}
	c.pastePartial = partial
}

// handleMouse apdoroja SGR 1006 pelės įvykį: \x1b[<b;x;yM|m.
// Ratukas scrollina; atleistas paspaudimas kanalo juostoje perjungia kanalą.
func (c *Client) handleMouse(b []byte) {
	if !c.mouse {
		return
	}
	s := string(b)
	if !strings.HasPrefix(s, "\x1b[<") {
		return
	}
	end := s[len(s)-1]
	if end != 'M' && end != 'm' {
		return // nebaigta seka
	}
	body := strings.TrimPrefix(s, "\x1b[<")
	body = strings.TrimSuffix(body, string(end))
	// Dalinama nuo dešinės, jei eilutėje priklijuoti kiti simboliai
	i := strings.LastIndex(body, ";")
	if i < 0 {
		return
	}
	xy := strings.Split(body[:i], ";")
	if len(xy) != 2 {
		return
	}
	btn, err1 := strconv.Atoi(xy[0])
	x, err2 := strconv.Atoi(xy[1])
	y, err3 := strconv.Atoi(body[i+1:])
	if err1 != nil || err2 != nil || err3 != nil {
		return
	}
	switch {
	case btn == 64: // ratukas aukštyn
		c.mu.Lock()
		c.scrollOff += 3
		c.mu.Unlock()
		c.queueRedraw()
	case btn == 65: // ratukas žemyn
		c.mu.Lock()
		c.scrollOff -= 3
		if c.scrollOff < 0 {
			c.scrollOff = 0
		}
		if c.scrollOff > 0 {
			c.chaseUntil = time.Now().Add(1500 * time.Millisecond)
		}
		c.mu.Unlock()
		c.queueRedraw()
	case end == 'm': // atleistas paspaudimas
		c.mu.Lock()
		top := c.msgAreaTop
		nChan := len(c.channels)
		// Slinktis tarpininkauja perskaičiuodamas ekrano eilutę į kanalo
		// indeksą — offsetas PRIEŠ tai SUGRUBINAMAS (terminalo perdarymas
		// gali būti sumažinęs msgRows nuo paskutinio slinkties keitimo;
		// draw() sugrubina tik savo kopiją — čia naudojame įRAŠYTĄ vertę).
		c.clampChanScrollLocked()
		chanScroll := c.chanScrollOff
		c.mu.Unlock()
		// Kanalų sąrašas gali būti paslinktas — spustelėjusio eilutės
		// kanalas atsiranda ne tiesioginiu indeksu, o su slinkties
		// poslinkiu.
		idx := y - top + chanScroll
		if x <= chanListWidth && idx >= 0 && idx < nChan {
			c.switchCh(idx)
		}
	}
}
