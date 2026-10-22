package main

import (
	"encoding/json"
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

// tsColorName / hlColorName — paskutinės sukonfigūruotos SPALVŲ VARDOS
// (ne kodai): /set sąrašui, kuris turi rodyti tą raktažodį, kurį vartotojas
// įrašė („gray“), o ne iš jo gautą ANSI kodą.
var (
	tsColorName = "gray"
	hlColorName = "red"
)

func setHighlightColor(name string) {
	hlColorName = strings.ToLower(name)
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
		hlColorName = "red"
		hlBgCode = ansiBgRed
	}
}

func setNickColors(b bool) { nickColoring = b }

// timestamp config — globaliai (kaip tsColorCode): įrašoma vieną kartą
// paleidimo metu, tik skaitoma renderLines (Message metodas neturi prieigos
// prie Client). Vienagijos prieiga — tik iš braižymo gorutinų.
var (
	timestampsEnabled = true
	tsFormat          = "15:04"
)

func setTimestamps(enabled bool, format string) {
	timestampsEnabled = enabled
	if format != "" {
		tsFormat = format
	}
}

func setTSColor(name string) {
	tsColorName = strings.ToLower(name)
	switch strings.ToLower(name) {
	case "blue":
		tsColorCode = ansiBlue
	case "green":
		tsColorCode = ansiGreen
	case "cyan":
		tsColorCode = ansiCyan
	case "none", "":
		tsColorName = "none"
		tsColorCode = ""
	default: // "gray" arba neatpažinta
		tsColorName = "gray"
		tsColorCode = ansiDarkGray
	}
}

// nickColorsDark — numatytoji (tamsaus fono) paletė.
var nickColorsDark = []string{
	"\033[31m", "\033[32m", "\033[33m", "\033[34m",
	"\033[35m", "\033[36m", "\033[91m", "\033[92m",
	"\033[93m", "\033[94m", "\033[95m", "\033[96m",
	"\033[38;5;208m", "\033[38;5;130m", "\033[38;5;46m",
	"\033[38;5;51m", "\033[38;5;201m", "\033[38;5;226m",
	"\033[38;5;39m", "\033[38;5;165m", "\033[38;5;82m",
	"\033[38;5;214m", "\033[38;5;105m", "\033[38;5;45m",
}

// nickColorsLight — šviesaus fono tema (theme=light, v0.4.8):
// šviesios spalvos (91–96, 226 ir pan.) BALTAME fone neišsiskiria —
// paletė pakečiama TAMSOMIS (16/256-color) spalvomis.
var nickColorsLight = []string{
	"\033[38;5;124m", "\033[38;5;28m", "\033[38;5;94m", "\033[38;5;26m",
	"\033[38;5;126m", "\033[38;5;30m", "\033[38;5;88m", "\033[38;5;22m",
	"\033[38;5;97m", "\033[38;5;18m", "\033[38;5;61m", "\033[38;5;52m",
	"\033[38;5;131m", "\033[38;5;59m", "\033[38;5;23m", "\033[38;5;132m",
	"\033[38;5;127m", "\033[38;5;67m", "\033[38;5;101m", "\033[38;5;63m",
	"\033[38;5;133m", "\033[38;5;24m", "\033[38;5;95m", "\033[38;5;62m",
}

// nickColors — aktyvi paletė (keičiama setTheme).
var nickColors = nickColorsDark

// themeDarkMode — tema (dark — numatyta; light — šviesi).
var themeDarkMode = true

