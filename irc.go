package main

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/term"
)

// ---- IRC Client ----

// maxLineBytes – IRC 512 eilutės baitų limitas; PRIVMSG atgaliniam atžvilgiui
// (antraštė + CRLF + saugumo suma) palieka tiek baitų tekstui.
const maxLineBytes = 512

// ircSendBurst – didžiausias eilučių skaičius per sekundę išėjimui (flood apsauga).
const ircSendBurst = 5

// maxCTCPBytes – didžiausias CTCP atspindžio (PING) dydis baitais. Be ribos
// užpuolėjas galėtų priversti serverį gauti tiek pat atspindą.
const maxCTCPBytes = 64

// Serverio protokolo parametrai (ISUPPORT/005) — numatytieji RFC reikšmės,
// atnaujinamos c.applyISUPPORT().
//
// Parametrai yra PER SERVERĮ (Client laukuose), ne paketinio lygio globaliai:
// vienas serveris neturi teisės gadinti kito nustatymų (ir testų —
// viena būsena turi būti izoliuota nuo kitų).
//
// ISUPPORT keičiamas parseIRC gorutinoje (005 numeriu), o skaitomas
// iš kitų gorutinių (UI braižymas, klaviatūra). Todėl apsaugotas
// sync.RWMutex (Client lauke): skaitytojai ima RLock, applyISUPPORT — Lock.
//
// SKAITYTOJAI NEGALI TIESIOGINIAI LIEPTI SIEMS KINTAMIESIEMS — naudok
// snapshot'ą (žr. c.isupSnapshot), kad matytų nuoseklų vaizdą.

// isupSnapshot grąžina nuoseklų ISUPPORT parametrų kopiją.
//
// NB: negalima naudoti named return kintamųjų (chantypes, prefixSigns, ...)
// — jie uždengtų laukus ir funkcija grąžintų tuščias reikšmes.
func (c *Client) isupSnapshot() (string, string, string, string, string) {
	c.isupMu.RLock()
	defer c.isupMu.RUnlock()
	return c.chantypes, c.prefixSigns, c.prefixModes, c.casemapping, c.chanModes
}

// applyISUPPORT nuskaiko ISUPPORT (005) tokeno: CHANTYPES=, PREFIX=?, CASEMAPPING=.
func (c *Client) applyISUPPORT(tokens []string) {
	c.isupMu.Lock()
	defer c.isupMu.Unlock()
	for _, tok := range tokens {
		switch {
		case strings.HasPrefix(tok, "CHANTYPES="):
			v := tok[len("CHANTYPES="):]
			if v != "" {
				c.chantypes = strings.Trim(v, "-") // "-" reiškia "kanalų nėra"
				if c.chantypes == "" {
					c.chantypes = "#&"
				}
			}
		case strings.HasPrefix(tok, "PREFIX="):
			v := tok[len("PREFIX="):]
			if i := strings.IndexByte(v, ')'); i > 0 {
				modes, signs := v[1:i], v[i+1:]
				if modes != "" && len([]rune(modes)) == len([]rune(signs)) {
					c.prefixModes = modes
					c.prefixSigns = signs
				}
			}
		case strings.HasPrefix(tok, "CHANMODES="):
			// CHANMODES formatas: <tipai su argumentu>,<tipai su argumentu>,<tipai be argumento>
			// pvz. "eIbq,k,flj,CFLMPQScgimnprstz"
			c.chanModes = tok[len("CHANMODES="):]
		case strings.HasPrefix(tok, "CASEMAPPING="):
			switch tok[len("CASEMAPPING="):] {
			case "ascii", "rfc1459", "strict-rfc1459":
				c.casemapping = tok[len("CASEMAPPING="):]
			}
		case strings.HasPrefix(tok, "NETWORK="):
			// Serverio tinklo pavadinimas (pvz. „Libera.Chat“). Naudojamas
			// tik informacijai — pats serveris lieka adresu.
			c.network = tok[len("NETWORK="):]
		case strings.HasPrefix(tok, "NICKLEN="):
			c.limits.NickLen = atoiDefault(tok[len("NICKLEN="):], c.limits.NickLen)
		case strings.HasPrefix(tok, "CHANNELLEN="):
			c.limits.ChannelLen = atoiDefault(tok[len("CHANNELLEN="):], c.limits.ChannelLen)
		case strings.HasPrefix(tok, "TOPICLEN="):
			c.limits.TopicLen = atoiDefault(tok[len("TOPICLEN="):], c.limits.TopicLen)
		case strings.HasPrefix(tok, "KICKLEN="):
			c.limits.KickLen = atoiDefault(tok[len("KICKLEN="):], c.limits.KickLen)
		case strings.HasPrefix(tok, "MODES="):
			c.limits.Modes = atoiDefault(tok[len("MODES="):], c.limits.Modes)
		}
	}
}

// isupLimits — serverio ribos (005). Nulyba reiškia „serveris nepranešė“
// (naudojamos standartinės reikšmės).
type isupLimits struct {
	NickLen    int
	ChannelLen int
	TopicLen   int
	KickLen    int
	Modes      int
}

// atoiDefault konvertuoja skaičių; neteisinga reikšmė → numatytoji.
func atoiDefault(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return def
	}
	return n
}

// defaultChanModes — standartinės CHANMODES, kai serveris jų nepaskyrė.
// RFC 2811: "eIbq,k,flj,CFLMPQScgimnprstz".
const defaultChanModes = "eIbq,k,flj,CFLMPQScgimnprstz"

// chanModesArgLetters grąžina modas, kurios IMASI ARGUMENTĄ.
//
// CHANMODES formatas: <su argumentu>,<su argumentu>,<be argumento>.
// Grąžinama visų trijų dalių raidžių junginys — todėl indeksai nesulaužo
// nežinant tikslaus serverio CHANMODES.
//
// Fallback: jei serveris CHANMODES nepaskyrė, naudojamas standartinis
// RFC 2811 rinkinys.
func chanModesArgLetters(chanModes, prefixModes string) string {
	if chanModes == "" {
		chanModes = defaultChanModes
	}
	parts := strings.Split(chanModes, ",")
	var sb strings.Builder
	// Pirma dalis — visada su argumentu. Antra — su argumentu.
	// Trečia — be argumento (jos NEĄTRAUKIAME).
	for i, p := range parts {
		if i >= 2 {
			break
		}
		sb.WriteString(p)
	}
	// Nick'ų statuso modos (PREFIX) taip pat imasi argumentą (nick'ą).
	sb.WriteString(prefixModes)
	return sb.String()
}

