package main

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/term"
)

// ---- IRC Client ----

// maxLineBytes – IRC 512 eilutės baitų limitas; PRIVMSG atgaliniam atžvilgiui
// (antraštė + CRLF + saugumo suma) palieka tiek baitų tekstui.
const maxLineBytes = 512

// ircSendBurst – didžiausias eilučių skaičius per sekundę išėjimui (flood apsauga).
const ircSendBurst = 5

// Serverio protokolo parametrai (ISUPPORT/005) — numatytieji RFC reikšmės,
// atnaujinamos c.applyISUPPORT(). Vienas klientas viename procese — paketo
// lygmeniu, lygiai kaip tsColorCode.
var (
	chantypes   = "#&"      // kanalų pavadinimų prefiksai
	prefixSigns = "~&@%+"   // nick'o statuso prefiksai pagal svarbą (q a o h v)
	prefixModes = "qaohv"   // mode raidės, atitinkančios prefixSigns
	casemapping = "rfc1459" // nick palyginimų taisyklės
)

// applyISUPPORT nuskaiko ISUPPORT (005) tokeno: CHANTYPES=, PREFIX=?, CASEMAPPING=.
func (c *Client) applyISUPPORT(tokens []string) {
	for _, tok := range tokens {
		switch {
		case strings.HasPrefix(tok, "CHANTYPES="):
			v := tok[len("CHANTYPES="):]
			if v != "" {
				chantypes = strings.Trim(v, "-") // "-" reiškia "kanalų nėra"
				if chantypes == "" {
					chantypes = "#&"
				}
			}
		case strings.HasPrefix(tok, "PREFIX="):
			v := tok[len("PREFIX="):]
			if i := strings.IndexByte(v, ')'); i > 0 {
				modes, signs := v[1:i], v[i+1:]
				if modes != "" && len([]rune(modes)) == len([]rune(signs)) {
					prefixModes = modes
					prefixSigns = signs
				}
			}
		case strings.HasPrefix(tok, "CASEMAPPING="):
			switch tok[len("CASEMAPPING="):] {
			case "ascii", "rfc1459", "strict-rfc1459":
				casemapping = tok[len("CASEMAPPING="):]
			}
		}
	}
}

// lowerIRC RFC 1459 casemapping (A–Z ir []\^ ↔ {}|~).
func lowerIRC(r rune) rune {
	switch r {
	case '[':
		return '{'
	case ']':
		return '}'
	case '\\':
		return '|'
	case '^':
		return '~'
	}
	if r >= 'A' && r <= 'Z' {
		return r + ('a' - 'A')
	}
	return r
}

// nickEqual palygina nick'us pagal serverio casemapping
// (RFC: nick'yra didžiųjų/mažųjų raidžių nejausmi, bet [] != {} pagal ASCII).
func nickEqual(a, b string) bool {
	if a == "" || b == "" {
		return a == b
	}
	if casemapping == "ascii" {
		return strings.EqualFold(a, b)
	}
	return strings.Map(lowerIRC, a) == strings.Map(lowerIRC, b)
}

// isChannel arba pavadinimas yra kanalas pagal serverio CHANTYPES.
// Ypatingieji "*server*" — ne kanalas.
func isChannel(name string) bool {
	if name == "" {
		return false
	}
	return strings.ContainsRune(chantypes, rune(name[0]))
}

// splitForPrivmsg dalija tekstą į dalis, kurios kiekviena po PRIVMSG antraštės
// tilptų į IRC 512 baitų eilutę. Dalyti reikia baitais (ne rune'ais), nes
// UTF-8 lietuviškos raidės yra po 2 baitus — kitaip žinutė viršytų limitą.
// Neperšaukia UTF-8 simbolio prie ribos.
func splitForPrivmsg(target, text string) []string {
	budget := maxLineBytes - len("PRIVMSG "+target+" :") - 2 - 45 // 45 = papildomos serverio/bouncer antraštės suma
	if budget < 100 {
		budget = 100
	}
	var parts []string
	for len(text) > budget {
		cut := budget
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut-- // nepjaunam viduryje simbolio
		}
		parts = append(parts, text[:cut])
		text = text[cut:]
	}
	parts = append(parts, text)
	return parts
}

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
	connectingGen  int // jungiamojo ciklo epocha; -1 = nejungiama
	ctcpVersion    bool
	notify         bool
	mouse          bool
	msgAreaTop     int
	lastNotify     time.Time

	// IRCv3 CAP / SASL
	activeCaps   map[string]bool
	capSASL      bool
	saslOk       bool // SASL autentifikacija sėkminga (903)
	saslPending  bool
	nickAttempts int             // 433 bandymų skaitliukas (nick_, nick2, nick3)
	connectGen   int             // jungties epocha: /connect ir /reconnect padidina, senos goroutines pasitraukia
	capAvailable map[string]bool // CAP LS sąrašo kaupimas (tęsinio eilutėms)

	// URL istorija
	lastURLs []string

	highlightWords []string // papildomi highlight žodžiai

	// Away throttle: nick -> paskutinio pranešimo laikas
	awayNotified map[string]time.Time

	// Paste apsauga
	pasteBuf          []string
	pastePartial      string    // nevienos eilutės gabaliukas (paste ateina ~64B gabaliukais)
	pasteCollectUntil time.Time // kol paste „teka" iš terminalo — renkame, neconfirm

	// Ping keepalive
	lastPong   time.Time
	pingTicker *time.Ticker

	channels    []*Channel
	activeIdx   int
	maxMessages int
	lastDrawn   *Channel // kuriuo kanalu paskutinį kartą braižyta (istorijos užšaldymui)

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
		notify:          cfg.Notifications,
		mouse:           cfg.Mouse,
		connectingGen:   -1, // "nejungiama"; 0 = epocha 0 jau „vyksta“ (netinkama pradžiai)
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