// setTheme — tema (v0.4.8): dark (numatyta) arba light. Light theme
// pakeičia nick paletes į tamsias (šviesios nieks nematoma balta fone).
func setTheme(name string) {
	switch strings.ToLower(name) {
	case "light":
		themeDarkMode = false
		nickColors = nickColorsLight
	default: // "dark" arba neatpažinta
		themeDarkMode = true
		nickColors = nickColorsDark
	}
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

// String — JSON eksportui (/exportjson): žmogui skaitomi tipai.
func (t MsgType) String() string {
	switch t {
	case MsgChat:
		return "chat"
	case MsgAction:
		return "action"
	case MsgJoin:
		return "join"
	case MsgPart:
		return "part"
	case MsgQuit:
		return "quit"
	case MsgNick:
		return "nick"
	case MsgNotice:
		return "notice"
	case MsgServer:
		return "server"
	}
	return "error"
}

type Message struct {
	ts        time.Time
	nick      string
	text      string
	msgType   MsgType
	highlight bool
}

// MarshalJSON — /exportjson (v0.4.8): neskelbtiniems Message laukams.
// ts — RFC3339; text — SANITIZUOTAS (be IRC formatavimo kodų, be terminalo
// valdiklių): failas, atidarytas kitur, neskleidžia valdyti terminalo.
func (m Message) MarshalJSON() ([]byte, error) {
	type wire struct {
		Ts        string `json:"ts"`
		Nick      string `json:"nick,omitempty"`
		Type      string `json:"type"`
		Text      string `json:"text,omitempty"`
		Highlight bool   `json:"highlight,omitempty"`
	}
	return json.Marshal(wire{
		Ts:        m.ts.UTC().Format(time.RFC3339),
		Nick:      sanitizeText(m.nick),
		Type:      m.msgType.String(),
		Text:      sanitizeText(stripIRCFormat(m.text)),
		Highlight: m.highlight,
	})
}

// wrapText apvynio tekstą pagal plotį LĄSTELĖMIS (ne rune'ais).
//
// CJK ideogramai užima 2 ląsteles, o derinamieji ženklai (ą, č, emoji odų
// tonai) — 0. Rune'ų skaičiavimas todėl perlydavo terminalo plotį.
// Tekstas taip pat skaidomas į grafinius klasterius, kad akcentuota raidė arba
// emoji nebūtų nutrūkta perpusę.
func wrapText(text string, width int) []string {
	if width <= 0 {
		width = 1
	}
	clusters := runeClusters(text)
	if len(clusters) == 0 {
		return []string{""}
	}
	var lines []string
	cur := make([]string, 0, width)
	curW := 0
	for _, cl := range clusters {
		cw := clusterWidth(cl)
		if curW+cw > width {
			// Eilutė pilna — ieškome pertraukimo vietos (tarpai).
			// Žodis per ilgas (neteliko nė vienos eilutės) — nukerpame
			// paskutinį klasterį.
			cut := -1
			for i := len(cur) - 1; i >= 0; i-- {
				if cur[i] == " " {
					cut = i
					break
				}
			}
			if cut < 0 {
				// Žodis neteliko — kirsti pagal plotį.
				if len(cur) == 0 {
					// Vienas klasteris (pvz. 2 ląstelės) viršija plotį —
					// vis tiek jį išspausdiname, kad tekstas nebūtų
					// prarastas.
					cur = append(cur, cl)
					curW = cw
					lines = append(lines, strings.Join(cur, ""))
					cur = cur[:0]
					curW = 0
					continue
				}
				lines = append(lines, strings.Join(cur, ""))
				cur = cur[:0]
				curW = 0
				// Pridedame klasterį į naują eilutę.
				cur = append(cur, cl)
				curW = cw
				continue
			}
			// Perskirsti iki paskutinio tarpo: turinys po tarpu — kita
			// eilutė.
			lines = append(lines, strings.Join(cur[:cut], ""))
			rest := cur[cut+1:]
			cur = append(cur[:0], rest...)
			curW = 0
			for _, r := range cur {
				curW += clusterWidth(r)
			}
			cur = append(cur, cl)
			curW += cw
			continue
		}
		cur = append(cur, cl)
		curW += cw
	}
	if len(cur) > 0 || len(lines) == 0 {
		lines = append(lines, strings.Join(cur, ""))
	}
	return lines
}

// highlightURLs apvynioja URL'us ANSI spalva.
//
// Anksčiau naudota `strings.Fields` + `strings.Replace(..., 1)`:
//   - tekstas jau būdavo su ANSI kodais, todėl spalvotame tekste URL niekada
//     neatpažįstamas (kodai pačioje URL pradžioje);
//   - antrą kartą aptinkamas tas pats URL (jau apvyniotas) → įdubiniai
//     ANSI kodai, o antrasis URL likdavo neapvyniotas.
//
// Dabar tekstas skenuojamas simbolį po simbolį: ANSI seka praleidžiama,
// o kiekvienas URL apvyniojamas lygiai VIENĄ kartą.
func highlightURLs(text string) string {
	var b strings.Builder
	b.Grow(len(text) + 32)
	i := 0
	for i < len(text) {
		// ANSI pakeitimo seka (ESC [ ... m) — praleidžiama, nekeičiama.
		if text[i] == 0x1b {
			j := skipANSISeq(text, i)
			b.WriteString(text[i:j])
			i = j
			continue
		}
		// URL?
		if hasURLPrefix(text[i:]) {
			j := i
			for j < len(text) && !isURLBoundary(text[j]) {
				j++
			}
			core := strings.TrimRight(text[i:j], ".,!?)")
			if len(core) > 0 {
				b.WriteString(ansiCyan + ansiBold + core + ansiReset)
				// Uždarymo punctacija — neapvyniota.
				b.WriteString(text[i+len(core) : j])
				i = j
				continue
			}
		}
		b.WriteByte(text[i])
		i++
	}
	return b.String()
}

// hasURLPrefix tikrina, ar tekstas prasideda URL schema.
func hasURLPrefix(s string) bool {
	return strings.HasPrefix(s, "http://") ||
		strings.HasPrefix(s, "https://") ||
		strings.HasPrefix(s, "ftp://")
}

// isURLBoundary tikrina, ar simbolis baigia URL'ą.
func isURLBoundary(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '<', '>', '"', '\'', 0x1b:
		return true
	}
	return false
}

// skipANSISeq grąžina indeksą už ANSI pakeitimo sekos (ESC [ ... m).
func skipANSISeq(s string, i int) int {
	j := i + 1
	if j < len(s) && s[j] == '[' {
		j++
		for j < len(s) && !isANSITerminator(s[j]) {
			j++
		}
		if j < len(s) {
			j++
		}
		return j
	}
	// Ne CSI seka — 2 simboliai.
	return i + 2
}

