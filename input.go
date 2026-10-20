package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ---- Klavišai ----

func (c *Client) readKeys() {
	buf := make([]byte, 64)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			// stdin uždarytas/nutrūkęs — klaviatūra nebeveikia, saugiai išeiti
			c.stop()
			return
		}
		c.handleKey(buf[:n])
	}
}

func (c *Client) handleKey(b []byte) {
	if c.keysDebug {
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
				active := c.activeChan()
				c.mu.Unlock()
				if c.isConnected() && active != nil && active.name != "*server*" {
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

	// Ctrl+rodyklė
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
			active := c.activeChan()
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
			// scrollOff matuojamas BRAIŽOMOMIS eilutėmis, ne žinutėmis —
			// ilga žinutė užima kelias eilutes, todėl ankstyvesnis
			// „totalMsgs - i - 1“ rodė ne ten, kur rasta žinutė yra.
			// Astatom langą taip, kad rastos žinutės paskutinė eilutė
			// atsidurtų matomo lango apačioje.
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
			// langas žemiau matomų eilučių, paskutinė rastos žinutės eilutė apačioje:
			// paskutinės eilutės indeksas = firstLine + foundCount - 1
			c.scrollOff = totalLines - (firstLine + foundCount)
			if c.scrollOff < 0 {
				c.scrollOff = 0
			}
			c.searchResultIdx = i
			c.lastSearchQuery = query
			c.searchMode = false
			return
		}
	}
	// Nerasta - palikti paieškos režimą (gal pradžia pasiekta)
	c.searchResultIdx = -1
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
	"/unignore", "/urls", "/w", "/wc", "/whois", "/wi", "/win",
	"/help", "/keys",
}

