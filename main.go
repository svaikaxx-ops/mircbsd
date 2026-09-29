package main

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"hash/fnv"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/term"
)

const version = "0.3.1"

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

var tsColorCode = ansiDarkGray // konfigūruojama timestamp spalva

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

// ---- Platforma ----

// osLabel – OS pavadinimas rodymui (CTCP VERSION, svečio pranešimas).
func osLabel() string {
	switch runtime.GOOS {
	case "linux":
		return "Linux"
	case "freebsd", "openbsd", "netbsd", "dragonfly":
		return "FreeBSD"
	case "darwin":
		return "macOS"
	case "windows":
		return "Windows"
	}
	return runtime.GOOS
}

// openURL atidaro URL numatytame naršyklėje.
// Debian: xdg-open (xdg-utils), gio, sensible-browser, x-www-browser, firefox.
func openURL(url string) error {
	type opener struct {
		name string
		args []string
	}

	var candidates []opener
	switch runtime.GOOS {
	case "darwin":
		candidates = []opener{{"open", nil}}
	case "windows":
		candidates = []opener{{"rundll32", []string{"url.dll,FileProtocolHandler"}}}
	default: // linux, freebsd, *bsd
		candidates = []opener{
			{"xdg-open", nil},
			{"gio", []string{"open"}},
			{"sensible-browser", nil},
			{"x-www-browser", nil},
			{"www-browser", nil},
			{"firefox", nil},
		}
	}

	tried := make([]string, 0, len(candidates))
	for _, cand := range candidates {
		tried = append(tried, cand.name)
		path, err := exec.LookPath(cand.name)
		if err != nil {
			continue
		}
		args := append(append([]string{}, cand.args...), url)
		if err := exec.Command(path, args...).Start(); err == nil {
			return nil
		}
	}
	return fmt.Errorf("naršyklės įrankis nerastas (bandė: %s). Debian: apt install xdg-utils",
		strings.Join(tried, ", "))
}

// ---- Config ----

type Config struct {
	Server         string
	Port           string
	TLS            bool
	Nick           string
	RealName       string
	NickServ       string
	SASLPass       string // SASL PLAIN slaptažodis (vartotojas = Nick)
	AutoJoin       []string
	Perform        []string // komandos vykdomos po prisijungimo
	HighlightWords []string // papildomi žodžiai highlight'inimui
	LogDir         string
	MaxMessages    int
	Reconnect      bool
	CTCPVersion    bool
	TimestampColor string // gray (numatyta), blue, green, cyan, none
}

func defaultConfig() Config {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	return Config{
		Port:        "6667",
		Nick:        "mircbsd",
		RealName:    "mircBSD IRC Client",
		LogDir:      filepath.Join(home, "irclogs"),
		MaxMessages: 5000,
		Reconnect:   true,
		CTCPVersion: true,
	}
}

func loadConfig(path string) Config {
	cfg := defaultConfig()
	f, err := os.Open(path)
	if err != nil {
		return cfg
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		k, v := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		switch strings.ToLower(k) {
		case "server":
			cfg.Server = v
		case "port":
			cfg.Port = v
		case "tls":
			cfg.TLS = v == "true" || v == "1" || v == "yes"
		case "nick":
			cfg.Nick = v
		case "realname":
			cfg.RealName = v
		case "nickserv":
			cfg.NickServ = v
		case "autojoin":
			for _, ch := range strings.Split(v, ",") {
				ch = strings.TrimSpace(ch)
				if ch != "" {
					cfg.AutoJoin = append(cfg.AutoJoin, ch)
				}
			}
		case "logdir":
			if strings.HasPrefix(v, "~/") {
				if home, err := os.UserHomeDir(); err == nil && home != "" {
					v = filepath.Join(home, v[2:])
				}
			}
			cfg.LogDir = v
		case "maxmessages":
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				cfg.MaxMessages = n
			}
		case "reconnect":
			cfg.Reconnect = v == "true" || v == "1" || v == "yes"
		case "ctcpversion":
			cfg.CTCPVersion = v == "true" || v == "1" || v == "yes"
		case "sasl":
			cfg.SASLPass = v
		case "tscolor":
			cfg.TimestampColor = v
		case "perform":
			for _, cmd := range strings.Split(v, ";") {
				cmd = strings.TrimSpace(cmd)
				if cmd != "" {
					cfg.Perform = append(cfg.Perform, cmd)
				}
			}
		case "highlight_words":
			for _, w := range strings.Split(v, ",") {
				w = strings.TrimSpace(w)
				if w != "" {
					cfg.HighlightWords = append(cfg.HighlightWords, w)
				}
			}
		}
	}
	return cfg
}

// configDir grąžina konfigūracijos katalogą.
// Linux/FreeBSD: $XDG_CONFIG_HOME/mircbsd, jei nėra – $HOME/.config/mircbsd.
func configDir() string {
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		return filepath.Join(dir, "mircbsd")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".config", "mircbsd")
	}
	return ""
}

func configPath() string {
	dir := configDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "config")
}

func historyPath() string {
	dir := configDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "history")
}

func ignorePath() string {
	dir := configDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "ignore")
}

func (c *Client) saveIgnore() {
	path := ignorePath()
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return
	}
	c.mu.Lock()
	list := make([]string, len(c.ignoreList))
	copy(list, c.ignoreList)
	c.mu.Unlock()

	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return
	}
	for _, n := range list {
		_, _ = f.WriteString(n + "\n")
	}
	f.Close()
	os.Rename(tmp, path)
}

