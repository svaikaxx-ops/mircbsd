package main

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// ---- ANSI ----

const (
	ansiReset    = "\033[0m"
	ansiBold     = "\033[1m"
	ansiItalic   = "\033[3m"
	ansiUnder    = "\033[4m"
	ansiReverse  = "\033[7m"
	ansiRed      = "\033[31m"
	ansiGreen    = "\033[32m"
	ansiYellow   = "\033[33m"
	ansiBlue     = "\033[34m"
	ansiMagenta  = "\033[35m"
	ansiCyan     = "\033[36m"
	ansiWhite    = "\033[37m"
	ansiBgBlue   = "\033[44m"
	ansiBgRed    = "\033[41m"
	ansiDarkGray = "\033[90m"
	ansiHideCur  = "\033[?25l"
	ansiShowCur  = "\033[?25h"
)

var (
	tsColorCode  = ansiDarkGray // konfigūruojama timestamp spalva
	hlBgCode     = ansiBgRed    // konfigūruojama highlight fono spalva
	nickColoring = true         // konfigūruojamas nick'ų spalvinimas
)

func setHighlightColor(name string) {
	switch strings.ToLower(name) {
	case "red":
		hlBgCode = ansiBgRed
	case "blue":
		hlBgCode = ansiBgBlue
	case "green":
		hlBgCode = "\033[42m"
	case "magenta":
		hlBgCode = "\033[45m"
	case "cyan":
		hlBgCode = "\033[46m"
	case "yellow":
		hlBgCode = "\033[43m"
	case "none":
		hlBgCode = ansiReverse // be fono — pažymejimas reverse video
	default:
		hlBgCode = ansiBgRed
	}
}

func setNickColors(b bool) { nickColoring = b }

func setTSColor(name string) {
	switch strings.ToLower(name) {
	case "blue":
		tsColorCode = ansiBlue
	case "green":
		tsColorCode = ansiGreen
	case "cyan":
		tsColorCode = ansiCyan
	case "none", "":
		tsColorCode = ""
	default: // "gray" arba neatpažinta
		tsColorCode = ansiDarkGray
	}
}

var nickColors = []string{
	"\033[31m", "\033[32m", "\033[33m", "\033[34m",
	"\033[35m", "\033[36m", "\033[91m", "\033[92m",
	"\033[93m", "\033[94m", "\033[95m", "\033[96m",
	"\033[38;5;208m", "\033[38;5;130m", "\033[38;5;46m",
	"\033[38;5;51m", "\033[38;5;201m", "\033[38;5;226m",
	"\033[38;5;39m", "\033[38;5;165m", "\033[38;5;82m",
	"\033[38;5;214m", "\033[38;5;105m", "\033[38;5;45m",
}

func nickColor(nick string) string {
	if !nickColoring {
		return ""
	}
	h := fnv.New32a()
	h.Write([]byte(strings.ToLower(nick)))
	return nickColors[h.Sum32()%uint32(len(nickColors))]
}

func moveTo(row, col int) string { return fmt.Sprintf("\033[%d;%dH", row, col) }

// ---- mIRC spalvų ir formatavimo kodai ----

// mircColors: mIRC spalvų numeriai → ANSI
var mircColors = [16]string{
	"\033[97m", // 0  white
	"\033[30m", // 1  black
	"\033[34m", // 2  blue
	"\033[32m", // 3  green
	"\033[91m", // 4  light red
	"\033[31m", // 5  brown/dark red
	"\033[35m", // 6  purple
	"\033[33m", // 7  orange
	"\033[93m", // 8  yellow
	"\033[92m", // 9  light green
	"\033[36m", // 10 teal
	"\033[96m", // 11 light cyan
	"\033[94m", // 12 light blue
	"\033[95m", // 13 pink
	"\033[90m", // 14 dark gray
	"\033[37m", // 15 light gray
}

// isControlRune nustato, ar simbolis yra terminalo valdymo kodas, kuris neturi
// patekti į ekraną. ESC (0x1b), C1 (0x80–0x9f, įskaitant vieno baito CSI 0x9b),
// DEL ir kiti C0 kodai gali valdyti terminalą (trinti ekraną, perkelti kursorių,
// keisti naršyklės pavadinimą per OSC, net rašyti į iškarpinę per OSC 52) —
// tinklo tekste juos visuomet pašaliname.
func isControlRune(r rune) bool {
	return (r >= 0x00 && r < 0x20 && r != '\t') || r == '\x7f' || (r >= 0x80 && r <= 0x9f)
}