// handleCapNew apdoroja serverio CAP NEW pasiulymą.
//
// Kviečia CAP REQ su mumis pageidaujamais cap'ais arba CAP END, jei
// nieko nereikia.
func (c *Client) handleCapNew(capsStr string) {
	c.mu.Lock()
	if c.capAvailable == nil {
		c.capAvailable = make(map[string]bool)
	}
	for _, cap := range strings.Fields(capsStr) {
		name := strings.ToLower(strings.SplitN(cap, "=", 2)[0])
		c.capAvailable[name] = true
	}
	available := c.capAvailable
	var wanted []string
	for _, want := range []string{"multi-prefix", "away-notify", "server-time", "echo-message"} {
		if available[want] {
			// Tik jei dar neaktyvus.
			if !c.activeCaps[want] {
				wanted = append(wanted, want)
			}
		}
	}
	needSASL := available["sasl"] && c.saslPass != "" && !c.saslOk
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
func (c *Client) nickEqual(a, b string) bool {
	_, _, _, cm, _ := c.isupSnapshot()
	return nickEqual(a, b, cm)
}

// nickEqual — laisva versija su casemapping parametru (naudojama
// kanale, kur nėra prieigos prie Client).
func nickEqual(a, b, cm string) bool {
	if a == "" || b == "" {
		return a == b
	}
	if cm == "ascii" {
		return strings.EqualFold(a, b)
	}
	return strings.Map(lowerIRC, a) == strings.Map(lowerIRC, b)
}

// isChannel arba pavadinimas yra kanalas pagal serverio CHANTYPES.
// Ypatingieji "*server*" — ne kanalas.
func (c *Client) isChannel(name string) bool {
	if name == "" {
		return false
	}
	ct, _, _, _, _ := c.isupSnapshot()
	return strings.ContainsRune(ct, rune(name[0]))
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
	serverPass   string // serverio slaptažodis (PASS komanda)
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

	// WHO/WHOIS atsakymų adresatas. Numerikai (352/315, 311–318) atėksta
	// be žinios, kurioje vietoje vartotojas juos užsakė — anksčiau visi
	// šie atsakymai eidavo į *server* langą, todėl kanale, kuriame buvo
	// užklausta, jų nebuvo matoma. Čia saugome kanalo, kuriame užklausta
	// (jei tai buvo kanalas), kad atsakymai grįžtų į TEN.
	whoChan   string
	whoisChan string

	// Serverio protokolo parametrai (ISUPPORT/005) — PER SERVERĮ.
	// Gydoma isupMu: skaitytojai RLock, applyISUPPORT Lock.
	isupMu      sync.RWMutex
	chantypes   string     // kanalų pavadinimų prefiksai
	prefixSigns string     // nick'o statuso prefiksai pagal svarbą (q a o h v)
	prefixModes string     // mode raidės, atitinkančios prefixSigns
	casemapping string     // nick palyginimų taisyklės
	chanModes   string     // CHANMODES: "eIbq,k,flj,CFLMPQScgimnprstz"
	network     string     // NETWORK — serverio tinklo pavadinimas
	limits      isupLimits // ribos: NICKLEN, CHANNELLEN, TOPICLEN, KICKLEN, MODES

	// Paste apsauga
	pasteBuf          []string
	pastePartial      string    // nevienos eilutės gabaliukas (paste ateina ~64B gabaliukais)
	pasteCollectUntil time.Time // kol paste „teka" iš terminalo — renkame, neconfirm

	// Ping keepalive
	lastPong time.Time

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
	chanScrollOff int // kanalų sąrašo slinktis (sidebar'ui)

	// chaseUntil: kol galioja, naujos žinutės NEUŽŠALDO vaizdo (istorijos
	// užšaldymas praleidžiamas) — naudotojas paskutiniu metu slinko žemyn,
	// todėl jo slinktis turi nugalėti ateinančias žinutes.
	chaseUntil time.Time

	history    []string
	historyIdx int

	tabCandidates []string
	tabIdx        int
	tabPrefix     string

	keysDebug bool

	// Paieška
	searchMode      bool
	searchQuery     string
	searchResultIdx int // paskutinis rastas indeksas (-1 = nerasta)
	lastSearchQuery string

	logDir   string
	logFiles map[string]*os.File

	// Žurnalų eilė: žinutės įrašomos VIENOS gorutinos, todėl faile
	// eilės yra tokia pačia tvarka, kokia atėjo į klientą. Anksčiau
	// kiekvienai žinutei buvo paleidžiama atskira gorutina (`go
	// logMessage`), todėl lygiagrečios žinutės rašydavo failą
	// beveik atsitiktine tvarka.
	logQueue chan logEntry
	logStop  chan struct{}
	logOnce  sync.Once
}

// logEntry — viena eilutė žurnalo eilutėje.
type logEntry struct {
	logDir   string
	server   string
	chanName string
	msg      Message
}

func newClient(cfg Config) *Client {
	// Serverio pavadinimas iš konfigūracijos / komandinės eilutės taip pat
	// turi būti saugus — jis įrašomas į IRC komandas ir žurnalų katalogą.
	server := cfg.Server
	if !isValidServer(server) {
		server = ""
	}
	// Nick'o validacija — čia, kad VISI įėjimo keliai (config, komandinė
	// eilutė) būtų patikrinti viename vietoje. Anksčiau tik /connect tikrino
	// nick'ą, o CLI `mircbsd srv 6667 "blogas nick"` siųsdavo tokį nick'ą
	// į serverį.
	nick := cfg.Nick
	if !isValidNick(nick) {
		nick = "mircbsd"
	}
	// Portas — tik skaičius 1–65535; blogas portas turi būti atmestas, ne
	// perduodamas į adresą.
	port := cfg.Port
	if !isValidPort(port) {
		port = "6667"
	}
	c := &Client{
		nick:            nick,
		server:          server,
		port:            port,
		useTLS:          cfg.TLS,
		realName:        cfg.RealName,
		nickservPass:    cfg.NickServ,
		saslPass:        cfg.SASLPass,
		serverPass:      cfg.ServerPass,
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
		logQueue:        make(chan logEntry, 1024),
		logStop:         make(chan struct{}),
		activeCaps:      make(map[string]bool),
		awayNotified:    make(map[string]time.Time),
		lastPong:        time.Now(),
		// ISUPPORT numatytosios reikšmės (RFC) — per serverį
		chantypes:   "#&",
		prefixSigns: "~&@%+",
		prefixModes: "qaohv",
		casemapping: "rfc1459",
	}
	return c
}

// findCh ieško kanalo pagal pavadinimą. Lyginama su SANITIZUOTU pavadinimu —
// nes kanalui sukūrimas buvo sanitizuotas, serverio atsiųstas originalus
// pavadinimas turi būti išvalomas prieš palyginimą.
func (c *Client) findCh(name string) *Channel {
	safe := sanitizeText(name)
	for _, ch := range c.channels {
		if strings.EqualFold(ch.name, safe) {
			return ch
		}
	}
	return nil
}

// maxChannels – didžiausias kanalų (langų) skaičius viename seanse.
// Be ribos priešiškas serveris galėtų siųsti JOIN/PM flood — klientas
// kurstų šimtus kanalų (atmintis + sidebar).
const maxChannels = 100

// ensureCh sukuria kanalą, jei jo nėra. Tuščių pavadinimų nepriima —
// serverio komanda be kanalo parametro (pvz. „:srv 332 :topic“) neturi
// teisės sukurti kanalo su tuščiu pavadinimu.
//
// Pavadinimas sanitizuojamas ČIA, kai kanalas sukuriamas: priešiškas
// serveris gali atsiųsti PRIVMSG su ESC simboliais nick'e — jei tokie
// simboliai liktų ch.name, jie būtų braižomi į ekraną (terminalo
// injekcija) ir rašomi į žurnalų failų vardus.
func (c *Client) ensureCh(name string) *Channel {
	if name == "" {
		return nil
	}
	ch := c.findCh(name)
	if ch != nil {
		return ch
	}
	// Per didelis kanalų skaičius — atmetame naują kanalą (flood apsauga).
	// Visada paliekame bent *server* kanalą.
	if len(c.channels) >= maxChannels {
		return nil
	}
	// Sanitizuojame PRIEŠ sukurdami — kad ch.name (kuris braižomas į ekraną
	// ir naudojamas žurnalų failų vardams) niekada nebūtų su terminalo
	// valdymo simboliais ar kelio elementais.
	safe := sanitizeText(name)
	if safe == "" {
		// Pavadinimas susidėjo tik iš valdymo simbolių — tokio kanalo
		// nekūriame (kitaip atsirastų tuščias / beprasmis langas).
		return nil
	}
	ch = newChannel(safe, c.maxMessages)
	// Kanalas prisimena serverio ISUPPORT parametrus, kad nick'ų
	// lygiavimas ir sortavimas būtų nuoseklūs (ir nepriklausomų
	// nuo to, kas tuo metu yra globalioje būsenoje).
	_, signs, modes, cm, _ := c.isupSnapshot()
	ch.prefix = signs
	ch.prefixModes = modes
	ch.casemapping = cm
	c.channels = append(c.channels, ch)
	return ch
}

// activeChan grąžina aktyvų kanalą, visada ne-nil. activeIdx gali būti -1
// (serverio PART *server* pašalino *server* kanalą), todėl indeksas
// tikrinamas PRIEŠ naudojant — kitaip draw()/addMsg() panikuoja.
func (c *Client) activeChan() *Channel {
	if c.activeIdx < 0 || c.activeIdx >= len(c.channels) {
		return nil
	}
	return c.channels[c.activeIdx]
}

// activeName grąžina aktyvaus kanalo pavadinimą (arba "" jei nėra).
func (c *Client) activeName() string {
	if ch := c.activeChan(); ch != nil {
		return ch.name
	}
	return ""
}

// setWhoTarget prisimena, į kurį langą grąžinti WHO atsakymus.
// target — kanalas, kuriame užklausta (jei ne kanalas — tuščia, tada
// atsakymai eina į *server*).
func (c *Client) setWhoTarget(target string) {
	c.mu.Lock()
	c.whoChan = target
	c.mu.Unlock()
}

// setWhoisTarget prisimena, į kurį langą grąžinti WHOIS atsakymus.
func (c *Client) setWhoisTarget(target string) {
	c.mu.Lock()
	c.whoisChan = target
	c.mu.Unlock()
}

// whoReplyMsg — WHO atsakymas. Jei užklausta buvo kanale, atsakymas
// rodomas TEN (o ne *server* lange) — kitaip vartotojas kanale jo nemato.
func (c *Client) whoReplyMsg(text string) {
	c.mu.Lock()
	target := c.whoChan
	c.mu.Unlock()
	if target == "" || !c.isChannel(target) {
		target = "*server*"
	}
	c.addMsg(target, Message{ts: time.Now(), text: text, msgType: MsgServer})
}

// whoisReplyMsg — WHOIS atsakymas (analogiškai whoReplyMsg).
func (c *Client) whoisReplyMsg(text string) {
	c.mu.Lock()
	target := c.whoisChan
	c.mu.Unlock()
	if target == "" || !c.isChannel(target) {
		target = "*server*"
	}
	c.addMsg(target, Message{ts: time.Now(), text: text, msgType: MsgServer})
}

// isConnected grąžina ryšio būklę saugiai (su rakinu). c.connected keičiamas
// connect/disconnect gorutinese, o skaitomas iš klaviatūros — tiesiogus
// skaitymas be rakino yra data race.
func (c *Client) isConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}