func (c *Client) loadIgnore() {
	path := ignorePath()
	if path == "" {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var list []string
	for sc.Scan() {
		n := strings.TrimSpace(sc.Text())
		if n != "" {
			list = append(list, n)
		}
	}
	c.mu.Lock()
	c.ignoreList = list
	c.mu.Unlock()
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
		nickStr := "<" + m.nick + "> "
		if m.highlight {
			p = part{ansiBgRed + ansiBold + ts + nc + nickStr + ansiReset + ansiBgRed, tsLen + len([]rune(nickStr))}
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

type Channel struct {
	name        string
	messages    []Message
	unread      int
	highlight   int
	nicks       []string
	topic       string
	maxMessages int
	scrollOff   int
	isPrivate   bool // tiesioginis pokalbis (ne kanalas)

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

func (ch *Channel) addNick(nick string) {
	for _, n := range ch.nicks {
		if strings.EqualFold(stripMode(n), stripMode(nick)) {
			return
		}
	}
	ch.nicks = append(ch.nicks, nick)
	sortNicks(ch.nicks)
}

func (ch *Channel) removeNick(nick string) {
	for i, n := range ch.nicks {
		if strings.EqualFold(stripMode(n), stripMode(nick)) {
			ch.nicks = append(ch.nicks[:i], ch.nicks[i+1:]...)
			return
		}
	}
}

func (ch *Channel) renameNick(oldNick, newNick string) {
	for i, n := range ch.nicks {
		if strings.EqualFold(stripMode(n), oldNick) {
			ch.nicks[i] = modePrefix(n) + newNick
			sortNicks(ch.nicks)
			return
		}
	}
}

func (ch *Channel) setMode(nick, mode string) {
	adding := true
	for _, r := range mode {
		switch r {
		case '+':
			adding = true
		case '-':
			adding = false
		case 'o':
			ch.changeNickMode(nick, '@', adding)
		case 'v':
			ch.changeNickMode(nick, '+', adding)
		case 'h':
			ch.changeNickMode(nick, '%', adding)
		case 'q':
			ch.changeNickMode(nick, '~', adding)
		case 'a':
			ch.changeNickMode(nick, '&', adding)
		}
	}
}

func (ch *Channel) changeNickMode(nick string, prefix byte, adding bool) {
	for i, n := range ch.nicks {
		if strings.EqualFold(stripMode(n), nick) {
			bare := stripMode(n)
			if adding {
				ch.nicks[i] = string(prefix) + bare
			} else {
				ch.nicks[i] = bare
			}
			sortNicks(ch.nicks)
			return
		}
	}
}

func stripMode(nick string) string {
	if len(nick) > 0 && strings.ContainsRune("@+%~&", rune(nick[0])) {
		return nick[1:]
	}
	return nick
}

func modePrefix(nick string) string {
	if len(nick) > 0 && strings.ContainsRune("@+%~&", rune(nick[0])) {
		return string(nick[0])
	}
	return ""
}

func nickOrder(n string) int {
	if len(n) == 0 {
		return 4
	}
	switch n[0] {
	case '~', '&':
		return 0
	case '@':
		return 1
	case '%':
		return 2
	case '+':
		return 3
	}
	return 4
}

func sortNicks(nicks []string) {
	sort.Slice(nicks, func(i, j int) bool {
		oi, oj := nickOrder(nicks[i]), nickOrder(nicks[j])
		if oi != oj {
			return oi < oj
		}
		return strings.ToLower(stripMode(nicks[i])) < strings.ToLower(stripMode(nicks[j]))
	})
}

// ---- IRC Client ----

const maxMsgLen = 400

type Client struct {
	mu        sync.Mutex
	nick      string
	server    string
	port      string
	useTLS    bool
	conn      net.Conn
	connected bool

	realName     string
	nickservPass string
	saslPass     string
	autoJoin     []string
	away         string
	perform      []string
	ignoreList   []string

	reconnect      bool
	reconnectDelay time.Duration
	connecting     bool
	ctcpVersion    bool

	// IRCv3 CAP / SASL
	activeCaps   map[string]bool
	capSASL      bool
	saslOk       bool // SASL autentifikacija sėkminga (903)
	saslPending  bool
	capAvailable map[string]bool // CAP LS sąrašo kaupimas (tęsinio eilutėms)

	// URL istorija
	lastURLs []string

	highlightWords []string // papildomi highlight žodžiai

	// Away throttle: nick -> paskutinio pranešimo laikas
	awayNotified map[string]time.Time

	// Paste apsauga
	pasteBuf []string

	// Ping keepalive
	lastPong   time.Time
	pingTicker *time.Ticker

	channels    []*Channel
	activeIdx   int
	maxMessages int

	writeCh chan string
	redraw  chan struct{}

	oldState *term.State
	width    int
	height   int

	input         []rune
	inputPos      int
	scrollOff     int
	nickScrollOff int

	history    []string
	historyIdx int

	tabCandidates []string
	tabIdx        int
	tabPrefix     string

	keysDebug bool

	// Paieška
	searchMode      bool
	searchQuery     string
	searchResult    []int // eilučių indeksai
	searchResultIdx int   // paskutinis rastas indeksas (-1 = nerasta)
	lastSearchQuery string

	logDir   string
	logFiles map[string]*os.File
}

func newClient(cfg Config) *Client {
	c := &Client{
		nick:            cfg.Nick,
		server:          cfg.Server,
		port:            cfg.Port,
		useTLS:          cfg.TLS,
		realName:        cfg.RealName,
		nickservPass:    cfg.NickServ,
		saslPass:        cfg.SASLPass,
		autoJoin:        cfg.AutoJoin,
		perform:         cfg.Perform,
		highlightWords:  cfg.HighlightWords,
		logDir:          cfg.LogDir,
		maxMessages:     cfg.MaxMessages,
		reconnect:       cfg.Reconnect,
		ctcpVersion:     cfg.CTCPVersion,
		channels:        []*Channel{newChannel("*server*", cfg.MaxMessages)},
		writeCh:         make(chan string, 256),
		redraw:          make(chan struct{}, 1),
		historyIdx:      -1,
		searchResultIdx: -1,
		logFiles:        make(map[string]*os.File),
		activeCaps:      make(map[string]bool),
		awayNotified:    make(map[string]time.Time),
		lastPong:        time.Now(),
	}
	return c
}

func (c *Client) findCh(name string) *Channel {
	for _, ch := range c.channels {
		if strings.EqualFold(ch.name, name) {
			return ch
		}
	}
	return nil
}

func (c *Client) ensureCh(name string) *Channel {
	ch := c.findCh(name)
	if ch != nil {
		return ch
	}
	ch = newChannel(name, c.maxMessages)
	c.channels = append(c.channels, ch)
	return ch
}

func (c *Client) isIgnored(nick string) bool {
	for _, ig := range c.ignoreList {
		if strings.EqualFold(ig, nick) {
			return true
		}
	}
	return false
}

func (c *Client) addMsg(chanName string, m Message) {
	c.mu.Lock()
	ch := c.ensureCh(chanName)
	ch.addMsg(m)
	isActive := c.channels[c.activeIdx] == ch
	if !isActive {
		ch.unread++
		if m.highlight {
			ch.highlight++
		}
	}
	logDir := c.logDir
	server := c.server
	c.mu.Unlock()

	if m.highlight {
		fmt.Print("\a")
	}
	go c.logMessage(logDir, server, chanName, m)
	c.queueRedraw()
}

func (c *Client) serverMsg(text string) {
	c.addMsg("*server*", Message{ts: time.Now(), text: text, msgType: MsgServer})
}

func (c *Client) errMsg(text string) {
	c.addMsg("*server*", Message{ts: time.Now(), text: text, msgType: MsgError})
}

func (c *Client) queueRedraw() {
	select {
	case c.redraw <- struct{}{}:
	default:
	}
}

func (c *Client) switchCh(idx int) {
	c.mu.Lock()
	if idx >= 0 && idx < len(c.channels) {
		// Išsaugoti dabartinę scroll poziciją
		c.channels[c.activeIdx].scrollOff = c.scrollOff
		c.activeIdx = idx
		// Atstatyti išsaugotą poziciją
		c.scrollOff = c.channels[idx].scrollOff
		c.channels[idx].unread = 0
		c.channels[idx].highlight = 0
		c.nickScrollOff = 0
		c.searchMode = false
	}
	c.mu.Unlock()
	c.queueRedraw()
}

// ---- Logging ----

func (c *Client) logMessage(logDir, server, chanName string, m Message) {
	if logDir == "" {
		return
	}
	key := server + "/" + chanName
	c.mu.Lock()
	f, ok := c.logFiles[key]
	if !ok {
		dir := filepath.Join(logDir, sanitizePath(server))
		if err := os.MkdirAll(dir, 0700); err != nil {
			c.mu.Unlock()
			return
		}
		path := filepath.Join(dir, sanitizePath(chanName)+".log")
		var err error
		f, err = os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			c.mu.Unlock()
			return
		}
		c.logFiles[key] = f
	}
	c.mu.Unlock()

	ts := m.ts.Format("2006-01-02 15:04:05")
	plain := sanitizeText(stripIRCFormat(m.text))
	nick := sanitizeText(m.nick)
	var line string
	switch m.msgType {
	case MsgChat:
		line = fmt.Sprintf("[%s] <%s> %s\n", ts, nick, plain)
	case MsgAction:
		line = fmt.Sprintf("[%s] * %s %s\n", ts, nick, plain)
	case MsgJoin:
		line = fmt.Sprintf("[%s] --> %s prisijungė\n", ts, nick)
	case MsgPart:
		line = fmt.Sprintf("[%s] <-- %s išėjo (%s)\n", ts, nick, plain)
	case MsgQuit:
		line = fmt.Sprintf("[%s] <-- %s atsijungė (%s)\n", ts, nick, plain)
	default:
		line = fmt.Sprintf("[%s] %s\n", ts, plain)
	}
	_, _ = f.WriteString(line)
}

func sanitizePath(s string) string {
	r := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_")
	return r.Replace(s)
}

func (c *Client) closeLogs() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, f := range c.logFiles {
		f.Close()
	}
	c.logFiles = make(map[string]*os.File)
}

// ---- Istorija išsaugojimas ----

func (c *Client) saveHistory() {
	path := historyPath()
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return
	}
	c.mu.Lock()
	lines := make([]string, len(c.history))
	copy(lines, c.history)
	c.mu.Unlock()

	// Išsaugoti paskutinius 1000
	if len(lines) > 1000 {
		lines = lines[len(lines)-1000:]
	}
	// Atominis rašymas: rašom į temp, tada pervadinam
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return
	}
	for _, l := range lines {
		_, _ = f.WriteString(l + "\n")
	}
	f.Close()
	os.Rename(tmp, path)
}