// sanitizeText pašalina terminalo valdymo simbolius iš tinklo/naudotojo teksto
// (nickai, kanalų pavadinimai, temos, serverio žinutės, paieškos užklausa).
func sanitizeText(s string) string {
	if !strings.ContainsFunc(s, isControlRune) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if isControlRune(r) {
			return -1
		}
		return r
	}, s)
}

// renderIRCFormat konvertuoja mIRC formatavimą į ANSI
func renderIRCFormat(text string) string {
	var b strings.Builder
	runes := []rune(text)
	i := 0
	for i < len(runes) {
		r := runes[i]
		switch r {
		case '\x02': // bold
			b.WriteString(ansiBold)
			i++
		case '\x1d': // italic
			b.WriteString(ansiItalic)
			i++
		case '\x1f': // underline
			b.WriteString(ansiUnder)
			i++
		case '\x16': // reverse
			b.WriteString(ansiReverse)
			i++
		case '\x0f': // reset
			b.WriteString(ansiReset)
			i++
		case '\x03': // spalva
			i++
			fg, bg := -1, -1
			// Priekinė spalva (1-2 skaitmenys)
			if i < len(runes) && runes[i] >= '0' && runes[i] <= '9' {
				num := int(runes[i] - '0')
				i++
				if i < len(runes) && runes[i] >= '0' && runes[i] <= '9' {
					num = num*10 + int(runes[i]-'0')
					i++
				}
				if num < 16 {
					fg = num
				}
			}
			// Fono spalva
			if i < len(runes) && runes[i] == ',' {
				i++
				if i < len(runes) && runes[i] >= '0' && runes[i] <= '9' {
					num := int(runes[i] - '0')
					i++
					if i < len(runes) && runes[i] >= '0' && runes[i] <= '9' {
						num = num*10 + int(runes[i]-'0')
						i++
					}
					if num < 16 {
						bg = num
					}
				}
			}
			if fg >= 0 {
				b.WriteString(mircColors[fg])
			} else {
				b.WriteString(ansiReset) // \003 be spalvos = reset
			}
			_ = bg // fono spalva ignoruojama
		default:
			// Valdymo simboliai iš tinklo (pvz. ESC) — nespausdiname
			if isControlRune(r) {
				i++
				break
			}
			b.WriteRune(r)
			i++
		}
	}
	b.WriteString(ansiReset)
	return b.String()
}

// stripIRCFormat pašalina visus IRC formatavimo kodus (grynajam tekstui)
func stripIRCFormat(text string) string {
	var b strings.Builder
	runes := []rune(text)
	i := 0
	for i < len(runes) {
		r := runes[i]
		switch r {
		case '\x02', '\x1d', '\x1f', '\x16', '\x0f':
			i++
		case '\x03':
			i++
			// praleisti spalvos numerius
			for k := 0; k < 2 && i < len(runes) && runes[i] >= '0' && runes[i] <= '9'; k++ {
				i++
			}
			if i < len(runes) && runes[i] == ',' {
				i++
				for k := 0; k < 2 && i < len(runes) && runes[i] >= '0' && runes[i] <= '9'; k++ {
					i++
				}
			}
		default:
			b.WriteRune(r)
			i++
		}
	}
	return b.String()
}

// ---- Message ----

type MsgType int

const (
	MsgChat MsgType = iota
	MsgAction
	MsgJoin
	MsgPart
	MsgQuit
	MsgNick
	MsgNotice
	MsgServer
	MsgError
)

type Message struct {
	ts        time.Time
	nick      string
	text      string
	msgType   MsgType
	highlight bool
}

func wrapText(text string, width int) []string {
	if width <= 0 {
		width = 1
	}
	var lines []string
	runes := []rune(text)
	for len(runes) > width {
		cut := width
		for cut > width/2 && runes[cut-1] != ' ' {
			cut--
		}
		if cut <= width/2 {
			cut = width
		}
		lines = append(lines, string(runes[:cut]))
		runes = []rune(strings.TrimLeft(string(runes[cut:]), " "))
	}
	lines = append(lines, string(runes))
	return lines
}