func (c *Client) doTab() {
	c.mu.Lock()
	inputStr := string(c.input[:c.inputPos])
	active := c.activeChan()
	c.mu.Unlock()
	if active == nil {
		return
	}

	if len(c.tabCandidates) > 0 {
		c.tabIdx = (c.tabIdx + 1) % len(c.tabCandidates)
		chosen := c.tabCandidates[c.tabIdx]
		c.mu.Lock()
		wb := wordBefore(c.tabPrefix)
		prefix := c.tabPrefix[:len([]rune(c.tabPrefix))-len([]rune(wb))]
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
	} else {
		seen := map[string]bool{}
		for _, n := range active.nicks {
			bare := stripMode(n, active.prefixSigns())
			if !seen[bare] && strings.HasPrefix(strings.ToLower(bare), wordLow) {
				seen[bare] = true
				candidates = append(candidates, bare)
			}
		}
		for _, m := range active.messages {
			if m.nick != "" && !seen[m.nick] {
				seen[m.nick] = true
				if strings.HasPrefix(strings.ToLower(m.nick), wordLow) {
					candidates = append(candidates, m.nick)
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
	prefix := inputStr[:len([]rune(inputStr))-len([]rune(word))]
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

// isSensitiveHistory – komandos, kuriose gali būti slaptažodis (NickServ
// IDENTIFY/REGISTER/CONFIRM/SET PASSWORD), nesaugomos įvesties istorijos faile.
func isSensitiveHistory(line string) bool {
	low := strings.ToLower(line)
	// /msg nickserv identify <pass> / register <pass> <email> / confirm <kodas> /
	// set password <pass>
	const nsPrefix = "msg nickserv"
	nickCmd := strings.HasPrefix(low, "/ns ") ||
		strings.HasPrefix(low, "/nickserv ") ||
		strings.HasPrefix(low, "/squery ") ||
		strings.Contains(low, nsPrefix)
	if !nickCmd {
		// Ne NickServ komanda — žymime (senos versijos elgsena) tik
		// eilutes su „identify“ žodžiu
		return strings.Contains(low, "identify")
	}
	for _, kw := range []string{"identify", "ident ", "register", "confirm", "set password", "auth"} {
		if strings.Contains(low, kw) {
			return true
		}
	}
	return false
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
	active := c.activeChan()
	c.mu.Unlock()
	if active == nil || active.name == "*server*" {
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
		if !isValidChannel(chName) {
			c.errMsg("Netinkamas kanalo pavadinimas (raidės, skaičiai ir .-_ ; max 50)")
			return
		}
		c.mu.Lock()
		c.ensureCh(chName)
		c.mu.Unlock()
		if c.isConnected() {
			if len(parts) == 2 && parts[1] != "" {
				c.send("JOIN " + chName + " " + parts[1])
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

	case "msg", "query", "m":
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
		chName := ""
		if len(parts) >= 2 {
			chName = parts[1]
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
		if c.isConnected() {
			c.send(fmt.Sprintf("MODE %s +o %s", chName, firstField(args)))
		}

	case "deop":
		if args == "" {
			c.errMsg("Naudojimas: /deop <nick>")
			return
		}
		chName := c.activeName()
		if c.isConnected() {
			c.send(fmt.Sprintf("MODE %s -o %s", chName, firstField(args)))
		}

	case "voice":
		if args == "" {
			c.errMsg("Naudojimas: /voice <nick>")
			return
		}
		chName := c.activeName()
		if c.isConnected() {
			c.send(fmt.Sprintf("MODE %s +v %s", chName, firstField(args)))
		}

	case "devoice":
		if args == "" {
			c.errMsg("Naudojimas: /devoice <nick>")
			return
		}
		chName := c.activeName()
		if c.isConnected() {
			c.send(fmt.Sprintf("MODE %s -v %s", chName, firstField(args)))
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
		reason := "Kicked"
		if len(parts) == 2 && parts[1] != "" {
			reason = parts[1]
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
			c.send("MODE " + chName + " " + args)
		} else {
			c.send("MODE " + args)
		}

	case "whois", "wi":
		if w := firstField(args); c.isConnected() && w != "" {
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
		activeCh := c.activeChan()
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
		c.mu.Unlock()
		c.queueRedraw()

	case "clearlogs":
		// Be argumentų — trinami VISI šio serverio logai (kaip nurodo /help).
		// Su argumentu — tik to kanalo logai.
		chanName := args
		if chanName != "" {
			c.mu.Lock()
			target := c.findCh(chanName)
			c.mu.Unlock()
			if target == nil {
				c.errMsg("Kanalo „" + chanName + "“ nėra šiame seanse")
				return
			}
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
		active := c.activeChan()
		chName := ""
		if active != nil {
			chName = active.name
		}
		do := c.connected && c.isChannel(chName)
		if do {
			active.nicks = nil
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
		newList := c.ignoreList[:0]
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
				c.send("LIST " + args)
			} else {
				c.send("LIST")
			}
		}

	case "search":
		if args == "" {
			c.mu.Lock()
			c.searchMode = true
			c.searchQuery = ""
			c.mu.Unlock()
		} else {
			c.mu.Lock()
			active := c.activeChan()
			c.mu.Unlock()
			if active != nil {
				c.doSearch(active, args)
			}
		}
		c.queueRedraw()

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

	case "w", "win", "window":
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
		}
		c.stop()

	case "keys":
		c.mu.Lock()
		c.keysDebug = true
		c.mu.Unlock()
		c.serverMsg("[KEYS] debug įjungtas. 'q' - išjungti")

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
			"/time                                serverio laikas",
			"/whois <nick>                        informacija",
			"/raw <IRC komanda>                   tiesioginė",
			"/urls                                URL istorija",
			"/open [n]                            atidaryti URL naršyklėje",
			"/clear                               išvalyti kanalo istoriją",
			"/clearlogs [kanalas]                 ištrinti log failus (numatytai: visi šiame serveryje)",
			"/w [n]                               langų sąrašas",
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
		c.mu.Unlock()
		idx := y - top
		if x <= chanListWidth && idx >= 0 && idx < nChan {
			c.switchCh(idx)
		}
	}
}