// isANSITerminator tikrina, ar simbolis baigia ANSI seką.
func isANSITerminator(c byte) bool {
	return c >= 0x40 && c <= 0x7e
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
	tsLen := 0
	// Laiko žyma — konfigūruojama (timestamps=true|false, ts_format).
	// Kai išjungta — be laikos: prefixLen mažesnis ir tekstas platesnis.
	if timestampsEnabled {
		formatted := m.ts.Format(tsFormat)
		tsLen = stringWidth(formatted) + 1
		if tsColor == "" {
			ts = formatted + " "
		} else {
			ts = tsColor + formatted + ansiReset + " "
		}
	}

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
			p = part{hl + ansiBold + ts + nc + nickStr + ansiReset + hl, tsLen + stringWidth(nickStr)}
		} else {
			p = part{ts + nc + nickStr + ansiReset, tsLen + stringWidth(nickStr)}
		}
	case MsgAction:
		nc := nickColor(m.nick)
		pfx := "* " + m.nick + " "
		p = part{ts + nc + pfx, tsLen + stringWidth(pfx)}
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
		p = part{ts + ansiCyan + pfx, tsLen + stringWidth(pfx)}
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
		bodyPlain = "--> " + m.nick + " prisijungė " + m.text
		bodyPlain = strings.TrimSpace(bodyPlain)
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
// findChunk bando rasti atitinkamą dalį originaliame tekste (su
// formatavimo kodais).
//
// Anksčiau ieškojo PLAIN tekste ir grąžindavo plainChunk (be formatavimo
// kodų) — todėl spalvos ir paryškiavimas išgyveno TIK vienos eilutės
// žinutėse. Dabar ieškoma originaliame tekste, ieškant ribų pagal plain
// indeksą, todėl formatavimo kodai išsaugomi.
//
// Jei eilutė yra trumpesnė nei plotis (viena eilutė) — grąžinamas visas
// originalas.
func findChunk(original, plainChunk string) string {
	plain := stripIRCFormat(original)
	trimmed := strings.TrimSpace(plainChunk)
	if trimmed == "" {
		return plainChunk
	}
	// Būtina visas originalo tekstas (viena eilutė) — grąžiname ją.
	if len([]rune(plain)) == len([]rune(trimmed)) {
		return original
	}
	// Ieškome PLAIN tekste ribų, tada atsižvelgiame į formatavimo kodus.
	plainIdx := strings.Index(plain, trimmed)
	if plainIdx == -1 {
		return plainChunk
	}
	// strings.Index grąžina BAITŲ indeksą, o plainToOriginalBounds jį lygina
	// su RUNE skaitikliu (plainSeen++ per raidę) — su ne-ASCII tekstu
	// (lietuviškos raidės, emoji) baitų indeksas „per didelis“ ir apvyniotos
	// eilutės riba nuslysta į kitas raides („begalingai“ → „galingai š“).
	// Konvertuojame baitų indeksą į runę.
	start, end := plainToOriginalBounds(original, utf8.RuneCountInString(plain[:plainIdx]), utf8.RuneCountInString(trimmed))
	if start < 0 || end < 0 {
		return plainChunk
	}
	return original[start:end]
}

// plainToOriginalBounds konvertuoja indeksus PLAIN tekste į indeksus
// originaliame tekste (su IRC formatavimo kodais).
//
// plainIdx — simbolio indeksas be formatavimo kodų;
// plainLen — simbolių skaičius.
//
// Grąžina [start, end) baitų indeksus originaliame tekste.
func plainToOriginalBounds(original string, plainIdx, plainLen int) (int, int) {
	runes := []rune(original)
	plainSeen := 0
	startByte := -1
	endByte := -1
	bytePos := 0
	// inColor — esame spalvos kodo parametruose (skaičiai po \x03).
	inColor := false
	// pendingCodeByte — baitų pozicija, kurioje prasidėjo formatavimo kodas.
	// Jei plain turis prasideda tiesiog po kodo, riba turi būti PRADEDAMA
	// nuo kodo pradžios (kitaip spalvos kodas atitrūksta nuo savo teksto).
	codeStart := -1
	// lastCodeEnd — baitų pozicija, kur baigėsi paskutinis formatavimo kodas.
	lastCodeEnd := -1

	for _, r := range runes {
		rlen := len(string(r))
		isCode := false
		switch r {
		case '\x02', '\x1d', '\x1f', '\x16', '\x0f':
			isCode = true
			inColor = false
		case '\x03':
			isCode = true
			inColor = true
		default:
			if inColor {
				if (r >= '0' && r <= '9') || r == ',' {
					// spalvos parametro dalis — kodas tęsiasi.
					isCode = true
				} else {
					inColor = false
				}
			}
		}

		if isCode {
			if codeStart < 0 {
				codeStart = bytePos
			}
			bytePos += rlen
			lastCodeEnd = bytePos
			continue
		}

		// Įprastas simbolis — skaičiuojamas į plain tekstą.
		if plainSeen == plainIdx && startByte < 0 {
			if codeStart >= 0 && lastCodeEnd == bytePos {
				// Plain turis prasideda po formatavimo kodo — įtraukiame
				// kodą į gabalą, kad spalva liktų su savo tekstu.
				startByte = codeStart
			} else {
				startByte = bytePos
			}
		}
		if plainSeen == plainIdx+plainLen {
			endByte = bytePos
			return startByte, endByte
		}
		plainSeen++
		bytePos += rlen
		lastCodeEnd = -1
		codeStart = -1
	}
	if startByte >= 0 && endByte < 0 {
		endByte = len(original)
	}
	return startByte, endByte
}