func highlightURLs(text string) string {
	words := strings.Fields(text)
	result := text
	for _, w := range words {
		bare := strings.TrimRight(w, ".,!?)")
		if strings.HasPrefix(bare, "http://") || strings.HasPrefix(bare, "https://") || strings.HasPrefix(bare, "ftp://") {
			result = strings.Replace(result, bare, ansiCyan+ansiBold+bare+ansiReset, 1)
		}
	}
	return result
}

func (m Message) renderLines(msgW int, myNick string) []string {
	// Nuolatinės spalvos tekstui (nick/kanalas/serverio žinutės) — be terminalo kodų
	m.nick = sanitizeText(m.nick)
	// Chat/action/notice tekstas praeina per renderIRCFormat (strippina kodus);
	// kitų tipų tekstas špaudžiamas raw — nuvalom valdymo simbolius
	if m.msgType != MsgChat && m.msgType != MsgAction && m.msgType != MsgNotice {
		m.text = sanitizeText(m.text)
	}
	tsColor := tsColorCode
	var ts string
	if tsColor == "" {
		ts = m.ts.Format("15:04") + " "
	} else {
		ts = tsColor + m.ts.Format("15:04") + ansiReset + " "
	}
	tsLen := 6

	type part struct {
		prefix    string
		prefixLen int
	}
	var p part

	switch m.msgType {
	case MsgChat:
		nc := nickColor(m.nick)
		hl := hlBgCode
		nickStr := "<" + m.nick + "> "
		if m.highlight {
			p = part{hl + ansiBold + ts + nc + nickStr + ansiReset + hl, tsLen + len([]rune(nickStr))}
		} else {
			p = part{ts + nc + nickStr + ansiReset, tsLen + len([]rune(nickStr))}
		}
	case MsgAction:
		nc := nickColor(m.nick)
		pfx := "* " + m.nick + " "
		p = part{ts + nc + pfx, tsLen + len([]rune(pfx))}
	case MsgJoin:
		p = part{ts + ansiGreen, tsLen}
	case MsgPart:
		p = part{ts + ansiGreen, tsLen}
	case MsgQuit:
		p = part{ts + ansiGreen, tsLen}
	case MsgNick:
		p = part{ts + ansiGreen, tsLen}
	case MsgNotice:
		pfx := "-" + m.nick + "- "
		p = part{ts + ansiCyan + pfx, tsLen + len([]rune(pfx))}
	case MsgServer:
		p = part{ts + ansiCyan, tsLen}
	case MsgError:
		p = part{ts + ansiRed, tsLen}
	default:
		p = part{ts, tsLen}
	}

	// Gauti body tekstą pagal tipą
	var bodyPlain string
	switch m.msgType {
	case MsgChat, MsgAction, MsgNotice:
		bodyPlain = stripIRCFormat(m.text)
	case MsgJoin:
		bodyPlain = "--> " + m.nick + " prisijungė"
	case MsgPart:
		bodyPlain = "<-- " + m.nick + " išėjo (" + m.text + ")"
	case MsgQuit:
		bodyPlain = "<-- " + m.nick + " atsijungė (" + m.text + ")"
	case MsgNick:
		bodyPlain = "*** " + m.nick + " → " + m.text
	case MsgServer:
		bodyPlain = m.text
	case MsgError:
		bodyPlain = "KLAIDA: " + m.text
	default:
		bodyPlain = m.text
	}

	bodyWidth := msgW - p.prefixLen
	if bodyWidth < 8 {
		bodyWidth = 8
	}

	chunks := wrapText(bodyPlain, bodyWidth)
	indent := strings.Repeat(" ", p.prefixLen)

	lines := make([]string, 0, len(chunks))
	for i, chunk := range chunks {
		// Renderinti su IRC formatavimo kodais
		var rendered string
		switch m.msgType {
		case MsgChat, MsgAction, MsgNotice:
			rendered = highlightURLs(renderIRCFormat(findChunk(m.text, chunk)))
		default:
			rendered = highlightURLs(chunk)
		}
		suffix := ""
		if m.highlight {
			suffix = ansiReset
		}
		if i == 0 {
			lines = append(lines, p.prefix+rendered+suffix+ansiReset)
		} else {
			lines = append(lines, indent+rendered+ansiReset)
		}
	}
	return lines
}