// myNick grąžina dabartinį nick'ą saugiai (su rakinu). c.nick keičiamas
// /nick komanda ir serverio NICK pranešimu — skaitymas be rakino yra race.
func (c *Client) myNick() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nick
}

// normalizeActiveIdx sugrąžina activeIdx į galiojamas ribas. Naudojamas
// po kanalų sąrašo keitimo (PART, /close), kad aktyvus kanalas liktų tas
// pats (o jei jis buvo pašalintas — kad indeksas vis tiek būtų galiojantis).
func (c *Client) normalizeActiveIdx(removedIdx int) {
	// Pašalintas kanalas buvo aktyvus → atsirinkti kitą.
	if removedIdx >= 0 && removedIdx == c.activeIdx {
		if removedIdx < len(c.channels) {
			c.activeIdx = removedIdx // kanalas po pašalinto užimą tą patį indeksą
		} else {
			c.activeIdx = len(c.channels) - 1
		}
		if c.activeIdx < 0 {
			c.activeIdx = 0
		}
		return
	}
	// Pašalintas kanalas buvo PRIEŠ aktyvaus → visi indeksai po juo
	// pasikeitė vienu žemyn, aktyvaus kanalas dabar yra senas indeksas-1.
	if removedIdx >= 0 && removedIdx < c.activeIdx {
		c.activeIdx--
	}
	// Bendra patikra — aktyvus visada turi būti galiojančioje zonoje.
	if c.activeIdx < 0 {
		c.activeIdx = 0
	}
	if c.activeIdx >= len(c.channels) {
		c.activeIdx = len(c.channels) - 1
	}
	if c.activeIdx < 0 {
		c.activeIdx = 0
	}
}

// removeCh pašalina kanalą iš sąrašo (indeksuoti pagal c.channels) ir
// sugalioja activeIdx. Užtikrina, kad sąraše visada liktų bent vienas
// kanalas — serverio PART negali ištrukti vienintelio *server* kanalo ir
// palikti activeIdx = -1 (nuo to vėliau draw()/addMsg() panikuoja).
// Grąžina true, jei kanalas buvo pašalintas.
func (c *Client) removeCh(idx int) bool {
	if idx < 0 || idx >= len(c.channels) {
		return false
	}
	// Visada paliekame bent vieną kanalą (pvz. *server*).
	if len(c.channels) <= 1 {
		return false
	}
	c.channels = append(c.channels[:idx], c.channels[idx+1:]...)
	c.normalizeActiveIdx(idx)
	return true
}