func (c *Client) loadHistory() {
	path := historyPath()
	if path == "" {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var lines []string
	for sc.Scan() {
		if l := sc.Text(); l != "" {
			lines = append(lines, l)
		}
	}
	c.mu.Lock()
	c.history = lines
	c.mu.Unlock()
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
	active := c.channels[c.activeIdx]
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

	showNicks := strings.HasPrefix(active.name, "#") || strings.HasPrefix(active.name, "&")
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
				displayName = ">" + name
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
		out.WriteString(moveTo(msgAreaTop, msgColStart))
		out.WriteString(ansiYellow + ansiBold + " ↑ SCROLL ─ PgDn/↓ grįžti žemyn " + ansiReset)
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
					switch modePrefix(n) {
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
	c.mu.Unlock()
	if hasPaste {
		if len(b) == 1 {
			switch b[0] {
			case '\r', '\n':
				c.mu.Lock()
				lines := c.pasteBuf
				c.pasteBuf = nil
				active := c.channels[c.activeIdx]
				c.mu.Unlock()
				if c.connected && active.name != "*server*" {
					for _, l := range lines {
						if l == "" {
							continue
						}
						c.send(fmt.Sprintf("PRIVMSG %s :%s", active.name, l))
						c.addMsg(active.name, Message{ts: time.Now(), nick: c.nick, text: l, msgType: MsgChat})
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
		raw := strings.ReplaceAll(string(b), "\r\n", "\n")
		raw = strings.ReplaceAll(raw, "\r", "\n")
		var lines []string
		for _, l := range strings.Split(raw, "\n") {
			if l != "" {
				lines = append(lines, l)
			}
		}
		if len(lines) > 1 {
			c.mu.Lock()
			c.pasteBuf = lines
			c.mu.Unlock()
			c.serverMsg(fmt.Sprintf("⚠ Paste: %d eilutės. Enter=siųsti, Esc=atšaukti", len(lines)))
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
		switch b[2] {
		case 'A':
			c.mu.Lock()
			empty := len(c.input) == 0
			c.mu.Unlock()
			if empty {
				c.mu.Lock()
				c.scrollOff += 3
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
			c.mu.Unlock()
			c.queueRedraw()
			return
		case '6':
			c.mu.Lock()
			c.scrollOff -= c.height - 4
			if c.scrollOff < 0 {
				c.scrollOff = 0
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
			idx := (c.activeIdx + 1) % len(c.channels)
			c.mu.Unlock()
			c.switchCh(idx)

		case 16: // Ctrl+P
			c.mu.Lock()
			idx := c.activeIdx - 1
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
			active := c.channels[c.activeIdx]
			c.mu.Unlock()
			if q != "" {
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
			totalMsgs := len(ch.messages)
			c.scrollOff = totalMsgs - i - 1
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
	"/away", "/back", "/clear", "/close", "/connect", "/cycle", "/exit",
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
	active := c.channels[c.activeIdx]
	c.mu.Unlock()

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
			bare := stripMode(n)
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
// IDENTIFY), nesaugomos įvesties istorijos faile.
func isSensitiveHistory(line string) bool {
	low := strings.ToLower(line)
	// /msg nickserv identify <pass> arba bet kuri kita eilutė su IDENTIFY
	if strings.Contains(low, "identify") {
		return true
	}
	// /ns ident <pass> (trumpinys)
	fields := strings.Fields(strings.TrimPrefix(low, "/"))
	return len(fields) >= 2 && fields[0] == "ns" && (fields[1] == "ident" || fields[1] == "identify")
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
	active := c.channels[c.activeIdx]
	c.mu.Unlock()
	if active.name == "*server*" {
		c.serverMsg("Rašyk /join #kanalas kad prisijungtum")
		return
	}
	if !c.connected {
		c.errMsg("Neprisijungta")
		return
	}
	runes := []rune(text)
	for len(runes) > 0 {
		chunk := runes
		if len(chunk) > maxMsgLen {
			chunk = runes[:maxMsgLen]
			runes = runes[maxMsgLen:]
		} else {
			runes = nil
		}
		part := string(chunk)
		c.send(fmt.Sprintf("PRIVMSG %s :%s", active.name, part))
		c.addMsg(active.name, Message{ts: time.Now(), nick: c.nick, text: part, msgType: MsgChat})
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
		go c.connectLoop()

	case "reconnect":
		c.mu.Lock()
		alreadyConnecting := c.connecting
		if c.conn != nil {
			c.conn.Close()
		}
		c.mu.Unlock()
		c.serverMsg("Jungiamasi iš naujo...")
		if !alreadyConnecting {
			go c.connectLoop()
		}

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
		if !strings.HasPrefix(chName, "#") && !strings.HasPrefix(chName, "&") {
			chName = "#" + chName
		}
		c.mu.Lock()
		c.ensureCh(chName)
		c.mu.Unlock()
		if c.connected {
			if len(parts) == 2 && parts[1] != "" {
				c.send("JOIN " + chName + " " + parts[1])
			} else {
				c.send("JOIN " + chName)
			}
		}

	case "part", "leave":
		c.mu.Lock()
		active := c.channels[c.activeIdx]
		c.mu.Unlock()
		msg := args
		if msg == "" {
			msg = "Viso gero!"
		}
		if c.connected && (strings.HasPrefix(active.name, "#") || strings.HasPrefix(active.name, "&")) {
			c.send(fmt.Sprintf("PART %s :%s", active.name, msg))
		}

	case "msg", "query", "m":
		parts := strings.SplitN(args, " ", 2)
		if len(parts) < 2 {
			c.errMsg("Naudojimas: /msg <nick> <žinutė>")
			return
		}
		target, text := parts[0], parts[1]
		if c.connected {
			runes := []rune(text)
			for len(runes) > 0 {
				chunk := runes
				if len(chunk) > maxMsgLen {
					chunk = runes[:maxMsgLen]
					runes = runes[maxMsgLen:]
				} else {
					runes = nil
				}
				c.send(fmt.Sprintf("PRIVMSG %s :%s", target, string(chunk)))
			}
		}
		c.mu.Lock()
		c.ensureCh(target)
		c.mu.Unlock()
		c.addMsg(target, Message{ts: time.Now(), nick: c.nick, text: text, msgType: MsgChat})

	case "notice":
		parts := strings.SplitN(args, " ", 2)
		if len(parts) < 2 {
			c.errMsg("Naudojimas: /notice <nick> <žinutė>")
			return
		}
		if c.connected {
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
			c.mu.Lock()
			chName = c.channels[c.activeIdx].name
			c.mu.Unlock()
		}
		if c.connected {
			c.send(fmt.Sprintf("INVITE %s %s", nick, chName))
			c.serverMsg(fmt.Sprintf("Pakviestas %s į %s", nick, chName))
		}

	case "cycle":
		c.mu.Lock()
		active := c.channels[c.activeIdx]
		chName := active.name
		c.mu.Unlock()
		if !strings.HasPrefix(chName, "#") && !strings.HasPrefix(chName, "&") {
			c.errMsg("/cycle galima tik kanale")
			return
		}
		if c.connected {
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
		c.mu.Lock()
		chName := c.channels[c.activeIdx].name
		c.mu.Unlock()
		if c.connected {
			c.send(fmt.Sprintf("MODE %s +o %s", chName, firstField(args)))
		}

	case "deop":
		if args == "" {
			c.errMsg("Naudojimas: /deop <nick>")
			return
		}
		c.mu.Lock()
		chName := c.channels[c.activeIdx].name
		c.mu.Unlock()
		if c.connected {
			c.send(fmt.Sprintf("MODE %s -o %s", chName, firstField(args)))
		}

	case "voice":
		if args == "" {
			c.errMsg("Naudojimas: /voice <nick>")
			return
		}
		c.mu.Lock()
		chName := c.channels[c.activeIdx].name
		c.mu.Unlock()
		if c.connected {
			c.send(fmt.Sprintf("MODE %s +v %s", chName, firstField(args)))
		}

	case "devoice":
		if args == "" {
			c.errMsg("Naudojimas: /devoice <nick>")
			return
		}
		c.mu.Lock()
		chName := c.channels[c.activeIdx].name
		c.mu.Unlock()
		if c.connected {
			c.send(fmt.Sprintf("MODE %s -v %s", chName, firstField(args)))
		}

	case "me":
		c.mu.Lock()
		active := c.channels[c.activeIdx]
		c.mu.Unlock()
		if c.connected && active.name != "*server*" {
			c.send(fmt.Sprintf("PRIVMSG %s :\x01ACTION %s\x01", active.name, args))
			c.addMsg(active.name, Message{ts: time.Now(), nick: c.nick, text: args, msgType: MsgAction})
		}

	case "topic":
		c.mu.Lock()
		active := c.channels[c.activeIdx]
		c.mu.Unlock()
		if c.connected {
			if args == "" {
				c.send("TOPIC " + active.name)
			} else {
				c.send(fmt.Sprintf("TOPIC %s :%s", active.name, args))
			}
		}

	case "kick", "k":
		parts := strings.SplitN(args, " ", 2)
		if !c.connected || len(parts) < 1 || parts[0] == "" {
			c.errMsg("Naudojimas: /kick <nick> [priežastis]")
			return
		}
		c.mu.Lock()
		chName := c.channels[c.activeIdx].name
		c.mu.Unlock()
		if !strings.HasPrefix(chName, "#") && !strings.HasPrefix(chName, "&") {
			c.errMsg("/kick galima tik kanale")
			return
		}
		reason := "Kicked"
		if len(parts) == 2 && parts[1] != "" {
			reason = parts[1]
		}
		c.send(fmt.Sprintf("KICK %s %s :%s", chName, parts[0], reason))

	case "mode":
		if !c.connected || args == "" {
			c.errMsg("Naudojimas: /mode <target|+/-mode> [parametrai]")
			return
		}
		fields := strings.Fields(args)
		// Jei pirmasis argumentas prasideda +/-, taikome aktyviam kanalui
		if strings.HasPrefix(fields[0], "+") || strings.HasPrefix(fields[0], "-") {
			c.mu.Lock()
			chName := c.channels[c.activeIdx].name
			c.mu.Unlock()
			c.send("MODE " + chName + " " + args)
		} else {
			c.send("MODE " + args)
		}

	case "whois", "wi":
		if w := firstField(args); c.connected && w != "" {
			c.send("WHOIS " + w)
		}

	case "away":
		if args == "" {
			c.mu.Lock()
			c.away = ""
			c.mu.Unlock()
			if c.connected {
				c.send("AWAY")
			}
			c.serverMsg("Away išjungtas")
		} else {
			c.mu.Lock()
			c.away = args
			c.mu.Unlock()
			if c.connected {
				c.send("AWAY :" + args)
			}
			c.serverMsg("Away: " + args)
		}
		c.queueRedraw()

	case "time":
		if c.connected {
			c.send("TIME")
		} else {
			c.errMsg("Neprisijungta")
		}

	case "clear":
		c.mu.Lock()
		c.channels[c.activeIdx].messages = nil
		c.channels[c.activeIdx].scrollOff = 0
		c.channels[c.activeIdx].renderGen++
		c.mu.Unlock()
		c.queueRedraw()

	case "names":
		c.mu.Lock()
		active := c.channels[c.activeIdx]
		chName := active.name
		do := c.connected && (strings.HasPrefix(chName, "#") || strings.HasPrefix(chName, "&"))
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
		if c.connected {
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
		if c.connected {
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
			active := c.channels[c.activeIdx]
			c.mu.Unlock()
			c.doSearch(active, args)
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
		if c.connected && args != "" {
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
		if idx == 0 {
			c.mu.Unlock()
			return
		}
		chName := c.channels[idx].name
		c.channels = append(c.channels[:idx], c.channels[idx+1:]...)
		if c.activeIdx >= len(c.channels) {
			c.activeIdx = len(c.channels) - 1
		}
		c.mu.Unlock()
		if c.connected && (strings.HasPrefix(chName, "#") || strings.HasPrefix(chName, "&")) {
			c.send("PART " + chName + " :Langas uždarytas")
		}
		c.queueRedraw()

	case "quit", "exit", "q":
		msg := args
		if msg == "" {
			msg = "mircBSD"
		}
		if c.connected {
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
			"/close                               uždaryti langą (+ PART)",
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
			"/w [n]                               langų sąrašas",
			"/quit                                išeiti",
			"───────────────────────────────────────────────────",
			"PgUp/PgDn       scroll      ↑↓   scroll / istorija",
			"Ctrl+N/P        kanalai     Ctrl+R   paieška",
			"Ctrl+L          ekrano atnaujinimas  Ctrl+W  ištrinti žodį",
			"Ctrl+←/→        žodžio navigacija",
			"F1-F12 / Alt+1-9  kanalas pagal numerį",
			"Tab             autocomplete   Ctrl+↑↓  nick scroll",
		}
		for _, l := range lines {
			c.serverMsg(l)
		}

	default:
		c.errMsg(fmt.Sprintf("Nežinoma komanda: /%s  (naudok /help)", cmd))
	}
}

// ---- IRC ryšys ----

// ircLineSanitizer pašalina CR/LF iš siunčiamų eilučių (protokolo injekcija)
var ircLineSanitizer = strings.NewReplacer("\r", "", "\n", "")

func (c *Client) send(line string) {
	// Apsaugoti nuo IRC protokolo injekcijos per \r\n
	line = ircLineSanitizer.Replace(line)
	// writeCh keičiamas doConnect'e — skaityti tik po rakinimu
	c.mu.Lock()
	writeCh := c.writeCh
	c.mu.Unlock()
	select {
	case writeCh <- line:
	default:
		// Kanalo pilnas - log bet nemesk panic
		c.addMsg("*server*", Message{ts: time.Now(), text: "ĮSPĖJIMAS: siuntimo eilė pilna, žinutė prarasta", msgType: MsgError})
	}
}

// connectLoop - prisijungimas su automatiniais bandymais iš naujo
func (c *Client) connectLoop() {
	c.mu.Lock()
	if c.connecting {
		c.mu.Unlock()
		return // jau jungiamasi
	}
	c.connecting = true
	c.reconnectDelay = 5 * time.Second
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.connecting = false
		c.mu.Unlock()
	}()

	for {
		c.doConnect()

		c.mu.Lock()
		shouldReconnect := c.reconnect
		delay := c.reconnectDelay
		// Eksponentinis atsitraukimas
		c.reconnectDelay *= 2
		if c.reconnectDelay > 120*time.Second {
			c.reconnectDelay = 120 * time.Second
		}
		c.mu.Unlock()

		if !shouldReconnect {
			return
		}

		c.serverMsg(fmt.Sprintf("Jungiamasi iš naujo po %v...", delay))
		c.queueRedraw()
		time.Sleep(delay)
	}
}

func (c *Client) doConnect() {
	c.mu.Lock()
	srvName := c.server
	srv := c.server + ":" + c.port
	useTLS := c.useTLS
	nick := c.nick
	c.mu.Unlock()

	c.serverMsg(fmt.Sprintf("Jungiamasi prie %s (TLS: %v)...", srv, useTLS))

	var conn net.Conn
	var err error

	if useTLS {
		tlsCfg := &tls.Config{
			ServerName: srvName,
			MinVersion: tls.VersionTLS12,
		}
		conn, err = tls.DialWithDialer(&net.Dialer{Timeout: 15 * time.Second}, "tcp", srv, tlsCfg)
	} else {
		conn, err = net.DialTimeout("tcp", srv, 15*time.Second)
	}
	if err != nil {
		c.errMsg(fmt.Sprintf("Nepavyko prisijungti: %v", err))
		return
	}

	// Nauja writeCh kiekvienam prisijungimui — sena goroutine nebepavogs žinučių
	newWriteCh := make(chan string, 256)
	writeStop := make(chan struct{})
	c.mu.Lock()
	c.conn = conn
	c.connected = true
	c.lastPong = time.Now()
	c.writeCh = newWriteCh
	c.mu.Unlock()

	c.mu.Lock()
	c.activeCaps = make(map[string]bool)
	c.capAvailable = make(map[string]bool)
	c.capSASL = false
	c.saslOk = false
	c.saslPending = false
	c.mu.Unlock()

	c.serverMsg("Prisijungta! Registruojamasi...")
	c.queueRedraw()

	// Write goroutine — susieta su šia jungtimi per writeStop
	go func() {
		buf := bufio.NewWriter(conn)
		for {
			select {
			case line, ok := <-newWriteCh:
				if !ok {
					return
				}
				buf.WriteString(line + "\r\n")
				// Flush kai eilė tuščia
				if len(newWriteCh) == 0 {
					if err := buf.Flush(); err != nil {
						conn.Close()
						return
					}
				}
			case <-writeStop:
				buf.Flush()
				return
			}
		}
	}()

	// Ping keepalive
	pingStop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				c.mu.Lock()
				lastPong := c.lastPong
				connected := c.connected
				c.mu.Unlock()
				if !connected {
					return
				}
				if time.Since(lastPong) > 120*time.Second {
					c.errMsg("Ping timeout - jungiamasi iš naujo")
					conn.Close()
					return
				}
				c.send(fmt.Sprintf("PING :%d", time.Now().Unix()))
			case <-pingStop:
				return
			}
		}
	}()

	c.mu.Lock()
	realName := c.realName
	c.mu.Unlock()
	if realName == "" {
		realName = "mircBSD IRC Client"
	}
	// IRCv3 CAP derybos — serveris laukia CAP END prieš siųsdamas 001
	c.send("CAP LS 302")
	c.send("NICK " + nick)
	c.send(fmt.Sprintf("USER %s 0 * :%s", nick, realName))

	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		c.parseIRC(strings.TrimRight(line, "\r\n"))
	}

	close(pingStop)
	close(writeStop)
	c.mu.Lock()
	c.connected = false
	c.conn = nil
	c.mu.Unlock()
	c.errMsg("Ryšys nutrūko")
	c.queueRedraw()
}

func (c *Client) parseIRC(line string) {
	if line == "" {
		return
	}
	var prefix, cmd string
	var params []string

	rest := line

	// IRCv3 žinutės žymės (message tags)
	var msgTime time.Time
	if strings.HasPrefix(rest, "@") {
		i := strings.Index(rest, " ")
		if i == -1 {
			return
		}
		for _, kv := range strings.Split(rest[1:i], ";") {
			if strings.HasPrefix(kv, "time=") {
				if t, err := time.Parse(time.RFC3339Nano, kv[5:]); err == nil {
					msgTime = t
				}
			}
		}
		rest = rest[i+1:]
	}

	if strings.HasPrefix(rest, ":") {
		i := strings.Index(rest, " ")
		if i == -1 {
			return
		}
		prefix = rest[1:i]
		rest = rest[i+1:]
	}
	i := strings.Index(rest, " ")
	if i == -1 {
		cmd = rest
		rest = ""
	} else {
		cmd = rest[:i]
		rest = rest[i+1:]
	}
	for rest != "" {
		if strings.HasPrefix(rest, ":") {
			params = append(params, rest[1:])
			break
		}
		i = strings.Index(rest, " ")
		if i == -1 {
			params = append(params, rest)
			break
		}
		params = append(params, rest[:i])
		rest = rest[i+1:]
	}

	fromNick := prefix
	if j := strings.Index(prefix, "!"); j != -1 {
		fromNick = prefix[:j]
	}

	now := time.Now()
	if !msgTime.IsZero() {
		now = msgTime
	}
	p := func(n int) string {
		if n < len(params) {
			return params[n]
		}
		return ""
	}

	switch cmd {
	case "PING":
		c.send("PONG :" + p(0))

	case "PONG":
		c.mu.Lock()
		c.lastPong = time.Now()
		c.mu.Unlock()

	case "001":
		// Sėkmingai prisijungta — atstatom backoff
		c.mu.Lock()
		c.reconnectDelay = 5 * time.Second
		c.mu.Unlock()
		if len(params) > 1 {
			c.serverMsg(strings.Join(params[1:], " "))
		}
		c.mu.Lock()
		pass := c.nickservPass
		saslUsed := c.capSASL && c.saslOk
		autoJoin := make([]string, len(c.autoJoin))
		copy(autoJoin, c.autoJoin)
		perform := make([]string, len(c.perform))
		copy(perform, c.perform)
		c.mu.Unlock()
		// Jei SASL nederėtas — naudojam SASL slaptažodį per NickServ (tas pats įgaliojimas)
		if pass == "" && !saslUsed {
			c.mu.Lock()
			pass = c.saslPass
			c.mu.Unlock()
		}
		if pass != "" {
			c.send("PRIVMSG NickServ :IDENTIFY " + pass)
			c.serverMsg("NickServ: siunčiamas IDENTIFY...")
		}
		for _, ch := range autoJoin {
			c.send("JOIN " + ch)
		}
		for _, cmd := range perform {
			c.handleInput(cmd)
		}

	case "002", "003", "004":
		if len(params) > 1 {
			c.serverMsg(strings.Join(params[1:], " "))
		}

	case "372", "375", "376":
		if len(params) > 1 {
			c.serverMsg(strings.Join(params[1:], " "))
		}

	case "JOIN":
		ch := p(0)
		c.mu.Lock()
		isMe := strings.EqualFold(fromNick, c.nick)
		channel := c.ensureCh(ch)
		channel.addNick(fromNick)
		if isMe {
			for i, channel := range c.channels {
				if strings.EqualFold(channel.name, ch) {
					c.activeIdx = i
					break
				}
			}
		}
		c.mu.Unlock()
		c.addMsg(ch, Message{ts: now, nick: fromNick, msgType: MsgJoin})

	case "PART":
		ch := p(0)
		msg := p(1)
		c.mu.Lock()
		if channel := c.findCh(ch); channel != nil {
			channel.removeNick(fromNick)
		}
		isMe := strings.EqualFold(fromNick, c.nick)
		if isMe {
			for i, channel := range c.channels {
				if strings.EqualFold(channel.name, ch) {
					c.channels = append(c.channels[:i], c.channels[i+1:]...)
					if c.activeIdx >= len(c.channels) {
						c.activeIdx = len(c.channels) - 1
					}
					break
				}
			}
		}
		c.mu.Unlock()
		if !isMe {
			c.addMsg(ch, Message{ts: now, nick: fromNick, text: msg, msgType: MsgPart})
		}

	case "QUIT":
		msg := p(0)
		c.mu.Lock()
		// Rasti kanalus kuriuose buvo nick PRIEŠ pašalinimą
		var affected []*Channel
		for _, ch := range c.channels {
			for _, n := range ch.nicks {
				if strings.EqualFold(stripMode(n), fromNick) {
					affected = append(affected, ch)
					break
				}
			}
		}
		for _, ch := range affected {
			ch.removeNick(fromNick)
		}
		c.mu.Unlock()
		for _, ch := range affected {
			c.addMsg(ch.name, Message{ts: now, nick: fromNick, text: msg, msgType: MsgQuit})
		}

	case "KICK":
		ch := p(0)
		kicked := p(1)
		reason := p(2)
		c.mu.Lock()
		me := c.nick
		if channel := c.findCh(ch); channel != nil {
			channel.removeNick(kicked)
		}
		c.mu.Unlock()
		if strings.EqualFold(kicked, me) {
			c.errMsg(fmt.Sprintf("Buvai išmestas iš %s (%s)", ch, reason))
			// Pateikti kanalą kaip neaktyvų, bet neuždaryti lango
		} else {
			c.addMsg(ch, Message{ts: now, nick: fromNick,
				text:    fmt.Sprintf("išmetė %s (%s)", kicked, reason),
				msgType: MsgServer})
		}
		c.queueRedraw()

	case "INVITE":
		// :nick!user@host INVITE testnick :#channel
		invitedTo := p(1)
		c.serverMsg(fmt.Sprintf("*** %s kviečia tave į %s (rašyk /join %s)", fromNick, invitedTo, invitedTo))
		c.queueRedraw()

	case "NICK":
		newNick := p(0)
		c.mu.Lock()
		if strings.EqualFold(fromNick, c.nick) {
			c.nick = newNick
		}
		for _, ch := range c.channels {
			ch.renameNick(fromNick, newNick)
		}
		c.mu.Unlock()
		c.serverMsg(fmt.Sprintf("%s → %s", fromNick, newNick))

	case "MODE":
		target := p(0)
		modeStr := p(1)
		// Nick mode kanaluose
		if strings.HasPrefix(target, "#") || strings.HasPrefix(target, "&") {
			c.mu.Lock()
			ch := c.findCh(target)
			if ch != nil && len(params) > 2 {
				// params[2..] = nickai kuriems taikomas mode
				adding := true
				modeIdx := 2
				for _, mr := range modeStr {
					switch mr {
					case '+':
						adding = true
					case '-':
						adding = false
					case 'o', 'v', 'h', 'q', 'a':
						if modeIdx < len(params) {
							modeChar := string(mr)
							if adding {
								modeChar = "+" + modeChar
							} else {
								modeChar = "-" + modeChar
							}
							ch.setMode(params[modeIdx], modeChar)
							modeIdx++
						}
					}
				}
			}
			c.mu.Unlock()
			modeDesc := ""
			if len(params) > 1 {
				modeDesc = strings.Join(params[1:], " ")
			}
			c.addMsg(target, Message{ts: now, text: fmt.Sprintf("Mode %s: %s", target, modeDesc), msgType: MsgServer})
			c.queueRedraw()
		}

	case "TOPIC":
		ch := p(0)
		newTopic := p(1)
		c.mu.Lock()
		if channel := c.findCh(ch); channel != nil {
			channel.topic = newTopic
		}
		c.mu.Unlock()
		c.addMsg(ch, Message{ts: now, nick: fromNick, text: "keičia temą į: " + newTopic, msgType: MsgServer})
		c.queueRedraw()

	case "PRIVMSG":
		target := p(0)
		text := p(1)
		c.mu.Lock()
		me := c.nick
		c.mu.Unlock()

		if c.isIgnored(fromNick) {
			return
		}

		if len(text) >= 2 && strings.HasPrefix(text, "\x01") && strings.HasSuffix(text, "\x01") {
			inner := text[1 : len(text)-1]
			if strings.HasPrefix(inner, "ACTION ") {
				action := inner[7:]
				dest := target
				if strings.EqualFold(target, me) {
					dest = fromNick
				}
				hl := strings.Contains(strings.ToLower(stripIRCFormat(action)), strings.ToLower(me))
				c.addMsg(dest, Message{ts: now, nick: fromNick, text: action, msgType: MsgAction, highlight: hl})
			} else if strings.HasPrefix(inner, "VERSION") {
				c.mu.Lock()
				ctcp := c.ctcpVersion
				c.mu.Unlock()
				if ctcp {
					c.send(fmt.Sprintf("NOTICE %s :\x01VERSION mircBSD %s (%s/Go)\x01", fromNick, version, osLabel()))
				}
			} else if strings.HasPrefix(inner, "PING") {
				c.send(fmt.Sprintf("NOTICE %s :\x01%s\x01", fromNick, inner))
			}
			return
		}

		dest := target
		isPM := false
		if strings.EqualFold(target, me) {
			dest = fromNick
			isPM = true
		}
		// echo-message: serveris siunčia atgal mūsų pačių žinutes — nerodyti dublikatų
		c.mu.Lock()
		isEcho := c.activeCaps["echo-message"] && strings.EqualFold(fromNick, c.nick)
		c.mu.Unlock()
		if isEcho {
			return
		}
		if isPM {
			c.mu.Lock()
			ch := c.ensureCh(dest)
			ch.isPrivate = true
			c.mu.Unlock()
		}
		plainText := stripIRCFormat(text)
		hl := isPM || containsNick(plainText, me)
		if !hl {
			c.mu.Lock()
			for _, w := range c.highlightWords {
				if strings.Contains(strings.ToLower(plainText), strings.ToLower(w)) {
					hl = true
					break
				}
			}
			c.mu.Unlock()
		}
		c.addMsg(dest, Message{ts: now, nick: fromNick, text: text, msgType: MsgChat, highlight: hl})
		// Sekti URL
		if urls := extractURLs(text); len(urls) > 0 {
			c.mu.Lock()
			c.lastURLs = append(c.lastURLs, urls...)
			if len(c.lastURLs) > 50 {
				c.lastURLs = c.lastURLs[len(c.lastURLs)-50:]
			}
			c.mu.Unlock()
		}

	case "NOTICE":
		text := p(1)
		if c.isIgnored(fromNick) {
			return
		}
		c.addMsg("*server*", Message{ts: now, nick: fromNick, text: text, msgType: MsgNotice})

	case "332":
		ch := p(1)
		topic := p(2)
		c.mu.Lock()
		if channel := c.findCh(ch); channel != nil {
			channel.topic = topic
		}
		c.mu.Unlock()
		c.addMsg(ch, Message{ts: now, text: "Tema: " + topic, msgType: MsgServer})
		c.queueRedraw()

	case "353":
		ch := p(2)
		users := p(3)
		c.mu.Lock()
		channel := c.ensureCh(ch)
		for _, u := range strings.Fields(users) {
			channel.addNick(u)
		}
		c.mu.Unlock()
		c.queueRedraw()

	case "321":
		c.serverMsg("── Kanalų sąrašas ──")
	case "322":
		ch := p(1)
		count := p(2)
		topic := stripIRCFormat(p(3))
		c.serverMsg(fmt.Sprintf("  %-30s %5s  %s", ch, count, topic))
	case "323":
		c.serverMsg("── Sąrašo pabaiga ──")

	case "366":
		// End of NAMES

	case "391": // RPL_TIME
		c.serverMsg(fmt.Sprintf("Serverio laikas [%s]: %s", p(1), p(2)))

	case "301":
		c.serverMsg(fmt.Sprintf("%s yra away: %s", p(1), p(2)))

	case "311": // RPL_WHOISUSER: nick user host * :realname
		nick, user2, host, realname := p(1), p(2), p(3), p(5)
		c.serverMsg(fmt.Sprintf("┌─[whois] %s (%s@%s) — %s", nick, user2, host, realname))
	case "312": // RPL_WHOISSERVER: nick server :info
		c.serverMsg(fmt.Sprintf("│  Serveris : %s (%s)", p(2), p(3)))
	case "313": // RPL_WHOISOPERATOR
		c.serverMsg(fmt.Sprintf("│  Operatorius: %s", p(1)))
	case "317": // RPL_WHOISIDLE: nick idle signon
		idle, _ := strconv.Atoi(p(2))
		signonTs, _ := strconv.ParseInt(p(3), 10, 64)
		idleStr := fmt.Sprintf("%dm %ds", idle/60, idle%60)
		signonStr := ""
		if signonTs > 0 {
			signonStr = "  prisijungė: " + time.Unix(signonTs, 0).Format("2006-01-02 15:04")
		}
		c.serverMsg(fmt.Sprintf("│  Idle      : %s%s", idleStr, signonStr))
	case "319": // RPL_WHOISCHANNELS
		c.serverMsg(fmt.Sprintf("│  Kanalai   : %s", p(2)))
	case "330": // RPL_WHOISACCOUNT: nick account :is logged in as
		c.serverMsg(fmt.Sprintf("│  Paskyra   : %s", p(2)))
	case "671": // RPL_WHOISSECURE
		c.serverMsg(fmt.Sprintf("│  Ryšys     : TLS (šifruotas)"))
	case "318": // RPL_ENDOFWHOIS
		c.serverMsg(fmt.Sprintf("└─[whois] %s — pabaiga", p(1)))

	case "305": // RPL_UNAWAY — serveris patvirtino away išjungimą
		c.mu.Lock()
		c.away = ""
		c.mu.Unlock()
		c.serverMsg("Away išjungtas")
		c.queueRedraw()

	case "306": // RPL_NOWAWAY — serveris patvirtino away įjungimą
		c.serverMsg("Away įjungtas")
		c.queueRedraw()

	case "431": // ERR_NONICKNAMEGIVEN
		c.errMsg("Nenurodytas slapyvardis")

	case "432": // ERR_ERRONEUSNICKNAME
		c.errMsg(fmt.Sprintf("Netinkamas slapyvardis: %s", p(1)))

	case "433":
		c.errMsg("Slapyvardis jau naudojamas!")
		c.mu.Lock()
		c.nick = c.nick + "_"
		// Neleisti nick'ui augti be galo ilgu
		for len(c.nick) > 30 {
			r := []rune(c.nick)
			c.nick = string(r[:len(r)-1])
		}
		n := c.nick
		c.mu.Unlock()
		c.send("NICK " + n)

	case "ERROR":
		msg := p(0)
		c.errMsg(msg)
		if strings.Contains(strings.ToLower(msg), "throttl") {
			c.mu.Lock()
			c.reconnectDelay = 120 * time.Second
			c.mu.Unlock()
			c.serverMsg("Throttled: laukiama 2 minutes prieš kitą bandymą")
		}

	case "CAP":
		subCmd := strings.ToUpper(p(1))
		// IRCv3 302: tęsiniams siunčiamas atskiras "*" parametras (CAP nick LS * :caps)
		cont := p(2) == "*"
		capsStr := p(2)
		if cont {
			capsStr = p(3)
		}
		switch subCmd {
		case "LS":
			// Kaupti sąrašą, kol pasiekiama paskutinė (be asterisko) eilutė
			c.mu.Lock()
			if c.capAvailable == nil {
				c.capAvailable = make(map[string]bool)
			}
			for _, cap := range strings.Fields(capsStr) {
				name := strings.ToLower(strings.SplitN(cap, "=", 2)[0])
				c.capAvailable[name] = true
			}
			if cont {
				c.mu.Unlock()
				return
			}
			available := c.capAvailable
			var wanted []string
			for _, want := range []string{"multi-prefix", "away-notify", "server-time", "echo-message"} {
				if available[want] {
					wanted = append(wanted, want)
				}
			}
			needSASL := available["sasl"] && c.saslPass != ""
			if needSASL {
				wanted = append(wanted, "sasl")
				c.capSASL = true
			}
			c.capAvailable = make(map[string]bool)
			c.mu.Unlock()
			if len(wanted) > 0 {
				c.send("CAP REQ :" + strings.Join(wanted, " "))
			} else {
				c.send("CAP END")
			}
		case "ACK":
			saslAcked := false
			c.mu.Lock()
			for _, cap := range strings.Fields(capsStr) {
				name := strings.ToLower(strings.TrimLeft(cap, "-"))
				c.activeCaps[name] = true
				if name == "sasl" {
					saslAcked = true
				}
			}
			c.mu.Unlock()
			if cont {
				return // bus daugiau ACK eilučių
			}
			if saslAcked {
				c.mu.Lock()
				c.saslPending = true
				c.mu.Unlock()
				c.send("AUTHENTICATE PLAIN")
			} else {
				c.send("CAP END")
			}
		case "NAK":
			c.send("CAP END")
		}

	case "AUTHENTICATE":
		if p(0) == "+" {
			c.mu.Lock()
			nick := c.nick
			pass := c.saslPass
			c.mu.Unlock()
			payload := base64.StdEncoding.EncodeToString([]byte("\x00" + nick + "\x00" + pass))
			c.send("AUTHENTICATE " + payload)
		}

	case "900": // RPL_LOGGEDIN
		c.serverMsg(fmt.Sprintf("SASL: prisijungta kaip %s", p(2)))

	case "903": // RPL_SASLSUCCESS
		c.serverMsg("SASL: autentifikacija sėkminga")
		c.mu.Lock()
		c.saslPending = false
		c.saslOk = true
		c.mu.Unlock()
		c.send("CAP END")

	case "902", "904", "905": // SASL nesėkmė
		c.errMsg(fmt.Sprintf("SASL klaida (%s): %s", cmd, p(len(params)-1)))
		c.mu.Lock()
		c.saslPending = false
		c.saslOk = false
		c.mu.Unlock()
		c.send("CAP END")

	case "AWAY": // away-notify cap
		if fromNick != "" {
			reason := p(0)
			if reason != "" {
				nickLow := strings.ToLower(fromNick)
				c.mu.Lock()
				last, seen := c.awayNotified[nickLow]
				if !seen || now.Sub(last) >= 10*time.Minute {
					c.awayNotified[nickLow] = now
					active := c.channels[c.activeIdx]
					c.mu.Unlock()
					for _, n := range active.nicks {
						if strings.EqualFold(stripMode(n), fromNick) {
							c.addMsg(active.name, Message{ts: now, nick: fromNick,
								text:    fromNick + " yra away: " + reason,
								msgType: MsgServer})
							break
						}
					}
				} else {
					c.mu.Unlock()
				}
			} else {
				// Nick grįžo - išvalome throttle ir rodome pranešimą aktyvame kanale
				nickLow := strings.ToLower(fromNick)
				c.mu.Lock()
				_, wasAway := c.awayNotified[nickLow]
				delete(c.awayNotified, nickLow)
				active := c.channels[c.activeIdx]
				c.mu.Unlock()
				if wasAway {
					for _, n := range active.nicks {
						if strings.EqualFold(stripMode(n), fromNick) {
							c.addMsg(active.name, Message{ts: now, nick: fromNick,
								text:    fromNick + " grįžo iš away",
								msgType: MsgServer})
							break
						}
					}
				}
			}
		}

	default:
		if len(params) > 1 {
			c.serverMsg(fmt.Sprintf("[%s] %s", cmd, strings.Join(params[1:], " ")))
		}
	}
}

func containsNick(text, nick string) bool {
	if nick == "" {
		return false
	}
	textLow := strings.ToLower(text)
	nickLow := strings.ToLower(nick)
	idx := strings.Index(textLow, nickLow)
	if idx == -1 {
		return false
	}
	before := idx == 0 || !isNickChar(rune(textLow[idx-1]))
	after := idx+len(nickLow) >= len(textLow) || !isNickChar(rune(textLow[idx+len(nickLow)]))
	return before && after
}

func extractURLs(text string) []string {
	var urls []string
	for _, word := range strings.Fields(stripIRCFormat(text)) {
		word = strings.TrimRight(word, ".,!?)")
		if strings.HasPrefix(word, "http://") || strings.HasPrefix(word, "https://") || strings.HasPrefix(word, "ftp://") {
			urls = append(urls, word)
		}
	}
	return urls
}

func isNickChar(r rune) bool {
	// RFC nick simboliai: raidės, skaičiai ir specialieji `[]\_^{|}-
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') ||
		strings.ContainsRune("_-[]\\`^{}|", r)
}

// isValidNick tikrina nick pagal IRC taisykles: leistini tik RFC nick simboliai
// (raidės, skaičiai, _ - [ ] \ ` ^ { } |), max 30 ženklų. Čia ne tik apsauga nuo
// protokolo klaidų, bet ir nuo valdymo simbolių (pvz. ESC) patekimo į serverį.
func isValidNick(nick string) bool {
	if nick == "" || len(nick) > 30 {
		return false
	}
	for _, r := range nick {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("_-[]\\`^{}|", r):
		default:
			return false
		}
	}
	return true
}

// ---- Sustabdymas ----

var (
	stopCh   = make(chan struct{})
	stopOnce sync.Once
)

func (c *Client) stop() {
	// stop() gali būti kviečiamas ir iš /quit, ir iš signalo — uždarome tik kartą
	stopOnce.Do(func() {
		c.saveHistory()
		c.closeLogs()
		close(stopCh)
	})
}

// ---- Main ----

func main() {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		fmt.Fprintln(os.Stderr, "Reikalingas terminalas (TTY)")
		os.Exit(1)
	}

	oldState, err := term.MakeRaw(fd)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Nepavyko nustatyti raw terminalo:", err)
		os.Exit(1)
	}

	cfg := loadConfig(configPath())
	setTSColor(cfg.TimestampColor)

	args := os.Args[1:]
	if len(args) >= 2 {
		cfg.Server = args[0]
		cfg.Port = args[1]
		if len(args) >= 3 {
			cfg.Nick = args[2]
		}
		if len(args) >= 4 && args[3] == "tls" {
			cfg.TLS = true
		}
	}

	c := newClient(cfg)
	c.oldState = oldState

	c.loadHistory()
	c.loadIgnore()

	w, h, _ := term.GetSize(fd)
	c.width, c.height = w, h

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	go func() {
		for range sigCh {
			w, h, _ := term.GetSize(fd)
			c.mu.Lock()
			c.width, c.height = w, h
			c.mu.Unlock()
			c.queueRedraw()
		}
	}()

	intCh := make(chan os.Signal, 1)
	signal.Notify(intCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-intCh
		c.stop()
	}()

	fmt.Print("\033[2J\033[H")
	c.serverMsg(fmt.Sprintf("Sveiki! mircBSD %s - IRC klientas (%s)", version, osLabel()))
	if cp := configPath(); cp != "" {
		c.serverMsg(fmt.Sprintf("Config: %s", cp))
	} else {
		c.errMsg("Nepavyko nustatyti konfigūracijos kelio (naudotojo katalogas nerastas)")
	}
	if cfg.Server != "" {
		c.serverMsg(fmt.Sprintf("Serveris: %s:%s (TLS:%v, reconnect:%v)", cfg.Server, cfg.Port, cfg.TLS, cfg.Reconnect))
		go c.connectLoop()
	} else {
		c.serverMsg("Naudojimas: /connect <serveris> <portas> [nick] [tls]")
		c.serverMsg("Pavyzdys:   /connect irc.libera.chat 6697 manick tls")
	}
	c.serverMsg("/help - komandų sąrašas  |  Ctrl+R - paieška")

	c.draw()
	go c.readKeys()

	for {
		select {
		case <-stopCh:
			term.Restore(fd, oldState)
			fmt.Print(ansiShowCur + "\033[2J\033[H")
			fmt.Println("Viso gero!")
			return
		case <-c.redraw:
			c.draw()
		}
	}
}
