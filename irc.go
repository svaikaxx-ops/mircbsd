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

	"golang.org/x/term"
)

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