// findChunk bando rasti atitinkamą dalį originaliame tekste (su formatavimo kodais)
// Jei nepavyksta - grąžina plain chunk
func findChunk(original, plainChunk string) string {
	plain := stripIRCFormat(original)
	idx := strings.Index(plain, strings.TrimSpace(plainChunk))
	if idx == -1 {
		return plainChunk
	}
	// Grąžiname originalą jei trumpas (viena eilutė)
	if len([]rune(plain)) == len([]rune(plainChunk)) {
		return original
	}
	return plainChunk
}

// ---- Channel ----

const defaultMaxMessages = 5000

// defaultPrefixSigns / defaultPrefixModes — standartinės IRC statuso
// prefiksų eilutės, kai serveris nepaskyrė savo PREFIX parametro (ISUPPORT).
const defaultPrefixSigns = "~&@%+"
const defaultPrefixModes = "qaohv"

type Channel struct {
	name          string
	messages      []Message
	unread        int
	highlight     int
	nicks         []string
	topic         string
	maxMessages   int
	scrollOff     int
	isPrivate     bool   // tiesioginis pokalbis (ne kanalas)
	drawLineCount int    // eilučių skaičius paskutiniam braižymui (istorijos užšaldymui)
	casemapping   string // serverio nick'ų palyginimo taisyklė (ISUPPORT)
	prefix        string // serverio PREFIX statuso prefiksai (ISUPPORT)
	prefixModes   string // serverio PREFIX mode raidės (ISUPPORT)

	// Renderio cache'as: eilutės priklauso nuo žinučių, pločio ir spalvos.
	// renderGen didinamas kiekvieną kartą pakeitus žinutes (addMsg, clear).
	renderGen    int
	renderedGen  int // <== renderGen kai cache galioja
	renderedW    int
	renderedNick string
	rendered     []string
}

func newChannel(name string, maxMsg int) *Channel {
	if maxMsg <= 0 {
		maxMsg = defaultMaxMessages
	}
	return &Channel{name: name, maxMessages: maxMsg, renderedGen: -1}
}

func (ch *Channel) addMsg(m Message) {
	ch.messages = append(ch.messages, m)
	ch.renderGen++
	// Apriboti žinučių skaičių
	if len(ch.messages) > ch.maxMessages {
		// Pašalinti seniausią ketvirtį
		trim := ch.maxMessages / 4
		ch.messages = ch.messages[trim:]
		ch.renderGen++
	}
}

// addNick prideda nick'ą į kanalo sąrašą. sign/modes — serverio PREFIX
// parametrai (iš ISUPPORT), kad sortavimas ir lygiavimas būtų nuoseklūs.
// prefixSigns grąžina kanalui priskirtą PREFIX parametrą arba standartinį.
func (ch *Channel) prefixSigns(opt ...string) string {
	if len(opt) > 0 {
		return opt[0]
	}
	if ch != nil && ch.prefix != "" {
		return ch.prefix
	}
	return defaultPrefixSigns
}

func (ch *Channel) addNick(nick string, signOpt ...string) {
	sign := ch.prefixSigns(signOpt...)
	for _, n := range ch.nicks {
		if nickEqual(stripMode(n, sign), stripMode(nick, sign), ch.casemapping) {
			return
		}
	}
	ch.nicks = append(ch.nicks, nick)
	sortNicks(ch.nicks, sign)
}

func (ch *Channel) removeNick(nick string, signOpt ...string) {
	sign := ch.prefixSigns(signOpt...)
	for i, n := range ch.nicks {
		if nickEqual(stripMode(n, sign), stripMode(nick, sign), ch.casemapping) {
			ch.nicks = append(ch.nicks[:i], ch.nicks[i+1:]...)
			return
		}
	}
}

func (ch *Channel) renameNick(oldNick, newNick string, signOpt ...string) {
	sign := ch.prefixSigns(signOpt...)
	for i, n := range ch.nicks {
		if nickEqual(stripMode(n, sign), oldNick, ch.casemapping) {
			ch.nicks[i] = modePrefix(n, sign) + newNick
			sortNicks(ch.nicks, sign)
			return
		}
	}
}