// isIgnored tikrina arba šaltinio `prefix` (nick!user@host) atitinka ignore
// sąrašą. Palaikomos ir paprastos nick'ų eilutės (senas elgesys), ir wildcards
// (*, ?) maskės: "gaidys!*@*", "*!*@sraunus.host.tld", "bot*".
func (c *Client) isIgnored(prefix string) bool {
	// ignoreList keičiamas iš /ignore ir /unignore komandų (klaviatūros
	// gorutinos), o skaitomas čia (parseIRC gorutinos) — reikia rakino.
	// BŪTINA tikra kopija: /unignore filtruoja sąrašą IN-PLACE, todėl
	// nuoroda į seną backing array matytų tarpinį (dubliuotą) elementą.
	c.mu.Lock()
	list := make([]string, len(c.ignoreList))
	copy(list, c.ignoreList)
	c.mu.Unlock()
	if len(list) == 0 {
		return false
	}
	nick := prefix
	if i := strings.IndexByte(prefix, '!'); i != -1 {
		nick = prefix[:i]
	}
	for _, ig := range list {
		if ig == "" {
			continue
		}
		if !strings.ContainsAny(ig, "*?") {
			// tiesioginis nick arba atitinkama eilutė
			if c.nickEqual(ig, nick) {
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
	if ch == nil {
		// Kanalas be pavadinimo — nekuriam (serverio klastota komanda)
		c.mu.Unlock()
		return
	}
	// Naudojame SANITIZUOTĄ pavadinimą — originalį gali atsiųsti priešiškas
	// serveris (ESC simboliai → terminalo injekcija, kelias už logDir ribų).
	chanName = ch.name
	ch.addMsg(m)
	isActive := c.activeChan() == ch
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
	// Į žurnalą — per eilę (viena gorutina), ne per `go logMessage`.
	// Taip žurnalo eilės eina ta pačia tvarka kaip į klientą atėjusios
	// žinutės.
	c.enqueueLog(logEntry{logDir: logDir, server: server, chanName: chanName, msg: m})
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
			if !c.isChannel(chanName) {
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
		// Išsaugoti dabartinę scroll poziciją (jei aktyvus kanalas egzistuoja)
		if cur := c.activeChan(); cur != nil {
			cur.scrollOff = c.scrollOff
		}
		c.activeIdx = idx
		// Atstatyti išsaugotą poziciją
		c.scrollOff = c.channels[idx].scrollOff
		c.channels[idx].unread = 0
		c.channels[idx].highlight = 0
		c.nickScrollOff = 0
		c.searchMode = false
		// Kanalų sąrašo slinktis — aktyvus kanalas turi būti MATOMAS.
		// (Slikties ribą skaičiuojame čia, po c.mu — visibleChannelRows()
		// ima patį c.mu, todėl negalima jo iškviesti laikant užrakintą.)
		rows := c.height - 4
		if rows < 1 {
			rows = 1
		}
		if idx < c.chanScrollOff {
			// Peršokome į viršų — slinktis grįžta, kad kanalas matytųsi.
			c.chanScrollOff = idx
		} else if idx >= c.chanScrollOff+rows {
			// Peršokome žemyn — slinktis nusikelia iki kanalo.
			c.chanScrollOff = idx
		}
	}
	c.mu.Unlock()
	c.queueRedraw()
}

// ---- Logging ----

// startLogWorker paleidžia žurnalų gorutiną. Ji apdoroja eilę FIFO tvarka,
// todėl žurnalo eilės visada tokios pačios tvarkos, kokia atėjo į klientą.
func (c *Client) startLogWorker() {
	go func() {
		for {
			select {
			case e := <-c.logQueue:
				c.logMessage(e.logDir, e.server, e.chanName, e.msg)
			case <-c.logStop:
				// Ištuštiname eilę (kad neprarastų žinučių) ir
				// sustojame.
				for {
					select {
					case e := <-c.logQueue:
						c.logMessage(e.logDir, e.server, e.chanName, e.msg)
					default:
						return
					}
				}
			}
		}
	}()
}

// enqueueLog įdeda žinutę į žurnalų eilę. Jei eilė pilna (labai didelis
// srautas), žinutė tiesiog atmetama — geriau prarasti žurnalą nei
// sustabdyti klientą.
func (c *Client) enqueueLog(e logEntry) {
	if e.logDir == "" {
		return
	}
	select {
	case c.logQueue <- e:
	default:
	}
}

// stopLogWorker sustabdo žurnalų gorutiną (ištušindamas eilę).
func (c *Client) stopLogWorker() {
	c.logOnce.Do(func() {
		close(c.logStop)
	})
}

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
	case MsgNick:
		// Nick'o pakeitimas: „*** senas → naujas“. Be to žurnale likdavo
		// tik naujas nick'as („Žilvinas2“) — neaišku, kas į ką pasikeitė.
		line = fmt.Sprintf("[%s] *** %s → %s\n", ts, nick, plain)
	case MsgNotice:
		line = fmt.Sprintf("[%s] -%s- %s\n", ts, nick, plain)
	default:
		line = fmt.Sprintf("[%s] %s\n", ts, plain)
	}
	// Slaptažodžiai (NickServ IDENTIFY/REGISTER/SET PASSWORD) niekada ne
	// rašomi į žurnalą — jame liktų plaintext slaptažodis.
	if isSensitiveHistory(plain) {
		line = fmt.Sprintf("[%s] <%s> [slaptažodis paslėptas]\n", ts, nick)
	}
	_, _ = f.WriteString(line)
}

// sanitizePath padaro serverio/kanalo pavadinimą saugiu katalogo vardu.
//
// Svarbu: ne tik pakeičia „/“ į „_“, bet ir blokuoja „..“ —
// filepath.Join(logDir, "..") nukelia į tėvinį katalogą, t.y. logai
// būtų rašomi UŽ nurodyto žurnalo katalogo ribų (serverio vardas
// naudojamas kaip katalogo vardas, o serverį renka /connect komanda).
func sanitizePath(s string) string {
	// Tuščias vardas lieka tuščias — clearLogs("") juo žymi „trinti visus“.
	if s == "" {
		return ""
	}
	r := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_", "\"", "_")
	s = r.Replace(s)
	// „..“ (ir „...“ ir t. t.) neleidžiame — jie reiškia katalogų kelią.
	// Po pakeitimo liko tik „..“ (be separatorių) — pakeičiame į „_“.
	for strings.Contains(s, "..") {
		s = strings.ReplaceAll(s, "..", "_")
	}
	// Taip pat apsaugome nuo pavadinimų, kuriuose yra tik taškai arba jie
	// tušti — tokie keliai nukelia aukštyn.
	if strings.Trim(s, ".") == "" {
		return "_"
	}
	return s
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

// truncateIRC apriboja eilutę iki 512 baitų (RFC 2812: eilutė = komanda +
// parametrai + CRLF). Ilgesnės eilutės nutrumpinamos — kitaip serveris
// nutraukia ryšį. Bandoma nukirsti UTF-8 simbolio viduryje — neleidžiama.
func truncateIRC(line string) string {
	// CRLF užimą 2 baitus
	const limit = maxLineBytes - 2
	if len(line) <= limit {
		return line
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(line[cut]) {
		cut--
	}
	return line[:cut]
}

func (c *Client) send(line string) {
	// Apsaugoti nuo IRC protokolo injekcijos per \r\n
	line = ircLineSanitizer.Replace(line)
	// Neviršyti 512 baitų limito (serveris nutraukia ryšį)
	line = truncateIRC(line)
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
//
// Anksčiau kiekvieną 50 ms sukūrė naują `time.After` — per 5 minučių
// laukimo tai ~6000 nenaudojamų timerių (kiekvienas savo runtime.timer).
// Dabar — VIENAS periodinis laikiklis.
func (c *Client) sleepReconnect(delay time.Duration, gen int) bool {
	t := time.NewTimer(delay)
	defer t.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-t.C:
			c.mu.Lock()
			stale := c.connectGen != gen
			c.mu.Unlock()
			return !stale
		case <-tick.C:
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
	srv := joinHostPort(c.server, c.port)
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

	// Ping keepalive.
	//
	// PING siunčiamas TIESIOGINIAI į šios jungties writeCh (newWriteCh), o
	// ne per c.send() — c.send() naudoja bendrą c.writeCh, kuris po
	// /connect jau rodo į NAUJĄ kanalą. Senas ping tick todėl rašytų
	// seną, nebevykdomą kanalą (serveris negauna PONG → „Ping timeout“).
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
				stale := c.connectGen != gen
				c.mu.Unlock()
				if !connected || stale {
					return
				}
				if time.Since(lastPong) > 120*time.Second {
					c.errMsg("Ping timeout - jungiamasi iš naujo")
					conn.Close()
					return
				}
				select {
				case newWriteCh <- fmt.Sprintf("PING :%d", time.Now().Unix()):
				default:
					// Eilė pilna — praleidžiame šį PING.
				}
			case <-pingStop:
				return
			}
		}
	}()

	c.mu.Lock()
	realName := c.realName
	serverPass := c.serverPass
	c.mu.Unlock()
	if realName == "" {
		realName = "mircBSD IRC Client"
	}
	// IRCv3 CAP derybos — serveris laukia CAP END prieš siųsdamas 001
	c.send("CAP LS 302")
	// PASS (serverio slaptažodis) — bouncer arba privatus serveris.
	// Siunčiamas PRIEŠ NICK, kaip reikalauja RFC 2812.
	if serverPass != "" {
		c.send("PASS " + serverPass)
	}
	c.send("NICK " + nick)
	c.send(fmt.Sprintf("USER %s 0 * :%s", nick, realName))

	reader := bufio.NewReader(conn)
	c.readLoop(reader)

	close(pingStop)
	close(writeStop)
	c.mu.Lock()
	// Sena jungtis gali nutrūkti PO to, kai jau įsteigta nauja (vartotojas
	// spaudė /connect). Tokiu atveju šio teardown'o negriebti naujos
	// jungties būsenos — liko be `conn` (fd nutekėjimas) ir `connected=false`.
	stale := c.connectGen != gen
	if !stale {
		c.connected = false
		c.conn = nil
	}
	c.mu.Unlock()
	if stale {
		// Nutraukta jau nebevykdoma jungtis — nauja jungtis tvarkoma
		// savo goroutine.
		return
	}
	c.errMsg("Ryšys nutrūko")
	c.queueRedraw()
}