// ---- Channel ----

const defaultMaxMessages = 5000

// defaultPrefixSigns / defaultPrefixModes — standartinės IRC statuso
// prefiksų eilutės, kai serveris nepaskyrė savo PREFIX parametro (ISUPPORT).
const defaultPrefixSigns = "~&@%+"
const defaultPrefixModes = "qaohv"

type Channel struct {
	name     string
	messages []Message
	unread   int
	// unreadFrom — pirmosios NEPERSKAITYTOS žinutės indeksas (imamas
	// kai unread 0→1, addMsg). Grįžus į kanalą switchCh prieš ją
	// įstatome „── naujos žinutės ──“ žymą (v0.4.7). -1 = neįrašytas.
	unreadFrom    int
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

	// nickSet — rinkinys normalizuotų nick'ų (be prefikso, su
	// casemapping'u) — greitas „ar toks nick'as yra?“ patikrinimas.
	//
	// KODĖL: be jo kiekvienas nick'o įterpimas buvo O(n) (stripMode +
	// nickEqual per elementą). 5 000 nick'ų kanale tai 5 000 × 5 000
	// = 25 milijonų operacijų — O(n²) DoS (užblokavimas).
	//
	// KODĖL NE map[string]int (su indeksais): po kiekvieno įterpimo
	// VISI indeksai po tašku pasikeičia → žemėlapio atstatymas O(n) →
	// vėl O(n²) visas. Rinkinys (be indeksų) yra stabilus per
	// įterpimus: jį reikia atnaujinti tik PAŠALINUS arba pervadinus
	// nick'ą (reti įvykiai).
	//
	// INVARIANTAS: galioja TIK SU c.mu (arba c.mu užrakintu).
	// Peržiūrėtoji vieta — kiekvienas ch.nicks pakeitimas (addNick,
	// removeNick, renameNick, changeNickMode, /names → nicks = nil).
	nickSet map[string]struct{}

	// Renderio cache'as: eilutės priklauso nuo žinučių, pločio ir spalvos.
	// renderGen didinamas kiekvieną kartą pakeitus žinutes (addMsg, clear).
	//
	// renderedMsgCount — kiek žinučių jau IŠRENDERIUOTA cache'e. Kai
	// pridedama nauja žinutė, senos eilutės lieka galiojančios — tik
	// naujos pridedamos (inkrementalus renderiavimas). Anksčiau kiekviena
	// nauja žinutė nugriaždindavo VISĄ cache'ą (5000 žinučių → 5000
	// renderLines() kiekviename braižyme).
	renderGen        int
	renderedGen      int // <== renderGen kai cache galioja
	renderedW        int
	renderedNick     string
	rendered         []string
	renderedMsgCount int // kiek žinučių cache'e apskaičiuota
}