func (ch *Channel) setMode(nick, mode string, signsOpt ...string) {
	signs := ch.prefixSigns()
	modes := ch.prefixModes
	if modes == "" {
		modes = defaultPrefixModes
	}
	if len(signsOpt) == 2 {
		signs, modes = signsOpt[0], signsOpt[1]
	} else if len(signsOpt) == 1 {
		signs = signsOpt[0]
	}
	adding := true
	for _, mr := range mode {
		switch mr {
		case '+':
			adding = true
		case '-':
			adding = false
		default:
			// mode raidė → statuso prefiksas pagal serverio PREFIX parametrą
			if i := strings.IndexRune(modes, mr); i >= 0 && i < len(signs) {
				ch.changeNickMode(nick, signs[i], adding, signs)
			}
		}
	}
}

func (ch *Channel) changeNickMode(nick string, prefix byte, adding bool, signs string) {
	for i, n := range ch.nicks {
		if nickEqual(stripMode(n, signs), nick, ch.casemapping) {
			bare := stripMode(n, signs)
			if adding {
				ch.nicks[i] = string(prefix) + bare
			} else {
				ch.nicks[i] = bare
			}
			sortNicks(ch.nicks, signs)
			return
		}
	}
}

// stripMode/prefixStatus naudoja serverio parametrus (PREFIX iš ISUPPORT),
// o ne hardkoduotą "~&@%+" sąrašą.
// stripMode pašalina statuso prefiksą. signs — serverio PREFIX prefiksai;
// jei nepaduodami, naudojami standartiniai („~&@%+“).
func stripMode(nick string, signsOpt ...string) string {
	signs := defaultPrefixSigns
	if len(signsOpt) > 0 {
		signs = signsOpt[0]
	}
	if len(nick) > 0 && strings.ContainsRune(signs, rune(nick[0])) {
		return nick[1:]
	}
	return nick
}

// modePrefix grąžina nick'o statuso prefiksą (arba "").
func modePrefix(nick string, signsOpt ...string) string {
	signs := defaultPrefixSigns
	if len(signsOpt) > 0 {
		signs = signsOpt[0]
	}
	if len(nick) > 0 && strings.ContainsRune(signs, rune(nick[0])) {
		return string(nick[0])
	}
	return ""
}

func nickOrder(n string, signsOpt ...string) int {
	signs := defaultPrefixSigns
	if len(signsOpt) > 0 {
		signs = signsOpt[0]
	}
	if len(n) == 0 {
		return len(signs)
	}
	if i := strings.IndexRune(signs, rune(n[0])); i >= 0 {
		return i
	}
	return len(signs)
}

func sortNicks(nicks []string, signsOpt ...string) {
	// Surūšiuoti pagal prefix'ų hierarchiją — signs imamas vieną kartą
	// iš kvietėjo, kad visi nicks būtų surūšiuoti pagal tą patį PREFIX parametrą.
	signs := defaultPrefixSigns
	if len(signsOpt) > 0 {
		signs = signsOpt[0]
	}
	order := func(n string) int {
		if len(n) == 0 {
			return len(signs)
		}
		if i := strings.IndexRune(signs, rune(n[0])); i >= 0 {
			return i
		}
		return len(signs)
	}
	strip := func(n string) string {
		if len(n) > 0 && strings.ContainsRune(signs, rune(n[0])) {
			return n[1:]
		}
		return n
	}
	sort.Slice(nicks, func(i, j int) bool {
		oi, oj := order(nicks[i]), order(nicks[j])
		if oi != oj {
			return oi < oj
		}
		return strings.ToLower(strip(nicks[i])) < strings.ToLower(strip(nicks[j]))
	})
}

// ---- UI ----

const chanListWidth = 18
const nickListWidth = 16