// isIgnored tikrina arba šaltinio `prefix` (nick!user@host) atitinka ignore
// sąrašą. Palaikomos ir paprastos nick'ų eilutės (senas elgesys), ir wildcards
// (*, ?) maskės: "gaidys!*@*", "*!*@sraunus.host.tld", "bot*".
func (c *Client) isIgnored(prefix string) bool {
	if len(c.ignoreList) == 0 {
		return false
	}
	nick := prefix
	if i := strings.IndexByte(prefix, '!'); i != -1 {
		nick = prefix[:i]
	}
	for _, ig := range c.ignoreList {
		if ig == "" {
			continue
		}
		if !strings.ContainsAny(ig, "*?") {
			// tiesioginis nick arba atitinkama eilutė
			if nickEqual(ig, nick) {
				return true
			}
			continue
		}
		// wildcard maskė — atitinka pilnąjį prefixą arba pagal nick'o dalį
		if wildcardMatch(ig, prefix) || wildcardMatch(ig, nick) {
			return true
		}
	}
	return false
}

// wildcardMatch — paprastas * ir (ne-regexp) matching (case-insensitive).
func wildcardMatch(pattern, s string) bool {
	return wildEq(strings.ToLower(pattern), strings.ToLower(s))
}

func wildEq(p, s string) bool {
	// iteratyvus * / ? matching su backtracking per paskutinį * (be rekursijos)
	pi, si := 0, 0
	star := -1
	siSave := 0
	for si < len(s) {
		switch {
		case pi < len(p) && p[pi] == '*':
			star = pi
			siSave = si
			pi++ // * atitinka ir tuščią dalį
		case pi < len(p) && (p[pi] == '?' || p[pi] == s[si]):
			pi++
			si++
		case star != -1:
			pi = star + 1
			siSave++
			si = siSave
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
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
	if m.highlight && !isActive && c.notify {
		// Desktop pranešimas: atsargiai su laiku, kad nespaustų per greitai
		c.mu.Lock()
		canNotify := time.Since(c.lastNotify) > 3*time.Second
		if canNotify {
			c.lastNotify = time.Now()
		}
		c.mu.Unlock()
		if canNotify {
			title := chanName
			if !isChannel(chanName) {
				title = "PM " + chanName
			}
			body := stripIRCFormat(m.text)
			if m.nick != "" {
				body = "<" + m.nick + "> " + body
			}
			go notifyDesktop(title, body)
		}
	}
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
	// Rotacija per dieną: kiekviena para — savo failas
	// (kanalas-2026-09-29.log); senas failas uždaromas pradedant naują.
	key := server + "/" + chanName + "/" + m.ts.Format("2006-01-02")
	c.mu.Lock()
	f, ok := c.logFiles[key]
	if !ok {
		dir := filepath.Join(logDir, sanitizePath(server))
		if err := os.MkdirAll(dir, 0700); err != nil {
			c.mu.Unlock()
			return
		}
		// uždarome ankstesnės dienos failą tos pačios kanalo eilutės
		oldPrefix := server + "/" + chanName + "/"
		for otherKey, otherF := range c.logFiles {
			if strings.HasPrefix(otherKey, oldPrefix) {
				otherF.Close()
				delete(c.logFiles, otherKey)
			}
		}
		path := filepath.Join(dir, sanitizePath(chanName)+"-"+m.ts.Format("2006-01-02")+".log")
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

// clearLogs išvalo log failus: numatytai visi dabartinio serverio kanalai,
// su /clearlogs <kanalas> — tik to kanalo. Grąžina pašalintų failų skaičių.
func (c *Client) clearLogs(chanName string) int {
	c.mu.Lock()
	dir := c.logDir
	server := c.server
	// Uždarom atidarytus handle'us — vengiant rašymo į jau ištrintus failus
	prefix := ""
	if chanName != "" {
		// raktas: server/kanalas/data
		prefix = server + "/" + chanName + "/"
	}
	closed := 0
	for k, f := range c.logFiles {
		if prefix == "" || strings.HasPrefix(k, prefix) {
			f.Close()
			delete(c.logFiles, k)
			closed++
		}
	}
	c.mu.Unlock()

	if dir == "" || server == "" {
		return 0
	}
	srvDir := filepath.Join(dir, sanitizePath(server))
	entries, err := os.ReadDir(srvDir)
	if err != nil {
		return 0
	}
	filePrefix := sanitizePath(chanName) // tuščias = visi šio serverio failai
	removed := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if filePrefix != "" && !strings.HasPrefix(e.Name(), filePrefix+"-2") && e.Name() != filePrefix+".log" {
			continue
		}
		if os.Remove(filepath.Join(srvDir, e.Name())) == nil {
			removed++
		}
	}
	return removed
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

// startConnect nutraukia senąjį ryšį ir pakuria naują jungimo ciklą.
// connectGen padidinimas: senos connectLoop/doConnect goroutines tikrindamos
// generaciją pasitraukia (be to read() cikle pasibaigia nuo conn.Close()).
func (c *Client) startConnect() {
	c.mu.Lock()
	c.connectingGen = -1 // senas ciklas pasitrauks per generaciją
	c.connectGen++
	if c.conn != nil {
		c.conn.Close()
	}
	c.mu.Unlock()
	go c.connectLoop()
}

// connectLoop - prisijungimas su automatiniais bandymais iš naujo
func (c *Client) connectLoop() {
	c.mu.Lock()
	gen := c.connectGen
	if c.connectingGen == gen {
		c.mu.Unlock()
		return // jau jungiamasi (ta pati epocha)
	}
	c.connectingGen = gen
	c.reconnectDelay = 5 * time.Second
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		// Atstatom tik jei niekas nepakeitė epochos po mūsų
		if c.connectingGen == gen {
			c.connectingGen = -1
		}
		c.mu.Unlock()
	}()

	for {
		c.doConnect(gen)

		c.mu.Lock()
		staleGen := c.connectGen != gen
		shouldReconnect := c.reconnect
		delay := c.reconnectDelay
		// Eksponentinis atsitraukimas
		c.reconnectDelay *= 2
		if c.reconnectDelay > 120*time.Second {
			c.reconnectDelay = 120 * time.Second
		}
		c.mu.Unlock()

		if staleGen {
			return
		}
		if !shouldReconnect {
			return
		}

		c.serverMsg(fmt.Sprintf("Jungiamasi iš naujo po %v...", delay))
		c.queueRedraw()
		// Sleep nutrauktas per epochos pakeitimą: žr. sleepReconnect
		if !c.sleepReconnect(delay, gen) {
			return
		}
	}
}

// sleepReconnect laukia delay, bet anksčiau pasitraukia, kai pakeičiama epocha.
func (c *Client) sleepReconnect(delay time.Duration, gen int) bool {
	t := time.NewTimer(delay)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			c.mu.Lock()
			stale := c.connectGen != gen
			c.mu.Unlock()
			return !stale
		case <-time.After(50 * time.Millisecond):
			c.mu.Lock()
			stale := c.connectGen != gen
			c.mu.Unlock()
			if stale {
				return false
			}
		}
	}
}

func (c *Client) doConnect(gen int) {
	c.mu.Lock()
	srvName := c.server
	srv := c.server + ":" + c.port
	useTLS := c.useTLS
	nick := c.nick
	staleGen := c.connectGen != gen
	c.mu.Unlock()
	if staleGen {
		return
	}

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
	if c.connectGen != gen {
		// Nuo dial pradžios buvo /connect / /reconnect — ši jungtis nebeaktuali
		c.mu.Unlock()
		conn.Close()
		return
	}
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

	// Write goroutine — susieta su šia jungtimi per writeStop.
	// Flood apsauga (išėjimo pusė): ≤5 eilutės/sek. su 10 eilučių „burst";
	// viršijus — palaukiama sekundės pertekliui (apsaugo nuo Excess Flood
	// kick/ban ir nuo serverio išmetimo daugiaeilio pasta metu).
	go func() {
		buf := bufio.NewWriter(conn)
		sent := 0 // eilutės šioje sekundėje
		t0 := time.Now()
		for {
			select {
			case line, ok := <-newWriteCh:
				if !ok {
					return
				}
				if now := time.Now(); now.Sub(t0) >= time.Second {
					sent = 0
					t0 = now
				}
				if sent >= ircSendBurst {
					time.Sleep(time.Second - time.Since(t0))
					sent = 0
					t0 = time.Now()
				}
				sent++
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
		// Sėkmingai prisijungta — atstatom backoff ir nick bandymų skaitliuką
		c.mu.Lock()
		c.reconnectDelay = 5 * time.Second
		c.nickAttempts = 0
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

	case "002", "003":
		if len(params) > 1 {
			c.serverMsg(strings.Join(params[1:], " "))
		}

	case "004":
		if len(params) > 1 {
			c.serverMsg(strings.Join(params[1:], " "))
		}

	case "005": // ISUPPORT — serverio parametrai (CHANTYPES, PREFIX, CASEMAPPING...)
		if len(params) > 1 {
			c.applyISUPPORT(params[1:])
			c.serverMsg(strings.Join(params[1:], " "))
		}

	case "372", "375", "376":
		if len(params) > 1 {
			c.serverMsg(strings.Join(params[1:], " "))
		}

	case "JOIN":
		ch := p(0)
		c.mu.Lock()
		isMe := nickEqual(fromNick, c.nick)
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
		isMe := nickEqual(fromNick, c.nick)
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
				if nickEqual(stripMode(n), fromNick) {
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
		if nickEqual(kicked, me) {
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
		if nickEqual(fromNick, c.nick) {
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
		if isChannel(target) {
			c.mu.Lock()
			ch := c.findCh(target)
			if ch != nil && len(params) > 2 {
				// params[2..] = argumentai mode raidėms. Privalime skaičiuoti
				// VISUS mode, kurie imasi argumento (o/v/h/q/a nickams; b, k,
				// l, e, I... irgi turi parametrą) — kitaip indeksas „nuklysta“
				// (pvz. MODE #chan +kb nick mask).
				adding := true
				modeIdx := 2
				argLetters := prefixModes + "blkeI"
				for _, mr := range modeStr {
					switch {
					case mr == '+':
						adding = true
					case mr == '-':
						adding = false
					case strings.IndexRune(argLetters, mr) >= 0:
						if modeIdx < len(params) {
							// tik nick'ų statuso modas keičia nick'o prefixą
							if i := strings.IndexRune(prefixModes, mr); i >= 0 && i < len(prefixSigns) {
								pfx := string('-')
								if adding {
									pfx = string('+')
								}
								ch.setMode(params[modeIdx], pfx+string(mr))
							}
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

		if c.isIgnored(prefix) {
			return
		}

		if len(text) >= 2 && strings.HasPrefix(text, "\x01") && strings.HasSuffix(text, "\x01") {
			inner := text[1 : len(text)-1]
			if strings.HasPrefix(inner, "ACTION ") {
				action := inner[7:]
				dest := target
				if nickEqual(target, me) {
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
		if nickEqual(target, me) {
			dest = fromNick
			isPM = true
		}
		// echo-message: serveris siunčia atgal mūsų pačių žinutes — nerodyti dublikatų
		c.mu.Lock()
		isEcho := c.activeCaps["echo-message"] && nickEqual(fromNick, c.nick)
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
		if c.isIgnored(prefix) {
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
		var cand string
		// Bandymų seka: nick_, nick2, nick3 – niekada nesimato ilgesnis kaip 30
		if c.nickAttempts < 3 {
			base := strings.TrimRight(c.nick, "_0123456789")
			if base == "" {
				base = strings.Repeat("n", 10)
			}
			c.nickAttempts++
			if c.nickAttempts == 1 {
				cand = base + "_"
			} else {
				cand = base + strconv.Itoa(c.nickAttempts)
			}
			if r := []rune(cand); len(r) > 30 {
				cand = string(r[:30])
			}
			c.nick = cand
		}
		n := c.nick
		gaveUp := c.nickAttempts >= 3 && cand == ""
		c.mu.Unlock()
		if gaveUp {
			c.errMsg("Paskutinis bandymas baigtas keisti nick po 433 – jungiantis rankiniu būdu /nick")
			break
		}
		c.serverMsg(fmt.Sprintf("Bandomas nick: %s", n))
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
		// Paskutinis parametras yra žinutė, bet jei eilutė sugadinta — p(0)
		msg := p(0)
		if len(params) > 1 {
			msg = params[len(params)-1]
		}
		c.errMsg(fmt.Sprintf("SASL klaida (%s): %s", cmd, msg))
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
						if nickEqual(stripMode(n), fromNick) {
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
						if nickEqual(stripMode(n), fromNick) {
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