func newChannel(name string, maxMsg int) *Channel {
	if maxMsg <= 0 {
		maxMsg = defaultMaxMessages
	}
	return &Channel{name: name, maxMessages: maxMsg, renderedGen: -1, unreadFrom: -1}
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
		// Senos eilutės cache'e nebeatitinka žinutėms — cache'ą
		// reikia atkurti nuo nulio (kitaip inkrementalus cache'as
		// rodytų senas, jau ištrintas žinutes).
		ch.rendered = nil
		ch.renderedGen = -1
		ch.renderedMsgCount = 0
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

// maxNicksPerChannel — didžiausias nick'ų skaičius viename kanale.
//
// Be ribos priešiškas serveris galėtų siųsti tūkstančius JOIN'ų į vieną
// kanalą — kiekvienas su savo nick'u. Sąrašas augdavo neribotai, o
// kiekvienas įterpimas buvo O(n) (žr. addNick) → O(n²) visas sudėtis.
// 10 000 nick'ų kanale tai ~3,5 s pauzės (užblokavus visą klientą).
const maxNicksPerChannel = 5000

// normNick normalizuoja nick'ą į žemėlapio raktą: be statuso prefikso,
// su serverio casemapping'u.
//
// NUDAŽIA: nickEqual() lygina su casemapping'u, todėl raktas turi būti
// lyginamas TAIP PAT — „Žilvinas“ ir „žilvinas“ yra VIENAS nick'as
// (pagal RFC 1459 ASCII rulą), o ne du.
//
// CASEMAPPING: pagal RFC 1459 lygina „[“ ≡ „{“, „]“ ≡ „}“, „\\“ ≡ „|“;
// pagal „ascii“ (ASCII casefold) — NIEKO nelygina. Todėl ant „ascii“
// serverio „[a]“ ir „{a}“ yra DU SKIRTINGI nick'ai. Ankstesnė versija
// visada naudojo RFC 1459 → „{a}“ būdavo atmestas kaip „[a]“ dublikatas,
// o vėlesnis QUIT ištrindavo NE TĄ nick'ą.
//
// Parametras `cm` — serverio casemapping („“ arba „rfc1459“ → RFC 1459;
// „ascii“ → ASCII).
func normNick(nick, sign, cm string) string {
	bare := stripMode(nick, sign)
	if cm == "ascii" {
		// ASCII casefold — NELYGINA ne-ASCII raidžių (t. y. „Žilvinas“
		// ≠ „žilvinas“), tiksliai kaip nickEqual().
		return asciiFold(bare)
	}
	return strings.Map(lowerIRC, bare)
}

// rebuildNickSet atstato rinkinį iš `nicks` slice'o.
//
// Reikalingas po operacijų, kurios PAŠALINA arba PERVADINA nick'ą.
func (ch *Channel) rebuildNickSet(sign string) {
	ch.nickSet = make(map[string]struct{}, len(ch.nicks))
	for _, n := range ch.nicks {
		ch.nickSet[normNick(n, sign, ch.casemapping)] = struct{}{}
	}
}

// hasNickKey tikrina, ar kanale yra toks nick'as (O(1)).
//
// NAUDOJA nickSet rinkinį. Jei rinkinys neegzistuoja (kanalas
// sukurtas senais būdais) — atstatomas iš `nicks` slice'o.
func (ch *Channel) hasNickKey(nick, sign string) bool {
	if ch.nickSet == nil {
		ch.rebuildNickSet(sign)
	}
	_, ok := ch.nickSet[normNick(nick, sign, ch.casemapping)]
	return ok
}

// findNickIdx ieško nick'o indekso kanale. Grąžina -1, jei nėra.
//
// NAUDOJA rinkinį tik kaip greitą „egzistuoja?“ patikrinimą; indeksas
// surandamas tiesiogine paieška (tik reikia retai — pašalinimas,
// pervadinas, prefikso pakeitimas — ne kiekvienam įterpinui).
//
// Tiesioginei paieškai naudojame foldEqualIRC — BE atminties alokacijų
// po kiekvieną elementą (strings.Map kurtų po laikinąją eilutę 5 000
// nick'ų sąraše — pašalinimų srautas užstrigtų klientą).
func (ch *Channel) findNickIdx(nick, sign string) int {
	if !ch.hasNickKey(nick, sign) {
		return -1
	}
	target := stripMode(nick, sign)
	ascii := ch.casemapping == "ascii"
	for i, n := range ch.nicks {
		if foldEqualIRC(stripMode(n, sign), target, ascii) {
			return i
		}
	}
	return -1
}

func (ch *Channel) addNick(nick string, signOpt ...string) {
	sign := ch.prefixSigns(signOpt...)
	if ch.nickSet == nil {
		ch.rebuildNickSet(sign)
	}
	key := normNick(nick, sign, ch.casemapping)
	if _, exists := ch.nickSet[key]; exists {
		// Nick'as jau yra sąraše. Bet 353 (NAMES) gali atneši STATUSO
		// prefiksą, kurio nebuvo, kai jis įrašytas (pvz. JOIN pridėjo
		// „Žilvinas“, o 353 atnešė „@Žilvinas“). Anksčiau čia buvo
		// grynas „return“ → prefiksas tyliai dingdavo ir vartotojas
		// nematė, kad jis operatorius.
		// Atnaujiname tik tada, kai NAUJAS nick'as turi prefiksą —
		// jei jo nėra, esantis prefiksas (iš MODE komandos) lieka.
		i := ch.findNickIdx(nick, sign)
		if i < 0 {
			return
		}
		n := ch.nicks[i]
		newPrefix := modePrefix(nick, sign)
		if newPrefix == "" || newPrefix == modePrefix(n, sign) {
			return
		}
		ch.nicks[i] = newPrefix + stripMode(n, sign)
		sortNicks(ch.nicks, sign)
		// Raktas (be prefikso) tas pats — rinkinio atnaujinti nereikia.
		return
	}
	// RIBOS — kanale negali būti begalybės nick'ų.
	if len(ch.nicks) >= maxNicksPerChannel {
		return
	}
	// Įstatome į SAVO VIETĄ (dichotominė paieška) — anksčiau kiekvienam
	// įterpimui buvo pilnas sortNicks (O(n log n)). 500 nick'ų kanale tai
	// 500 × 500×log(500) operacijų; dabar — 500 × log(500) paieškų.
	pos := sort.Search(len(ch.nicks), func(i int) bool {
		return !nickLess(ch.nicks[i], nick, sign)
	})
	ch.nicks = append(ch.nicks, "")
	copy(ch.nicks[pos+1:], ch.nicks[pos:])
	ch.nicks[pos] = nick
	// Rinkinys atnaujinamas O(1) — indeksai nekeičiami.
	ch.nickSet[key] = struct{}{}
}

// hasNick tikrina, ar kanalo nick'ų sąraše yra toks nick (su prefiksu arba
// be jo). Naudojama NICK komandai — pranešimą rodoma tik ten, kur nick
// realiai buvo.
func (ch *Channel) hasNick(nick string) bool {
	return ch.findNickIdx(nick, ch.prefixSigns()) >= 0
}

func (ch *Channel) removeNick(nick string, signOpt ...string) {
	sign := ch.prefixSigns(signOpt...)
	i := ch.findNickIdx(nick, sign)
	if i < 0 {
		return
	}
	ch.nicks = append(ch.nicks[:i], ch.nicks[i+1:]...)
	// Pašalintas nick'as — rinkinys atnaujinamas.
	delete(ch.nickSet, normNick(nick, sign, ch.casemapping))
}

func (ch *Channel) renameNick(oldNick, newNick string, signOpt ...string) {
	sign := ch.prefixSigns(signOpt...)
	i := ch.findNickIdx(oldNick, sign)
	if i < 0 {
		return
	}
	oldKey := normNick(ch.nicks[i], sign, ch.casemapping)
	ch.nicks[i] = modePrefix(ch.nicks[i], sign) + newNick
	sortNicks(ch.nicks, sign)
	// Raktas pasikeitė (nick'o vardas) — rinkinys atnaujinamas.
	delete(ch.nickSet, oldKey)
	ch.nickSet[normNick(newNick, sign, ch.casemapping)] = struct{}{}
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
	i := ch.findNickIdx(nick, signs)
	if i < 0 {
		return
	}
	bare := stripMode(ch.nicks[i], signs)
	if adding {
		ch.nicks[i] = string(prefix) + bare
	} else {
		ch.nicks[i] = bare
	}
	sortNicks(ch.nicks, signs)
	// Prefiksas pasikeitė → tvarka pasikeitė, bet raktas (be prefikso) tas
	// pats — rinkinio atnaujinti nereikia.
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

// nickLess atskleidžia surūšiuojimo taisyklę, kad addNick galėtų įstatyti
// nick'ą į VIETĄ (be pilno perrankymo).
func nickLess(a, b, signs string) bool {
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
	oi, oj := order(a), order(b)
	if oi != oj {
		return oi < oj
	}
	return strings.ToLower(strip(a)) < strings.ToLower(strip(b))
}

// ---- UI ----

const chanListWidth = 18
const nickListWidth = 16

// clampStatusLeft nukirpia statuso juostos kairiąją dalį iki laisvos vietos
// (w minus statusRight plotis, ląstelėmis) ir grąžina tarpų kiekį iki
// dešinės dalies — kitaip ilgas kanalo vardas perlenkia juostą į antrą
// eilutę.
func clampStatusLeft(statusLeft, statusRight string, w int) (string, int) {
	maxLeft := w - stringWidth(statusRight)
	if maxLeft < 0 {
		maxLeft = 0
	}
	statusLeft = truncateWidth(statusLeft, maxLeft)
	pad := maxLeft - stringWidth(statusLeft)
	if pad < 0 {
		pad = 0
	}
	return statusLeft, pad
}

// daySepLine — paros persiridimo žyma (v0.4.7). Ilgose sesijose be jos
// neįskaita, kada įvyko žinutės.
func daySepLine(t time.Time) string {
	return ansiDarkGray + "── " + t.Format("2006-01-02") + " ──" + ansiReset
}

// changedDay — dviejų žinutės laikų PAROJA skiriasi.
func changedDay(a, b time.Time) bool {
	return a.Year() != b.Year() || a.YearDay() != b.YearDay()
}

func (c *Client) draw() {
	c.mu.Lock()
	w, h := c.width, c.height
	if w < 40 || h < 8 {
		c.mu.Unlock()
		return
	}
	// c.mu jau užrakintas — Locked variantas.
	active := c.activeChanLocked()
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
	activeName := active.name
	// SIDEBAR'O KANALŲ LAUKAI — nukopijuojame SU RAKINU.
	//
	// Anksčiau ch.unread / ch.highlight / ch.isPrivate buvo skaitomi PO
	// c.mu.Unlock() (ši eilutė piešė visus kanalus) — parseIRC gorutina tuo
	// pačiu juos keitė (addMsg → unread++, irc.go:726). Duomenų lenktynė.
	//
	// Atkreipkite: addMsg didina unread TIK kai kanalas NĖRA aktyvus — todėl
	// lenktynė nematyti, kai piešiamas aktyvus kanalas. Švarus
	// `go test -race` baseline'as to neįrodė.
	type chanState struct {
		unread    int
		highlight int
		isPrivate bool
	}
	states := make([]chanState, len(channels))
	for i, ch := range channels {
		states[i] = chanState{
			unread:    ch.unread,
			highlight: ch.highlight,
			isPrivate: ch.isPrivate,
		}
	}
	inputRunes := make([]rune, len(c.input))
	copy(inputRunes, c.input)
	inputPos := c.inputPos
	scrollOff := c.scrollOff
	nickScrollOff := c.nickScrollOff
	chanScrollOff := c.chanScrollOff
	connected := c.connected
	myNick := c.nick
	server := c.server
	away := c.away
	useTLS := c.useTLS
	searchMode := c.searchMode
	searchQuery := c.searchQuery
	c.mu.Unlock()

	showNicks := c.isChannel(activeName)
	out := &strings.Builder{}
	out.WriteString(ansiHideCur)

	// Terminalo LANGO ANTRAŠTĖ (OSC 0, BEL pabaiga). Kita darbalaukio
	// eilutė matys, kuris kanalas aktyvus ir kiek neperskaitytų žinučių
	// kituose languose (v0.4.7). Tekstas sanitizuotas ir kirptas — OSC
	// antraštė su valdymo simboliais kitaip taptų terminalo valdymu.
	titleTotal := 0
	for i, st := range states {
		if i != activeIdx {
			titleTotal += st.unread
		}
	}
	titleName := truncateWidth(sanitizeText(activeName), 40)
	title := "mircBSD — " + titleName
	if titleTotal > 0 {
		title += fmt.Sprintf(" (%d)", titleTotal)
	}
	out.WriteString("\033]0;" + title + "\a")

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
		tlsMark, myNick, awayMark, reconnMark, server, activeName)
	if !connected && server == "" {
		statusLeft = " mircBSD | [NEPRISIJUNGTA] "
	}
	statusRight := " PgUp/Dn:scroll Ctrl+N/P:kan Ctrl+←→:žodis Tab:AC /help "
	statusLeft = sanitizeText(statusLeft)
	// statusLeft kirpimas iki laisvos vietos: ilgas kanalo pavadinimas
	// (priešiškas serveris, ilgio ribos nėra) kitaip perlenkia statuso
	// juostą į antrą eilutę ir sutrikdo temos/paieškos braižymą.
	statusLeft, pad := clampStatusLeft(statusLeft, statusRight, w)
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
		topicPlain = truncateWidth(topicPlain, maxTopic)
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
	//
	// SLINKTIS: kanalų gali būti daugiau, nei telpa ekrane (iki 100).
	// Anksčiau rodydavome tik pirmus 12 ir likusieji buvo NEPASIEKIAMI.
	// Dabar — slinktis (chanScrollOff) + rodyklės ▲▼.
	chanScroll := chanScrollOff
	maxChanScroll := len(channels) - msgRows
	if maxChanScroll < 0 {
		maxChanScroll = 0
	}
	if chanScroll > maxChanScroll {
		chanScroll = maxChanScroll
	}
	if chanScroll < 0 {
		chanScroll = 0
	}
	for i := 0; i < msgRows; i++ {
		r := msgAreaTop + i
		out.WriteString(moveTo(r, 1))
		out.WriteString(fmt.Sprintf("\033[%dX", chanListWidth))
		out.WriteString(moveTo(r, 1))
		chanIdx := chanScroll + i
		if chanIdx < len(channels) {
			ch := channels[chanIdx]
			name := ch.name
			st := states[chanIdx]
			if chanIdx == activeIdx {
				out.WriteString(ansiBgBlue + ansiBold + ansiWhite)
			} else if st.highlight > 0 {
				out.WriteString(ansiRed + ansiBold)
			} else if st.unread > 0 {
				out.WriteString(ansiYellow + ansiBold)
			} else if st.isPrivate {
				out.WriteString(ansiMagenta)
			} else {
				out.WriteString(ansiDarkGray)
			}
			displayName := sanitizeText(name)
			if st.isPrivate {
				// Privatūs kanalai pažymėti „>“ — pavadinimas taip pat
				// turi būti sanitizuotas (priešiškas serveris gali atsiųsti
				// PRIVMSG su ESC simboliais nick'e).
				displayName = ">" + displayName
			}
			fkeyPrefix := ""
			if chanIdx < 12 {
				fkeyPrefix = fmt.Sprintf("%d:", chanIdx+1)
			}
			maxNameW := chanListWidth - 2 - len(fkeyPrefix)
			displayName = truncateWidth(displayName, maxNameW)
			label := " " + fkeyPrefix + fitWidth(displayName, maxNameW)
			if st.unread > 0 && chanIdx != activeIdx {
				unreadW := maxNameW - 3
				if unreadW < 1 {
					unreadW = 1
				}
				// Stulpelio plotis LĄSTELĖMIS (fitWidth), ne baitais (%-*s):
				// kitaip ne-ASCII pavadinimai (lietuviški, CJK) užima daugiau
				// stulpelių, nei yra baitų — „!N“ žyma išstumtų už
				// chanListWidth ir visos eilutės pradėtų svirti.
				badge := fmt.Sprintf("%d", st.unread)
				if st.highlight > 0 {
					badge = "!" + fmt.Sprintf("%d", st.highlight)
				}
				// Badge ribojamas iki eilutėje likusio ploto: milžiniškas
				// unread (100000+) kitaip išlenda už „│“.
				maxBadge := chanListWidth - 1 - len(fkeyPrefix) - unreadW
				if maxBadge < 1 {
					maxBadge = 1
				}
				if stringWidth(badge) > maxBadge {
					badge = truncateWidth(badge, maxBadge)
					if badge == "" {
						badge = "+"
					}
				}
				label = " " + fkeyPrefix + fitWidth(displayName, unreadW) + badge
			}
			out.WriteString(label + ansiReset)
		}
		out.WriteString(moveTo(r, chanListWidth+1))
		out.WriteString(ansiDarkGray + "│" + ansiReset)
	}
	// Kanalų sąrašo rodyklės (kai kanalų daugiau, nei telpa ekrane)
	if maxChanScroll > 0 {
		if chanScroll > 0 {
			out.WriteString(moveTo(msgAreaTop, chanListWidth))
			out.WriteString(ansiYellow + "▲" + ansiReset)
		}
		if chanScroll < maxChanScroll {
			out.WriteString(moveTo(msgAreaBot, chanListWidth))
			out.WriteString(ansiYellow + "▼" + ansiReset)
		}
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
	// jei žinutės ir plotis nepasikeitę (greitis ant užimtų kanalų).
	//
	// INCREMENTALUS: kai pridedama nauja žinutė, senos eilutės lieka
	// galiojančios — renderiuojame tik NAUJAS. Tai sumažina braižmo
	// kainą nuo O(n) iki O(naujų žinučių) kiekviename redraw.
	c.mu.Lock()
	gen := active.renderGen
	msgs := active.messages
	// Kiek žinučių cache'e jau apskaičiuota (ir ar tas pats plotis/nick).
	cacheOK := active.renderedGen == gen && active.renderedW == msgW && active.renderedNick == myNick
	// Jei žinutės tik PRIDETOS (renderGen pakito, bet senas cache galioja
	// tame pačiame pločiu) — tęsiame cache'ą. Žinutės gali būti tik
	// pridamos į galą (addMsg), todėl cache'o prieiga lieka tik galoje.
	incremental := active.renderedGen != -1 && active.renderedW == msgW &&
		active.renderedNick == myNick && active.renderedMsgCount <= len(msgs)
	// Paskutinės perrenderiuotos žinutės laikas — dienos skirtikliui
	// (v0.4.7): jei nauja žinutė kitoje PAROJE, prieš ją įstatome
	// „── YYYY-MM-DD ──“ eilutę (tik rašymo metu, nenorim laikyti
	// žinutėse, kad į žurnalą neiškeliautų dukart).
	var prevTs time.Time
	if incremental && active.renderedMsgCount > 0 && active.renderedMsgCount <= len(msgs) {
		prevTs = msgs[active.renderedMsgCount-1].ts
	}
	var lines []string
	if cacheOK {
		lines = active.rendered
		c.mu.Unlock()
	} else {
		// Kopijuojame tik TA DALĮ, kurią reikia renderiuoti.
		start := 0
		if incremental {
			start = active.renderedMsgCount
		}
		// Copiuojame naujas žinutes (be c.mu — renderLines ilgas).
		newMsgs := make([]Message, len(msgs)-start)
		copy(newMsgs, msgs[start:])
		existing := active.rendered
		c.mu.Unlock()

		lines = existing
		if !incremental {
			lines = nil
		}
		if len(newMsgs) > 0 {
			fresh := make([]string, 0, len(newMsgs)+len(newMsgs))
			for i, m := range newMsgs {
				prev := prevTs
				if i > 0 {
					prev = newMsgs[i-1].ts
				}
				if !prev.IsZero() && changedDay(prev, m.ts) {
					fresh = append(fresh, daySepLine(m.ts))
				}
				fresh = append(fresh, m.renderLines(msgW, myNick)...)
			}
			lines = append(lines, fresh...)
		}

		// Cache'uoti tik jei žinutės nepakitę renderio metu
		c.mu.Lock()
		if active.renderGen == gen {
			active.rendered = lines
			active.renderedGen = gen
			active.renderedW = msgW
			active.renderedNick = myNick
			active.renderedMsgCount = len(msgs)
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
				out.WriteString(ansiBold + ansiCyan + fitWidth(" Nick"+hint, nickListWidth) + ansiReset)
			} else {
				ni := nickScrollOff + i - 1
				if ni < len(nicks) {
					n := sanitizeText(nicks[ni])
					display := truncateWidth(n, nickListWidth)
					color := ansiWhite
					switch modePrefix(n, active.prefixSigns()) {
					case "~", "&", "@":
						color = ansiYellow
					case "%":
						color = ansiGreen
					case "+":
						color = ansiCyan
					}
					out.WriteString(color + fitWidth(display, nickListWidth) + ansiReset)
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
	// SANITIZUOJAME ĮVESTIES EILUTĘ — tai vienintelė vieta, kur nesanitizuotas
	// tekstas pasiekia terminalą. Serverio duomenys (nick'o prefiksas, kuris
	// atėjo per JOIN/NAMES) gali turėti ESC/OSC seką: be šio filtro
	// „OSC 52“ (rašymas į iškarpinę) ar „OSC 0“ (keisti lango pavadinimą)
	// pasiektų terminalą be vartotojo žinios.
	safeInput := sanitizeText(string(inputRunes))
	dispRunes := []rune(safeInput)
	maxInput := w - prefixLen - 1
	dispStart := 0
	if len(dispRunes) > maxInput {
		dispStart = len(dispRunes) - maxInput
	}
	out.WriteString(string(dispRunes[dispStart:]))

	// Kursorio pozicija — per SANITIZUOTĄ tekstą (sanitizavimas pašalina
	// simbolius, todėl raw indeksas nebetinka). Skaičiuojame, kiek simbolių
	// liko iki inputPos.
	prefixRunes := inputRunes
	if inputPos < len(prefixRunes) {
		prefixRunes = prefixRunes[:inputPos]
	}
	curPos := len([]rune(sanitizeText(string(prefixRunes))))
	cursorCol := prefixLen + 1 + (curPos - dispStart)
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