// readLoop skaito serverio eilutes ir perduoda jas į parseIRC.
//
// Eilutės ribojamos: jei serveris siunčia milijonų baitų eilutę be '\n',
// ReadSlice grąžina bufio.ErrBufferFull — tuomet eilutė apleidžiama iki
// '\n', o atmintis nesusikaupia (priešingai nei ReadString be ribos, kur
// priešiškas serveris galėtų kaupti megabaitus).
func (c *Client) readLoop(reader *bufio.Reader) {
	// Panika (pvz. neteisingai interpretuotas serverio numeris) negali
	// palikti terminalo raw režimu — atkursime jį ir išeisime švariai.
	defer c.recoverPanic("serverio eilutės skaitymas")
	for {
		line, err := reader.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			// Per didelė eilutė — apleidžiame iki '\n'
			for err == bufio.ErrBufferFull {
				_, err = reader.ReadSlice('\n')
			}
			c.errMsg("ĮSPĖJIMAS: serverio eilutė per ilga — atmesta")
			continue
		}
		if err != nil {
			return
		}
		c.parseIRC(strings.TrimRight(string(line), "\r\n"))
	}
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
		isMe := c.nickEqual(fromNick, c.nick)
		channel := c.ensureCh(ch)
		if channel == nil {
			// Kanalas be pavadinimo — ignoruojame (serverio klastota komanda)
			c.mu.Unlock()
			return
		}
		channel.addNick(fromNick)
		if isMe {
			for i, cn := range c.channels {
				if cn == channel {
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
		// Serverio komanda be kanalo parametro neturi teisės kurti kanalo.
		if ch == "" {
			return
		}
		c.mu.Lock()
		channel := c.findCh(ch)
		if channel != nil {
			channel.removeNick(fromNick)
		}
		isMe := c.nickEqual(fromNick, c.nick)
		if isMe {
			// *server* yra vidinis kanalas — serveris negali jo pašalinti,
			// nes tada liktų tuščias kanalų sąrašas ir activeIdx = -1.
			if !strings.EqualFold(ch, "*server*") {
				for i, cn := range c.channels {
					if cn == channel {
						c.removeCh(i)
						break
					}
				}
			}
		}
		c.mu.Unlock()
		if !isMe && channel != nil {
			// Žinutė rodoma tik jei kanalas jau buvo mūsų sąraše — kitaip
			// serveris galėtų prikurpti šoninį kanalą per atsitiktinį PART.
			c.addMsg(ch, Message{ts: now, nick: fromNick, text: msg, msgType: MsgPart})
		}

	case "QUIT":
		msg := p(0)
		// Casemapping'ą imame VIENĄ kartą — c.nickEqual() kiekvieną kartą
		// ima isupMu.RLock, o cikle su 100 kanalų × 500 nick'ų tai 50 000
		// rakinio įvedių (RWMutex.skalių operacija).
		_, _, _, cm, _ := c.isupSnapshot()
		c.mu.Lock()
		// Rasti kanalus kuriuose buvo nick PRIEŠ pašalinimą
		var affected []*Channel
		for _, ch := range c.channels {
			for _, n := range ch.nicks {
				if nickEqual(stripMode(n, ch.prefixSigns()), fromNick, cm) {
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
		if c.nickEqual(kicked, me) {
			c.errMsg(fmt.Sprintf("Buvai išmestas iš %s (%s)", ch, reason))
			// Pateikti kanalą kaip neaktyvų, bet neuždaryti lango
		} else {
			c.addMsg(ch, Message{ts: now, nick: fromNick,
				text:    fmt.Sprintf("išmetė %s (%s)", kicked, reason),
				msgType: MsgServer})
		}
		c.queueRedraw()

	case "INVITE":
		// :nick!user@host INVITE <target-nick> :#channel
		// p(0) — kuris nick pakviestas, p(1) — kanalas į kurį kviečia.
		// Anksčiau rodomas buvo p(0) (target nick), todėl kanalas niekada
		// nepasiekdavo vartotojo (rašydavo „rašyk /join manick“).
		invitedTo := p(0)
		inviteChan := p(1)
		if inviteChan == "" {
			return
		}
		if invitedTo != "" {
			c.mu.Lock()
			isMe := c.nickEqual(invitedTo, c.nick)
			c.mu.Unlock()
			if !isMe {
				// Mus kitaip — pakvietė (mums pačiams skirtas pranešimas).
				return
			}
		}
		c.serverMsg(fmt.Sprintf("*** %s kviečia tave į %s (rašyk /join %s)", fromNick, inviteChan, inviteChan))
		c.queueRedraw()

	case "NICK":
		newNick := p(0)
		if newNick == "" {
			return
		}
		c.mu.Lock()
		if c.nickEqual(fromNick, c.nick) {
			c.nick = newNick
		}
		// Surinkti kanalus, kuriuose buvo senasis nick — ten rodykime
		// pranešimą („*** senas → naujas“), kaip mIRC.
		var nickChans []string
		for _, ch := range c.channels {
			if ch.hasNick(fromNick) {
				ch.renameNick(fromNick, newNick)
				nickChans = append(nickChans, ch.name)
			}
		}
		c.mu.Unlock()
		for _, chName := range nickChans {
			c.addMsg(chName, Message{ts: now, nick: fromNick, text: newNick, msgType: MsgNick})
		}
		c.serverMsg(fmt.Sprintf("%s → %s", fromNick, newNick))

	case "MODE":
		target := p(0)
		modeStr := p(1)
		// Nick mode kanaluose
		if c.isChannel(target) {
			_, signs, modes, _, _ := c.isupSnapshot()
			c.mu.Lock()
			ch := c.findCh(target)
			if ch != nil && len(params) > 2 {
				// params[2..] = argumentai mode raidėms. Privalime skaičiuoti
				// VISUS mode, kurie imasi argumento (o/v/h/q/a nickams; b, k,
				// l, e, I... irgi turi parametrą) — kitaip indeksas „nuklysta“
				// (pvz. MODE #chan +kb nick mask).
				adding := true
				modeIdx := 2
				argLetters := modes + "blkeI"
				for _, mr := range modeStr {
					switch {
					case mr == '+':
						adding = true
					case mr == '-':
						adding = false
					case strings.IndexRune(argLetters, mr) >= 0:
						if modeIdx < len(params) {
							// tik nick'ų statuso modas keičia nick'o prefixą
							if i := strings.IndexRune(modes, mr); i >= 0 && i < len(signs) {
								pfx := string('-')
								if adding {
									pfx = string('+')
								}
								ch.setMode(params[modeIdx], pfx+string(mr), signs, modes)
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
				if c.nickEqual(target, me) {
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
				// CTCP PING atspinduliuojamas — bet apribojamas: be ribos
				// užpuolėjas galėtų atsiųsti 100 KB CTCP PING ir priversti
				// serverį gauti tiek pat atspindą (atspindėjimo amplifikacija).
				payload := inner
				if len(payload) > maxCTCPBytes {
					payload = payload[:maxCTCPBytes]
					// neperpjauti UTF-8 simbolio
					for len(payload) > 0 && !utf8.RuneStart(payload[len(payload)-1]) {
						payload = payload[:len(payload)-1]
					}
				}
				c.send(fmt.Sprintf("NOTICE %s :\x01%s\x01", fromNick, payload))
			}
			return
		}

		dest := target
		isPM := false
		if c.nickEqual(target, me) {
			dest = fromNick
			isPM = true
		}
		// echo-message: serveris siunčia atgal mūsų pačių žinutes — nerodyti dublikatų
		c.mu.Lock()
		isEcho := c.activeCaps["echo-message"] && c.nickEqual(fromNick, c.nick)
		c.mu.Unlock()
		if isEcho {
			return
		}
		if isPM {
			c.mu.Lock()
			ch := c.ensureCh(dest)
			if ch != nil {
				ch.isPrivate = true
			}
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
		// Serveris gali siųsti 332 be kanalo parametro — toks kanalas
		// neegzistuoja, todėl jo nekuriame (ir nepranešame apie kanalą).
		if ch == "" {
			return
		}
		c.mu.Lock()
		if channel := c.findCh(ch); channel != nil {
			channel.topic = topic
		}
		c.mu.Unlock()
		c.addMsg(ch, Message{ts: now, text: "Tema: " + topic, msgType: MsgServer})
		c.queueRedraw()

	case "353":
		// RPL_NAMREPLY — serverio komanda. Ji turi NAUJŲ kanalų nekurti:
		// priešiškas serveris galėtų siųsti šimtais 353 numerių ir
		// sukurti tiek langų (užpildydamas šoninę juostą). 332 (tema)
		// jau yra užkirtas taip pat.
		ch := p(2)
		users := p(3)
		if ch == "" {
			return
		}
		c.mu.Lock()
		channel := c.findCh(ch)
		if channel == nil {
			// Kanalas, kurio neprisijungėme — praleidžiame.
			c.mu.Unlock()
			return
		}
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

	case "315": // RPL_ENDOFWHO — WHO sąrašo pabaiga
		c.whoReplyMsg(fmt.Sprintf("── WHO %s: pabaiga ──", p(1)))

	case "352": // RPL_WHOREPLY: <client> <channel> <user> <host> <server> <nick> <flags> :<hopcount> <realname>
		// Anksčiau šis numerikas krisdavo į `default:` ir buvo rodomas
		// žaliai kaip „[352] …“ — vartotojas nematė, kas yra kanale.
		whoChan := p(1)
		whoNick := p(5)
		whoFlags := p(6)
		if whoNick != "" {
			c.whoReplyMsg(fmt.Sprintf("│  %-20s %s%s", whoChan, whoFlags, whoNick))
		}

	case "354": // RPL_WHOSPCRPL — WHOIS stilingas (whowas)
		whoNick := p(1)
		whoFlags := p(2)
		whoReal := p(7)
		if whoNick != "" {
			c.whoReplyMsg(fmt.Sprintf("│  %s%s — %s", whoFlags, whoNick, whoReal))
		}

	case "367": // RPL_BANLIST: <client> <channel> <mask> <who> <when>
		c.serverMsg(fmt.Sprintf("  ban: %s", p(2)))
	case "368": // RPL_ENDOFBANLIST
		c.serverMsg("── Banų sąrašo pabaiga ──")
	case "369": // RPL_ENDOFWHOWAS
		c.serverMsg("── WHOWAS pabaiga ──")

	case "251": // RPL_LUSERCLIENT
		c.serverMsg(fmt.Sprintf("Serveris: %s vartotojų, %s nežinomų, %s kanalų", p(1), p(2), p(3)))
	case "252": // RPL_LUSEROP
		c.serverMsg(fmt.Sprintf("Operatorių: %s", p(1)))
	case "253": // RPL_LUSERUNKNOWN
		c.serverMsg(fmt.Sprintf("Nežinomų serverių: %s", p(1)))
	case "255": // RPL_LUSERCHANNELS
		c.serverMsg(fmt.Sprintf("Kanalų: %s", p(1)))

	case "265": // RPL_LOCALUSERS
		c.serverMsg(fmt.Sprintf("Vietinių vartotojų: %s, maks. %s", p(1), p(2)))
	case "266": // RPL_GLOBALUSERS
		c.serverMsg(fmt.Sprintf("Iš viso vartotojų: %s, maks. %s", p(1), p(2)))

	case "391": // RPL_TIME
		c.serverMsg(fmt.Sprintf("Serverio laikas [%s]: %s", p(1), p(2)))

	case "301":
		c.whoisReplyMsg(fmt.Sprintf("%s yra away: %s", p(1), p(2)))

	case "311": // RPL_WHOISUSER: nick user host * :realname
		nick, user2, host, realname := p(1), p(2), p(3), p(5)
		c.whoisReplyMsg(fmt.Sprintf("┌─[whois] %s (%s@%s) — %s", nick, user2, host, realname))
	case "312": // RPL_WHOISSERVER: nick server :info
		c.whoisReplyMsg(fmt.Sprintf("│  Serveris : %s (%s)", p(2), p(3)))
	case "313": // RPL_WHOISOPERATOR
		c.whoisReplyMsg(fmt.Sprintf("│  Operatorius: %s", p(1)))
	case "317": // RPL_WHOISIDLE: nick idle signon
		idle, _ := strconv.Atoi(p(2))
		signonTs, _ := strconv.ParseInt(p(3), 10, 64)
		idleStr := fmt.Sprintf("%dm %ds", idle/60, idle%60)
		signonStr := ""
		if signonTs > 0 {
			signonStr = "  prisijungė: " + time.Unix(signonTs, 0).Format("2006-01-02 15:04")
		}
		c.whoisReplyMsg(fmt.Sprintf("│  Idle      : %s%s", idleStr, signonStr))
	case "319": // RPL_WHOISCHANNELS
		c.whoisReplyMsg(fmt.Sprintf("│  Kanalai   : %s", p(2)))
	case "330": // RPL_WHOISACCOUNT: nick account :is logged in as
		c.whoisReplyMsg(fmt.Sprintf("│  Paskyra   : %s", p(2)))
	case "671": // RPL_WHOISSECURE
		c.whoisReplyMsg(fmt.Sprintf("│  Ryšys     : TLS (šifruotas)"))
	case "318": // RPL_ENDOFWHOIS
		c.whoisReplyMsg(fmt.Sprintf("└─[whois] %s — pabaiga", p(1)))

	case "305": // RPL_UNAWAY — serveris patvirtino away išjungimą
		c.mu.Lock()
		c.away = ""
		c.mu.Unlock()
		c.serverMsg("Away išjungtas")
		c.queueRedraw()

	case "306": // RPL_NOWAWAY — serveris patvirtino away įjungimą
		// Nustatome away būseną — anksčiau statuso juosta nerodė [AWAY].
		c.mu.Lock()
		if c.away == "" {
			c.away = "Away"
		}
		c.mu.Unlock()
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

		case "NEW":
			// CAP NEW — serveris siūlo naują cap'ą PO registracijos.
			// Anksčiau ši komanda buvo ignoruojama, todėl serveris, kuris
			// sasl'ą siūlo tik per CAP NEW, nebūdavo derybas.
			// Atsakome CAP REQ (jei mums reikia) arba CAP END.
			c.handleCapNew(capsStr)

		case "LIST":
			// CAP LIST — serveris klausia, kokius capus turime.
			c.mu.Lock()
			var active []string
			for name := range c.activeCaps {
				active = append(active, name)
			}
			c.mu.Unlock()
			sort.Strings(active)
			if len(active) == 0 {
				c.send("CAP LIST :")
			} else {
				c.send("CAP LIST :" + strings.Join(active, " "))
			}

		case "DEL":
			// CAP DEL — serveris atima cap'ą. Aktyvius capus pažymime
			// neaktyviais.
			c.mu.Lock()
			for _, cap := range strings.Fields(capsStr) {
				name := strings.ToLower(strings.TrimLeft(cap, "-"))
				delete(c.activeCaps, name)
			}
			c.mu.Unlock()
		}

	case "AUTHENTICATE":
		if p(0) == "+" {
			// Serveris prašo slaptažodžio TIK jeigu mes patys užsakėme SASL
			// (saslPending). Be šios patikros priešiškas serveris galėtų
			// gauti mūsų SASL slaptažodį už bet kokio prašymo.
			c.mu.Lock()
			pending := c.saslPending
			nick := c.nick
			pass := c.saslPass
			c.mu.Unlock()
			if !pending {
				// Atsakome tuščiu slaptažodžiu — neatsakymas liktų
				// serverį laukiančioje būsenoje.
				c.send("AUTHENTICATE +")
				return
			}
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
			// Casemapping'ą imame vieną kartą (cikle kiekvienas
			// c.nickEqual() → isupMu.RLock).
			_, _, _, cm, _ := c.isupSnapshot()
			if reason != "" {
				nickLow := strings.ToLower(fromNick)
				c.mu.Lock()
				last, seen := c.awayNotified[nickLow]
				if !seen || now.Sub(last) >= 10*time.Minute {
					c.awayNotified[nickLow] = now
					active := c.activeChan()
					c.mu.Unlock()
					if active != nil {
						for _, n := range active.nicks {
							if nickEqual(stripMode(n, active.prefixSigns()), fromNick, cm) {
								c.addMsg(active.name, Message{ts: now, nick: fromNick,
									text:    fromNick + " yra away: " + reason,
									msgType: MsgServer})
								break
							}
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
				active := c.activeChan()
				c.mu.Unlock()
				if wasAway && active != nil {
					for _, n := range active.nicks {
						if nickEqual(stripMode(n, active.prefixSigns()), fromNick, cm) {
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

// isValidTarget tikrina komandos gavėją (nick'ą arba kanalą) — jis
// įrašomas į PRIVMSG/NOTICE komandas. Leistini tik raidės, skaičiai ir
// pagrindiniai adresės simboliai, kad į komandą nepatektų tarpai,
// CRLF ar valdymo simboliai (komandų injekcija).
func isValidTarget(t string) bool {
	if t == "" || len(t) > 100 {
		return false
	}
	for _, r := range t {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == '_':
		case r == '#' || r == '&' || r == '!' || r == '+':
		// Raidės (lietuviškos taip pat) — leidžiame, bet ne valdymo
		// simbolius, ne tarpus ir ne CRLF.
		case unicode.IsLetter(r):
		default:
			return false
		}
	}
	return true
}

// standartinėsChanTypes — RFC numytieji kanalų prefiksai, kai serveris
// nepaskyrė CHANTYPES (ISUPPORT).
const standartinėsChanTypes = "#&!+"

// isValidChannel tikrina kanalo pavadinimą: jis įrašomas į JOIN/PART
// komandas, todėl turi būti ribojamas (be tarpų, be CRLF, be valdymo
// simbolių) — kitaip galima komandų injekcija.
//
// Pirmas simbolis yra kanalo prefiksas pagal serverio CHANTYPES
// („#“, „&“ ir pan.); likusi dalis — raidės, skaičiai ir „.-_“.
// Prefiksas be vardo („#“ vienas) — netinkamas.
//
// Naudoja standartinius prefiksus. Kai serveris skelbia kitokius
// CHANTYPES (pvz. „#$“), naudok isValidChannelFor.
func isValidChannel(ch string) bool {
	return isValidChannelFor(ch, standartinėsChanTypes)
}

// isValidChannelFor tikrina kanalo pavadinimą pagal nurodytus CHANTYPES.
//
// Ankstesnė versija kietai kodavo „#&!+“, todėl serveris su CHANTYPES=“#$”
// atmesdavo kanalą „$kanalas“ — o jis VIS LIEGALUS tame serveryje.
func isValidChannelFor(ch string, chantypes string) bool {
	if ch == "" || len(ch) > 50 {
		return false
	}
	if chantypes == "" {
		chantypes = standartinėsChanTypes
	}
	// Prefiksas turi būti pagal CHANTYPES.
	first := rune(ch[0])
	if !strings.ContainsRune(chantypes, first) {
		return false
	}
	for i, r := range ch {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == '_':
		case i == 0:
			// Prefiksas (jau patikrintas aukščiau)
		// Raidės (lietuviškos taip pat) — leidžiame.
		case unicode.IsLetter(r):
		default:
			return false
		}
	}
	// bent vienas simbolis po prefikso
	return len(ch) > 1
}

// joinHostPort sujungia serverio adresą ir portą tinkamu formatu.
//
// IPv6 adresas turi būti apgaubtas laiptaisais: „[::1]:6667“ — be jų
// „::1:6667“ yra neaiškus adresas (portas arba adresas?).
func joinHostPort(host, port string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]:" + port
	}
	return host + ":" + port
}

// isValidServer tikrina serverio pavadinimą: raidės, skaičiai ir
// pagrindiniai adresės simboliai (.-_). Be to patikrinama, kad pavadinime
// nėra tarpų, CRLF ar valdymo simbolių — serverio vardas įrašomas į IRC
// komandas (NICK, USER, JOIN) ir į žurnalų katalogo vardą, todėl turi
// būti apsaugotas nuo komandų injekcijos.
//
// Palaikomi ir IPv6 adresai (pvz. „::1“, „2001:db8::1“) — anksčiau jie
// buvo atmetami, nes „:“ nebuvo leistinas simbolis.
func isValidServer(server string) bool {
	if server == "" || len(server) > 100 {
		return false
	}
	// „..“ (ir jo atmainos) — katalogų traversas žurnalų katalogo
	// pavadinime; tokio serverio vardo nepriimame.
	if strings.Contains(server, "..") {
		return false
	}
	// IPv6 adresas — tik heksadecimaliniai skaičiai, „:“ ir „::“.
	if strings.Contains(server, ":") {
		return isValidIPv6(server)
	}
	for _, r := range server {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}

// isValidIPv6 tikrina IPv6 adreso formą: 2–8 grupės, kiekviena 1–4
// heksadecimaliniai skaičiai, atskirta „:“; galima ir „::“ (sufiksso
// trumpinimas). ZIP formata („[::1]:6667“) NEPRIIMAMI — adresas ir portas
// perduodami atskirai.
//
// Zone ID („fe80::1%eth0“) NEPRIIMAMI — retas atvejis IRC serveriams, o
// „%“ simbolis apsunkintų žurnalų katalogų pavadinimų apsaugą.
func isValidIPv6(s string) bool {
	if strings.ContainsAny(s, "[]%") {
		return false
	}
	// „::“ gali būti tik VIENAS.
	if strings.Count(s, "::") > 1 {
		return false
	}
	var groups []string
	if i := strings.Index(s, "::"); i >= 0 {
		groups = append(groups, splitIPv6Groups(s[:i])...)
		groups = append(groups, splitIPv6Groups(s[i+2:])...)
		// Su „::“ — nuo 0 iki 7 grupių (viena vieta skirta nuliniams).
		if len(groups) > 7 {
			return false
		}
	} else {
		groups = splitIPv6Groups(s)
		// Be „::“ — lygiai 8 grupės.
		if len(groups) != 8 {
			return false
		}
	}
	for _, g := range groups {
		if len(g) == 0 || len(g) > 4 {
			return false
		}
		for _, r := range g {
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
				return false
			}
		}
	}
	return true
}

// splitIPv6Groups skaido IPv6 adreso dalį į grupes. Tuščios grupės
// (iš „:“ arba „::“) atmestos — tikrosios grupės tikrinamos atskirai.
func splitIPv6Groups(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, g := range strings.Split(s, ":") {
		if g != "" {
			out = append(out, g)
		}
	}
	return out
}

// isValidNick tikrina nick pagal IRC taisykles: leistini tik RFC nick simboliai
// (raidės, skaičiai, _ - [ ] \ ` ^ { } |), max 30 ženklų. Čia ne tik apsauga nuo
// protokolo klaidų, bet ir nuo valdymo simbolių (pvz. ESC) patekimo į serverį.
//
// Svarbu: ilgis skaičiuojamas SIMBOLIAIS (rune'ais), ne baitais. Anksčiau
// 30 baitų riba atmete 15 lietuviškų raidžių (kiekviena = 2 baitai) —
// lietuviškasis vartotojas negalėjo nustatyti savo nick'o.
func isValidNick(nick string) bool {
	if nick == "" {
		return false
	}
	// RFC riba: 15 simbolių, bet daugoma serverių leidžia 30. Skaičiuojame
	// simboliais, kad 30 lietuviškų raidžių būtų galimos.
	if utf8.RuneCountInString(nick) > 30 {
		return false
	}
	for _, r := range nick {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("_-[]\\`^{}|", r):
		// Raidės rašomos lietuviškai (ą, č, ė, į, š, ų, ū, ž) — leidžiame
		// visas raides, išskyrus valdymo simbolius ir tarpus.
		case unicode.IsLetter(r):
		default:
			return false
		}
	}
	return true
}

// isValidNickLen tikrina nick'o ilgį pagal serverio NICKLEN ribą.
// Kai serveris ribos nepranešė (0) — naudojama standartinė (30).
func (c *Client) isValidNickLen(nick string) bool {
	limit := c.nickLenLimit()
	return utf8.RuneCountInString(nick) <= limit
}

// nickLenLimit grąžina serverio NICKLEN ribą (arba standartinę 30).
func (c *Client) nickLenLimit() int {
	c.isupMu.RLock()
	defer c.isupMu.RUnlock()
	if c.limits.NickLen > 0 {
		return c.limits.NickLen
	}
	return 30
}

// channelLenLimit grąžina serverio CHANNELLEN ribą (arba standartinę 50).
func (c *Client) channelLenLimit() int {
	c.isupMu.RLock()
	defer c.isupMu.RUnlock()
	if c.limits.ChannelLen > 0 {
		return c.limits.ChannelLen
	}
	return 50
}

// topicLenLimit grąžina serverio TOPICLEN ribą (arba standartinę 390).
func (c *Client) topicLenLimit() int {
	c.isupMu.RLock()
	defer c.isupMu.RUnlock()
	if c.limits.TopicLen > 0 {
		return c.limits.TopicLen
	}
	return 390
}

// kickLenLimit grąžina serverio KICKLEN ribą (0 = serveris nepranešė).
func (c *Client) kickLenLimit() int {
	c.isupMu.RLock()
	defer c.isupMu.RUnlock()
	return c.limits.KickLen
}

// isValidPort tikrina TCP portą: skaičius 1–65535.
func isValidPort(port string) bool {
	if port == "" || len(port) > 5 {
		return false
	}
	for _, r := range port {
		if r < '0' || r > '9' {
			return false
		}
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return false
	}
	return true
}