func (c *Client) draw() {
	c.mu.Lock()
	w, h := c.width, c.height
	if w < 40 || h < 8 {
		c.mu.Unlock()
		return
	}
	active := c.activeChan()
	if active == nil {
		// Aktyvaus kanalo nėra (activeIdx negaliojantis) — nebraižyk
		c.mu.Unlock()
		return
	}
	channels := make([]*Channel, len(c.channels))
	copy(channels, c.channels)
	activeIdx := c.activeIdx
	nicks := make([]string, len(active.nicks))
	copy(nicks, active.nicks)
	topic := active.topic
	inputRunes := make([]rune, len(c.input))
	copy(inputRunes, c.input)
	inputPos := c.inputPos
	scrollOff := c.scrollOff
	nickScrollOff := c.nickScrollOff
	connected := c.connected
	myNick := c.nick
	server := c.server
	away := c.away
	useTLS := c.useTLS
	searchMode := c.searchMode
	searchQuery := c.searchQuery
	c.mu.Unlock()

	showNicks := c.isChannel(active.name)
	out := &strings.Builder{}
	out.WriteString(ansiHideCur)

	row := 1

	// --- Statusas ---
	out.WriteString(moveTo(row, 1))
	out.WriteString(fmt.Sprintf("\033[%dX", w))
	out.WriteString(moveTo(row, 1))
	out.WriteString(ansiBgBlue + ansiBold)
	tlsMark := ""
	if useTLS {
		tlsMark = "[TLS] "
	}
	awayMark := ""
	if away != "" {
		awayMark = " [AWAY]"
	}
	reconnMark := ""
	if !connected {
		reconnMark = " [RECONNECTING...]"
	}
	statusLeft := fmt.Sprintf(" mircBSD %s| %s%s%s @ %s | %s ",
		tlsMark, myNick, awayMark, reconnMark, server, active.name)
	if !connected && server == "" {
		statusLeft = " mircBSD | [NEPRISIJUNGTA] "
	}
	statusRight := " PgUp/Dn:scroll Ctrl+N/P:kan Ctrl+←→:žodis Tab:AC /help "
	statusLeft = sanitizeText(statusLeft)
	pad := w - utf8.RuneCountInString(statusLeft) - utf8.RuneCountInString(statusRight)
	if pad < 0 {
		pad = 0
	}
	out.WriteString(statusLeft + strings.Repeat(" ", pad) + statusRight + ansiReset)
	row++

	// --- Temos juosta ---
	if topic != "" {
		out.WriteString(moveTo(row, 1))
		out.WriteString(fmt.Sprintf("\033[%dX", w))
		out.WriteString(moveTo(row, 1))
		out.WriteString(ansiDarkGray + " ✦ " + ansiReset)
		topicPlain := sanitizeText(stripIRCFormat(topic))
		maxTopic := w - 4
		if len([]rune(topicPlain)) > maxTopic {
			topicPlain = string([]rune(topicPlain)[:maxTopic-1]) + "…"
		}
		out.WriteString(ansiCyan + topicPlain + ansiReset)
		row++
	}

	// --- Paieškos juosta ---
	if searchMode {
		out.WriteString(moveTo(row, 1))
		out.WriteString(fmt.Sprintf("\033[%dX", w))
		out.WriteString(moveTo(row, 1))
		out.WriteString(ansiBgBlue + " Paieška: " + sanitizeText(searchQuery) + " (Enter=ieškoti, Enter iš naujo=atgal, Esc=išeiti) " + ansiReset)
		row++
	}

	msgAreaTop := row
	c.mu.Lock()
	c.msgAreaTop = msgAreaTop
	c.mu.Unlock()
	msgAreaBot := h - 2
	msgRows := msgAreaBot - msgAreaTop + 1
	if msgRows < 1 {
		msgRows = 1
	}

	// --- Kanalų sąrašas ---
	for i := 0; i < msgRows; i++ {
		r := msgAreaTop + i
		out.WriteString(moveTo(r, 1))
		out.WriteString(fmt.Sprintf("\033[%dX", chanListWidth))
		out.WriteString(moveTo(r, 1))
		if i < len(channels) {
			ch := channels[i]
			name := ch.name
			if i == activeIdx {
				out.WriteString(ansiBgBlue + ansiBold + ansiWhite)
			} else if ch.highlight > 0 {
				out.WriteString(ansiRed + ansiBold)
			} else if ch.unread > 0 {
				out.WriteString(ansiYellow + ansiBold)
			} else if ch.isPrivate {
				out.WriteString(ansiMagenta)
			} else {
				out.WriteString(ansiDarkGray)
			}
			displayName := sanitizeText(name)
			if ch.isPrivate {
				// Privatūs kanalai pažymėti „>“ — pavadinimas taip pat
				// turi būti sanitizuotas (priešiškas serveris gali atsiųsti
				// PRIVMSG su ESC simboliais nick'e).
				displayName = ">" + displayName
			}
			fkeyPrefix := ""
			if i < 12 {
				fkeyPrefix = fmt.Sprintf("%d:", i+1)
			}
			maxNameW := chanListWidth - 2 - len(fkeyPrefix)
			if len([]rune(displayName)) > maxNameW {
				displayName = string([]rune(displayName)[:maxNameW])
			}
			label := fmt.Sprintf(" %s%-*s", fkeyPrefix, maxNameW, displayName)
			if ch.unread > 0 && i != activeIdx {
				unreadW := maxNameW - 3
				if unreadW < 1 {
					unreadW = 1
				}
				if ch.highlight > 0 {
					label = fmt.Sprintf(" %s%-*s!%d", fkeyPrefix, unreadW, displayName, ch.highlight)
				} else {
					label = fmt.Sprintf(" %s%-*s%d", fkeyPrefix, unreadW, displayName, ch.unread)
				}
			}
			out.WriteString(label + ansiReset)
		}
		out.WriteString(moveTo(r, chanListWidth+1))
		out.WriteString(ansiDarkGray + "│" + ansiReset)
	}

	// --- Žinutės ---
	nickPanelW := 0
	if showNicks {
		nickPanelW = nickListWidth + 1
	}
	msgW := w - chanListWidth - 2 - nickPanelW
	if msgW < 10 {
		msgW = 10
	}

	// Eilučių cache'as: nerenderiojam žinučių iš naujo kiekvienam klavišui,
	// jei žinutės ir plotis nepasikeitę (greitis ant užimtų kanalų)
	c.mu.Lock()
	var lines []string
	gen := active.renderGen
	if active.renderedGen == gen && active.renderedW == msgW && active.renderedNick == myNick {
		lines = active.rendered
		c.mu.Unlock()
	} else {
		msgs := make([]Message, len(active.messages))
		copy(msgs, active.messages)
		c.mu.Unlock()

		lines = make([]string, 0, len(msgs)+len(msgs))
		for _, m := range msgs {
			lines = append(lines, m.renderLines(msgW, myNick)...)
		}

		// Cache'uoti tik jei žinutės nepakitę renderio metu
		c.mu.Lock()
		if active.renderGen == gen {
			active.rendered = lines
			active.renderedGen = gen
			active.renderedW = msgW
			active.renderedNick = myNick
		}
		c.mu.Unlock()
	}

	totalLines := len(lines)

	// Istorijos „užšaldymas": būnant atslinkus aukštyn (scrollOff > 0) ir priėjus
	// naujų žinučių, scrollOff pagrįstas nuo apačios — matomas tekstas slystų
	// aukštyn. Prieinam prie slinkties pagal naujų eilučių skaičių, kad vaizdas
	// liktų stabilūs (ankščiausias viršus lieka viršuje).
	c.mu.Lock()
	wasDrawn := c.lastDrawn == active
	c.lastDrawn = active
	prevCount := active.drawLineCount
	if !wasDrawn || prevCount > totalLines {
		prevCount = totalLines
	}
	active.drawLineCount = totalLines
	// Jei naudotojas ką tik slinko žemyn (chaseUntil galioja), neužšaldom:
	// kitaip atėję nauji eilučių kiekis grąžų į apačią padaro neįmanoma —
	// kiekviena žinutė padidintų scrollOff tiek pat, kiek slinktis nuskauso.
	chase := time.Now().Before(c.chaseUntil)
	if scrollOff > 0 && wasDrawn && totalLines > prevCount && !chase {
		scrollOff += totalLines - prevCount
		c.scrollOff = scrollOff
	}
	c.mu.Unlock()

	startLine := totalLines - msgRows - scrollOff
	if startLine < 0 {
		startLine = 0
	}

	msgColStart := chanListWidth + 2
	msgColEnd := w - nickPanelW
	for i := 0; i < msgRows; i++ {
		r := msgAreaTop + i
		out.WriteString(moveTo(r, msgColStart))
		out.WriteString(fmt.Sprintf("\033[%dX", msgColEnd-msgColStart))
		out.WriteString(moveTo(r, msgColStart))
		lineIdx := startLine + i
		if lineIdx >= 0 && lineIdx < len(lines) {
			out.WriteString(lines[lineIdx])
		}
	}

	if scrollOff > 0 {
		banner := fmt.Sprintf(" ↑ SCROLL (+%d eilučių žemiau) ─ PgDn/↓ žemyn, Shift+PgDn apačion ", scrollOff)
		if runes := []rune(banner); len(runes) > msgW {
			banner = string(runes[:msgW])
		}
		out.WriteString(moveTo(msgAreaTop, msgColStart))
		out.WriteString(ansiYellow + ansiBold + banner + ansiReset)
	}

	// --- Nick sąrašas ---
	if showNicks {
		nickCol := w - nickListWidth
		visibleNicks := msgRows - 1
		maxScroll := len(nicks) - visibleNicks
		if maxScroll < 0 {
			maxScroll = 0
		}
		if nickScrollOff > maxScroll {
			nickScrollOff = maxScroll
		}
		for i := 0; i < msgRows; i++ {
			r := msgAreaTop + i
			out.WriteString(moveTo(r, nickCol-1))
			out.WriteString(ansiDarkGray + "│" + ansiReset)
			out.WriteString(moveTo(r, nickCol))
			out.WriteString(fmt.Sprintf("\033[%dX", nickListWidth))
			out.WriteString(moveTo(r, nickCol))
			if i == 0 {
				hint := ""
				if len(nicks) > visibleNicks {
					shown := nickScrollOff + visibleNicks
					if shown > len(nicks) {
						shown = len(nicks)
					}
					hint = fmt.Sprintf("(%d/%d)", shown, len(nicks))
				}
				out.WriteString(ansiBold + ansiCyan + fmt.Sprintf("%-*s", nickListWidth, " Nick"+hint) + ansiReset)
			} else {
				ni := nickScrollOff + i - 1
				if ni < len(nicks) {
					n := sanitizeText(nicks[ni])
					display := n
					if len([]rune(display)) > nickListWidth {
						display = string([]rune(display)[:nickListWidth])
					}
					color := ansiWhite
					switch modePrefix(n, active.prefixSigns()) {
					case "~", "&", "@":
						color = ansiYellow
					case "%":
						color = ansiGreen
					case "+":
						color = ansiCyan
					}
					out.WriteString(color + fmt.Sprintf("%-*s", nickListWidth, display) + ansiReset)
				}
			}
		}
		if len(nicks) > visibleNicks {
			if nickScrollOff > 0 {
				out.WriteString(moveTo(msgAreaTop, nickCol+nickListWidth-1))
				out.WriteString(ansiYellow + "▲" + ansiReset)
			}
			if nickScrollOff < maxScroll {
				out.WriteString(moveTo(msgAreaBot, nickCol+nickListWidth-1))
				out.WriteString(ansiYellow + "▼" + ansiReset)
			}
		}
	}

	// --- Skiriamoji linija ---
	out.WriteString(moveTo(h-1, 1))
	out.WriteString(ansiDarkGray + strings.Repeat("─", w) + ansiReset)

	// --- Įvedimo eilutė ---
	out.WriteString(moveTo(h, 1))
	out.WriteString(fmt.Sprintf("\033[%dX", w))
	out.WriteString(moveTo(h, 1))
	awayPfx := ""
	if away != "" {
		awayPfx = ansiYellow + "[AWAY] " + ansiReset
	}
	prompt := ansiGreen + "> " + ansiReset
	out.WriteString(awayPfx + prompt)
	prefixLen := 2
	if away != "" {
		prefixLen += 7
	}
	dispRunes := []rune(string(inputRunes))
	maxInput := w - prefixLen - 1
	dispStart := 0
	if len(dispRunes) > maxInput {
		dispStart = len(dispRunes) - maxInput
	}
	out.WriteString(string(dispRunes[dispStart:]))

	cursorCol := prefixLen + 1 + (inputPos - dispStart)
	if cursorCol < 1 {
		cursorCol = 1
	}
	if cursorCol > w {
		cursorCol = w
	}
	out.WriteString(moveTo(h, cursorCol))
	out.WriteString(ansiShowCur)
	fmt.Print(out.String())
}
