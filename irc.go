package main

import (
	"bufio"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
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

// maxReadBuffer – serverio skaitiklio (readLoop) bufferio dydis.
//
// DAUGIAU nei IRC 512 eilutės limitas (maxLineBytes): serverio PRIDĖTI
// IRCv3 tag'ai (server-time, msgid — kuriuos patys prašome per CAP LS)
// NEĮEINA į 512 eilutės biudžetą, todėl realios žinutės su tag'ais yra
// ~550+ baitų. Su 512 baitų bufferiu kiekviena tokia eilutė sukelia
// bufio.ErrBufferFull → eilutė atmestama ir POKALBIS PRARANDAMAS.
//
// Priešiškas serveris vis tiek negali siųsti be galo ilgos eilutės —
// ilgesnė nei maxReadBuffer atmestama (žr. readLoop).
const maxReadBuffer = 4096

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
		case strings.HasPrefix(tok, "MONITOR="):
			c.limits.Monitor = atoiDefault(tok[len("MONITOR="):], 0)
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
	Monitor    int // MONITOR= — didžiausias stebimų nick'ų kiekis (0 — nepalaikomas)
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

// chanModeGroups — CHANMODES (ISUPPORT 005) suskaidytos į 4 grupes.
//
// Formatas: „CHANMODES=A,B,C,D“ (RFC 2811; ISUPPORT draftas
// draft-brocklesby-irc-isupport-03). REALŲ serverių reikšmės taip pat
// 4-dalės: UnrealIRCd „eIbq,k,flj,CFLMPQScgimnprstz“, IRCNet
// „beI,k,l,imnpstaqr“, Hybrid „beI,k,l,imnpsta“.
//
//	A — režimai, pridedantys arba pašalinantys adresą iš/iš sąrašo;
//	    VISADA su parametru, kai siunčia serveris klientui;
//	B — režimai, keičiantys kanalo nustatymą; VISADA su parametru;
//	C — režimai, keičiantys kanalo nustatymą, bet TIK nustatant
//	    (pašalinant parametras praleidžiamas);
//	D — režimai, keičiantys kanalo nustatymą; NIEKADA su parametru.
//
// KODĖL GRUPĖS ORI REIKIA VISOS: C grupės elgsena PRIKLAUSO nuo ženklo
// („+“ = nustatoma → su parametru; „-“ = pašalinama → be parametro).
// Vienas statinis raidžių sąrašas šito negali atspindėti — todėl
// atsakomybė perkeliama į takesParam().
type chanModeGroups struct {
	a, b, c, d string
}

// parseChanModes skaidra CHANMODES į 4 grupes.
//
// Jei serveris atsiuntė MAŽIAU nei 4 dalis (seni serveriai), trūkstamos
// grupės laikomos tuščiomis — t. y. vertinama pagal „trumpesnę“ formą.
// Kai CHANMODES visai nėra — naudojamas RFC 2811 standartas.
func parseChanModes(chanModes string) chanModeGroups {
	if chanModes == "" {
		chanModes = defaultChanModes
	}
	parts := strings.Split(chanModes, ",")
	var g chanModeGroups
	dst := []*string{&g.a, &g.b, &g.c, &g.d}
	for i, p := range parts {
		if i >= len(dst) {
			break
		}
		*dst[i] = p
	}
	return g
}

// takesParam ardo, ar mode raide (pvz. „o“) šiame kontekste ima parametrą.
//
//	prefixModes — PREFIX modų raidės (nick'ų statusas). VISADA imasi
//	              parametrą (nick'ą).
//	adding      — ar režimas NUSTATOMAS („+“). C grupės režimai imasi
//	              parametrą tik nustatant.
//
// KAD REIKŠMĖS SKAIČIUOTOS: serverio → kliento srautas (kurį tvarkome)
// „+l 50“ — C grupės režimas SU parametru; „-l“ — BE parametro.
func (g chanModeGroups) takesParam(letter rune, adding bool, prefixModes string) bool {
	// Nick'ų statuso modas (o, v, h, q, a) — VISADA su nick'u.
	if strings.ContainsRune(prefixModes, letter) {
		return true
	}
	// A ir B grupės — visada su parametru.
	if strings.ContainsRune(g.a, letter) || strings.ContainsRune(g.b, letter) {
		return true
	}
	// C grupė — tik nustatant.
	if strings.ContainsRune(g.c, letter) {
		return adding
	}
	// D grupė — niekada.
	return false
}

// handleCapNew apdoroja serverio CAP NEW pasiulymą.
//
// Kviečia CAP REQ su mumis pageidaujamais cap'ais arba CAP END, jei
// nieko nereikia.
// ircv3WantedCaps — cap'ų sąrašas, kurį prašome CAP LS ir CAP NEW metu
// (bendras, kad du keliai nesiskirtų).
var ircv3WantedCaps = []string{
	"multi-prefix", "away-notify", "server-time", "echo-message",
	// v0.4.7 — nauji: JOIN su paskyra/realname (extended-join), paskyrų
	// pakeitimo pranešimas (account-notify), vartotojo/hosto pakeitimas
	// (chghost), pakvietimų pranešimai į bendrus kanalus (invite-notify).
	"extended-join", "account-notify", "chghost", "invite-notify",
}

// saslExtUsable — ar SASL EXTERNAL TIKRAI galimas: konfigūruotas IR
// nustatyti sertifikatai IR jungiamasi su TLS (c.mu LAIKOMAS).
func (c *Client) saslExtUsable() bool {
	return c.saslExternal && c.tlsCertPath != "" && c.useTLS
}

// saslDecide — bendra SASL prašymo logika abiejose CAP LS vietose
// (handleCapNew ir inline CAP LS): iki to buvo DUBLIUOTA logika su
// drift'u (vienoje vietoje buvo įspėjimas, kitoje — ne; vienoje —
// saslOk reikšmė, kitoje — ne). c.mu LAIKOMAS.
//
// Įspėjimas (sasl_external be sertifikato/TLS) NEŠTA čia — serverMsg
// ima c.mu, o jis jau laikomas: grąžiname warn, o kvietėjas praneša PO
// atsirakinimo (priešingu atveju — self-deadlock).
//
// EXTERNAL galimas tik su sertifikatu per TLS; kitaip PLAIN
// (slaptažodis); be jokios kredencialos (t.y. nei EXTERNAL, nei
// slaptažodžio) — SASL neprašoma (serveris gautų tuščią PLAIN
// payload'ą).
func (c *Client) saslDecide(available map[string]bool) (request, warn bool) {
	extUsable := c.saslExtUsable()
	if c.saslExternal && !extUsable {
		warn = true
	}
	request = available["sasl"] && (c.saslPass != "" || extUsable) && !c.saslOk
	if request {
		c.capSASL = true
		// Mechanizmas renkamas derybų pradžioje: EXTERNAL (kliento
		// sertifikatas) — kai sukonfigūruotas, kitaip PLAIN.
		if extUsable {
			c.saslMech = "EXTERNAL"
		} else {
			c.saslMech = "PLAIN"
		}
	}
	return
}

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
	for _, want := range ircv3WantedCaps {
		if available[want] {
			// Tik jei dar neaktyvus.
			if !c.activeCaps[want] {
				wanted = append(wanted, want)
			}
		}
	}
	// SASL EXTERNAL sąlygos/mechanizmas — saslDecide (bendra su inline
	// CAP LS). Įspėjimas — PO atsirakinimo (serverMsg ima c.mu).
	req, saslWarn := c.saslDecide(available)
	if req {
		wanted = append(wanted, "sasl")
	}
	c.capAvailable = make(map[string]bool)
	c.mu.Unlock()
	if saslWarn {
		c.serverMsg("sasl_external=true, bet nenurodyti sertifikatai (tls_cert/tls_key) arba TLS išjungtas — SASL EXTERNAL nenaudojamas")
	}

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

// asciiFold — ASCII-only casefold.
//
// Naudojamas, kai serveris paskelbia CASEMAPPING=ascii: lyginami TIK
// ASCII raidės (A–Z → a–z), ne-ASCII (lietuviškos) lieka TOKIOS, kokios
// yra. Tai tiksliai atitinka nickEqual() elgseną „ascii“ atveju (jie abu
// naudoja TĄ PATĮ asciiFold — kitaip rinkinio raktai ir lygybės sprendimai
// išsiskirtų ir nick'ai liktų „ghost'ais“).
//
// strings.ToLower čia NEDAROME — jis lygina ir ne-ASCII raides
// („Žilvinas“ → „žilvinas“), o ASCII serveriui jos yra SKIRTINGOS.
func asciiFold(s string) string {
	hasUpper := false
	for i := 0; i < len(s); i++ {
		if s[i] >= 'A' && s[i] <= 'Z' {
			hasUpper = true
			break
		}
	}
	if !hasUpper {
		return s
	}
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// nickEqual palygina nick'us pagal serverio casemapping
// (RFC: nick'yra didžiųjų/mažųjų raidžių nejausmi, bet [] != {} pagal ASCII).
func (c *Client) nickEqual(a, b string) bool {
	_, _, _, cm, _ := c.isupSnapshot()
	return nickEqual(a, b, cm)
}

// monKey — monitorOnline žemėlapio RAKTAS: nick'as fold'inamas pagal
// serverio casemapping. Serveris 730/731 gali grąžinti kitas raides
// nei įrašėme („Bob“ vs „bob“) — be fold'o vartotojas gautų DU įrašus
// (online „Bob“ + offline „bob“) arba amžiną „nežinoma“.
func (c *Client) monKey(nick string) string {
	_, _, _, cm, _ := c.isupSnapshot()
	return normNick(nick, "", cm)
}

// nickEqual — laisva versija su casemapping parametru (naudojama
// kanale, kur nėra prieigos prie Client).
//
// CASEMAPPING: pagal RFC 1459 lygina „[“ ≡ „{“, „]“ ≡ „}“, „\\“ ≡ „|“;
// pagal „ascii“ — tik ASCII raidės (A–Z → a–z), ne-ASCII SKIRTINGOS.
//
// SVARBŪ: abu atvejai naudoja TĄ PATĮ fold'ą, kokiu nickSet raktai
// sudaromi (normNick):
//   - „ascii“: strings.EqualFold čia NEGALI — jis sulieja ir ne-ASCII
//     raides („Žilvinas“ ≡ „žilvinas“), o rinkinys jų NESULIEJA (asciiFold)
//     → QUIT/parts ieškotų nicko, KURIO rinkinyje NĖRA („ghost nick'ai“:
//     įvykis atrodo suvaldytas, bet nick'as lieka sąraše, nes
//     findNickIdx pagal raktą jo nesuranda).
//   - „rfc1459“: foldEqualIRC rune po rune — tas pats kaip
//     strings.Map(lowerIRC) palyginimas, bet be alokacijų.
func nickEqual(a, b, cm string) bool {
	if a == "" || b == "" {
		return a == b
	}
	return foldEqualIRC(a, b, cm == "ascii")
}

// foldEqualIRC — nick'ų lygybė pagal casemapping'ą BE atminties
// alokacijų.
//
// Naudojama cikluose ant tūkstančių nick'ų (QUIT/PART/MODE per visą
// kanalo sąrašą): strings.Map kiekvienam elementui kurtų po laikinąją
// eilutę — 5 000 QUIT'ų 5 000-nick kanale tai milijonai alokacijų ir
// kliento užstrigimas.
func foldEqualIRC(a, b string, ascii bool) bool {
	if ascii {
		// ASCII fold baitais — UTF-8 ne-ASCII baitai niekada nepatenka
		// į 'A'-'Z' diapazoną (0x41–0x5A), todėl jie lieka nesugadinti.
		if len(a) != len(b) {
			return false
		}
		for i := 0; i < len(a); i++ {
			ca, cb := a[i], b[i]
			if ca >= 'A' && ca <= 'Z' {
				ca += 'a' - 'A'
			}
			if cb >= 'A' && cb <= 'Z' {
				cb += 'a' - 'A'
			}
			if ca != cb {
				return false
			}
		}
		return true
	}
	// RFC 1459 — rune po rune. strings.Map(lowerIRC, a) ==
	// strings.Map(lowerIRC, b) ekvivalentas, tik be alokacijų.
	ai, bi := 0, 0
	for {
		ra, sa := utf8.DecodeRuneInString(a[ai:])
		rb, sb := utf8.DecodeRuneInString(b[bi:])
		if sa == 0 && sb == 0 {
			return true
		}
		if sa == 0 || sb == 0 {
			return false
		}
		if lowerIRC(ra) != lowerIRC(rb) {
			return false
		}
		ai += sa
		bi += sb
	}
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
	saslExternal bool
	tlsCertPath  string // kliento sertifikatas (SASL EXTERNAL)
	tlsKeyPath   string
	tlsPinning   bool   // PINNing (TOFU) — sertifikato atspaudo tikrinimas
	tlsPin       string // rankinis atspaudas (sha256 hex) — pirmenybė prieš TOFU
	saslMech     string // "PLAIN" / "EXTERNAL" — koks mechanizmas STARTED
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

	// MONITOR (/notify, v0.4.8): stebimų nick'ų sąrašas + būsena.
	monitorList   []string
	monitorOnline map[string]bool

	// Klavišų perrašymas (keys failas, v0.4.8): valdymo seka →
	// veiksmo VARDAS (keyActions raktas). Kraunama VIENĄ KARTĄ paleidime
	// (loadKeys) ir toliau tik SKAITOMA — klaviatūros goroutine yra
	// vienintelis skaitytojas.
	customBinds map[string]string

	highlightWords []string // papildomi highlight žodžiai

	// Away throttle: nick -> paskutinio pranešimo laikas
	//
	// RIBOTA: priešiškas serveris gali siųsti AWAY nuo milijono skirtingų
	// nick'ų — be ribos žemėlapis augtu be galios. Privaloma riba: kai
	// viršijama, seniausi įrašai ištrinami (žr. pruneAwayNotified).
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

	// chanLimitHit — kanalo pavadinimas, kurio ŽINUTĖ atmesta dėl kanalų
	// ribos. Naudojama, kad vartotojas gautų ŽINIĄ, kad pranešimas nebuvo
	// pristatytas — anksčiau žinutės tyliai išnykdavo.
	//
	// LAIKYTI pavadinimą (o ne bool) — kad įspėjimas būtų PRISKIRTAS
	// TEISINGAM kanalui. Anksčiau buvo globalus „lipni“ žymeklis: riba
	// pasiekus viename kanale, įspėjimą gaudavo VISI VELIAU atmettos
	// žinutės (net kito kanalo).
	chanLimitHit string
	lastDrawn    *Channel // kuriuo kanalu paskutinį kartą braižyta (istorijos užšaldymui)

	writeCh chan string
	// writeDone — rašytojos goroutine užsibaigimo signalas (po buferio
	// flush). writeStopFn — dvigubam close apsaugotas writeStop uždarymas
	// (/quit + nutrūkimas tvarko tą pačią jungtimi).
	writeDone   chan struct{}
	writeStopFn func()
	redraw      chan struct{}

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
		saslExternal:    cfg.SASLExternal,
		tlsCertPath:     expandHomePath(cfg.TLSCert),
		tlsKeyPath:      expandHomePath(cfg.TLSKey),
		tlsPinning:      cfg.TLSPinning,
		tlsPin:          cfg.TLSPin,
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
		monitorOnline:   make(map[string]bool),
		customBinds:     make(map[string]string),
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

// sanitizeChanName normalizuoja kanalo pavadinimą iš serverio.
//
// SANITIZUOJAMA NE TIK TERMINALO VALDYMO SIMBOLIAI, bet ir TARPAS.
//
// Kodėl: kanalo pavadinimas įrašomas į IRC komandas (MODE #kanalas +o nick,
// KICK #kanalas nick, TOPIC #kanalas :tema, /op, /part, /msg). Tarpas IRC
// komandoje yra PARAMETRŲ SKIRTUKAS — todėl kanalas „#pub #priv“ (atsiųstas
// priešiško serverio arba per JOIN :#pub #priv) sukeldavo komandų tikslo
// klastojimą: „MODE #pub #priv +o attacker“ — operatoriaus teisės suteikiamos
// KITAME kanale.
//
// Čia trimamas tik VIDINIS tarpas (apvaliu ir kelių tarpų serija) — viena
// eilutė, kad kanalo pavadinimas liktų žmoniui atpažįstamas. Bet atsparus
// komandų tikslo klastojimui to NEDAROM visų tarpų pašalinimo: kanalo
// pavadinimas turi būti PRIEŠ serverio atsiųstas tikslus pavadinimas
// (serverio atsiųstas pavadinimas su tarpu neturi teisės tapti komandos
// tikslu — todėl toks kanalas atmetamas).
//
// Grąžina tuščią eilutę, jei pavadinimas netinkamas (tuščias, su tarpais,
// arba su komandų atskyrimo simboliais).
func sanitizeChanName(name string) string {
	// BALTIEJI ir CRLF tikrinami ORIGINALIOJE pavadinime — PRIE
	// sanitizavimo. Kitaip „#a\nb“ po sanitizeText() taptų „#ab“ ir
	// praeitų patikrą — o serverio tikslas (koks kanalas iš tikrųjų)
	// vis tiek būtų kitas.
	if strings.ContainsAny(name, " \t\r\n\v\f") {
		return ""
	}
	safe := sanitizeText(name)
	if safe == "" {
		return ""
	}
	// Po sanitizavimo dar kartą — jei sanitizavimas sukūrė tarpą
	// (nepavyktų, bet saugiai).
	if strings.ContainsAny(safe, " \t\r\n") {
		return ""
	}
	return safe
}

// maxChannels – didžiausias REALIŲ kanalų (langų) skaičius viename seanse.
//
// Be ribos priešiškas serveris galėtų siųsti JOIN flood — klientas kurstų
// šimtus kanalų (atmintis + sidebar).
const maxChannels = 100

// maxPrivateChannels – didžiausias PRIVAČIŲ pokalbių (PM) langų skaičius.
//
// ATSKIRAS biudžetas nuo maxChannels.
//
// Kodel? Ankstesnė riba (maxChannels) taikoma VISIEMS kanalams, įskaitant
// PM. Todėl 100 privačių pokalbių (PM flood) IŠNAUDO visą biudžetą, ir po
// to REALUS kanalas nebebuvo sukuriamas: jo žinutės TYLIAI išnykdavo
// (addMsg → ensureCh → nil → return). Vartotojas manydavo, kad kanale
// nieko nėra, nors serveris siuntė pranešimus.
//
// Dabar PM flood eikdoja tik privačių pokalbių biudžetą — realūs kanalai
// visada turi vietos.
const maxPrivateChannels = 200

// ensureCh sukuria kanalą, jei jo nėra. Tuščių pavadinimų nepriima —
// serverio komanda be kanalo parametro (pvz. „:srv 332 :topic“) neturi
// teisės sukurti kanalo su tuščiu pavadinimu.
//
// Pavadinimas sanitizuojamas ČIA, kai kanalas sukuriamas: priešiškas
// serveris gali atsiųsti PRIVMSG su ESC simboliais nick'e — jei tokie
// simboliai liktų ch.name, jie būtų braižomi į ekraną (terminalo
// injekcija) ir rašomi į žurnalų failų vardus.
//
// TIK SU c.mu užrakintu.
func (c *Client) ensureCh(name string) *Channel {
	if name == "" {
		return nil
	}
	ch := c.findCh(name)
	if ch != nil {
		return ch
	}
	// Sanitizuojame PRIEŠ sukurdami — kad ch.name (kuris braižomas į ekraną
	// ir naudojamas žurnalų failų vardams) niekada nebūtų su terminalo
	// valdymo simboliais ar kelio elementais.
	//
	// BE TARPŲ: kanalo pavadinimas įrašomas į IRC komandas kaip TIKSLAS.
	// Tarpo kanalas („#pub #priv“) komandoje būtų interpretuojamas kaip
	// papildomas parametras → komandų tikslo klastojimas (MODE #pub #priv
	// +o attacker). Tokį kanalą atmetame — jo serverio tikslas yra kitas.
	safe := sanitizeChanName(name)
	if safe == "" {
		// Pavadinimas susidėjo tik iš valdymo simbolių arba turi tarpų —
		// tokio kanalo nekūriame (kitaip atsirastų tuščias / beprasmis
		// langas, arba kanalas su klastotu komandos tikslu).
		return nil
	}
	// RIBOS — atskiri biudžetai realiems kanalams ir privatiems pokalbiams.
	ct, _, _, _, _ := c.isupSnapshot()
	isRealChannel := strings.ContainsRune(ct, rune(safe[0]))
	realCount, privateCount := c.countChannelsLocked()
	if isRealChannel {
		if realCount >= maxChannels {
			// Išsaugome ATSISAKYTO kanalo vardą — įspėjimas turi būti
			// tikslus (o ne „pakibęs“ ant kitos vėlesnės žinutės).
			c.chanLimitHit = safe
			return nil
		}
	} else {
		// Privatūs pokalbiai (PM). Jie turi SAVO biudžetą — kad PM
		// flood neužimtų vietos realiems kanalams.
		if privateCount >= maxPrivateChannels {
			c.chanLimitHit = safe
			return nil
		}
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

// countChannelsLocked skaičiuoja realius kanalus ir privačius pokalbius.
//
// TIK SU c.mu užrakintu. Privatiems pokalbiams nepriskaitome vidinio
// „*server*“ lango — jis visada egzistuoja ir nėra vartotojo pokalbis.
func (c *Client) countChannelsLocked() (real, private int) {
	ct, _, _, _, _ := c.isupSnapshot()
	for _, ch := range c.channels {
		if ch.name == "*server*" {
			continue
		}
		if strings.ContainsRune(ct, rune(ch.name[0])) {
			real++
		} else {
			private++
		}
	}
	return real, private
}

// activeChan grąžina aktyvų kanalą (arba nil, jei aktyvaus kanalo nėra).
//
// SAUGUMAS: ši funkcija TIKRINA c.activeIdx ir c.channels BE c.mu rakino —
// tai duomenų lenktynė (parseIRC gorutina keičia c.channels per JOIN/PART,
// o UI/klaviatūros gorutina skaito c.activeIdx).
//
// Todėl atskirai yra DU saugios versijos:
//   - activeChanLocked() — TIK kai jau laikomas c.mu (NE BLOKUOKITE —
//     c.mu yra neperkeliama, o ne RWMutex).
//   - activeChanSafe()  — kai rakinio laikyti negalima (nuolat naudojamas).
//
// NAUDOKITE activeChanSafe() visur, kur rakinio negalima turėti.
func (c *Client) activeChan() *Channel {
	if c.activeIdx < 0 || c.activeIdx >= len(c.channels) {
		return nil
	}
	return c.channels[c.activeIdx]
}

// activeChanLocked grąžina aktyvų kanalą TIK kai jau laikomas c.mu.
//
// ĮSPĖJIMAS: negalima šioje funkcijoje užrakinti c.mu — visi „Locked“
// variantai naudojami iš vietų, kur c.mu jau užrakintas (užrakintų c.mu
// kai kuriuose keliuose (pvz. addMsg) — dar kartą užrakinti = deadlock).
func (c *Client) activeChanLocked() *Channel {
	if c.activeIdx < 0 || c.activeIdx >= len(c.channels) {
		return nil
	}
	return c.channels[c.activeIdx]
}

// activeChanSafe grąžina aktyvų kanalo NUORODĄ su užrakintu c.mu.
//
// Grąžinamas pats *Channel objektas (ne kopija) — todėl jo laukus (nicks,
// unread, topic) vis dar reikia skaityti tik su užrakintu c.mu arba nukopijuoti
// (žr. activeSnapshot). Tačiau PATS kanalas (ir jo pavadinimas) jau
// nekyla — removeCh negali jo pašalinti iš c.channels, nes c.channels
// elementas perrašomas tik su c.mu.
func (c *Client) activeChanSafe() *Channel {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.activeChanLocked()
}

// activeName grąžina aktyvaus kanalo pavadinimą (arba "" jei nėra).
//
// SAUGUMAS: imamas su c.mu — žr. activeChanSafe.
func (c *Client) activeName() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ch := c.activeChanLocked(); ch != nil {
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
		//
		// RIBOS ATMETA — pranešimas PRARADIMAS. Anksčiau jis tyliai
		// išnykdavo (vartotojas manydavo, kad kanale nieko nėra).
		// Dabar bent vieną kartą pranešame, kad žinutė nepristatyta.
		//
		// Bet įspėjimo chanLimitHit NENAUDOJAME bet kokiam atmetimui:
		// ensureCh jį nustato TIK kai atmetimas dėl RIBOS. Kanalo
		// pavadinimas su tarpais ar valdymo simboliais atmetamas KITOS
		// priežasties — jei jo atžvilgiu sunaudotume pasiliktą kito
		// kanalo įspėjimą, vartotojas gautų apgavystę („limitas“ ant
		// nesusijusio klastoto tikslo). Todėl įspėjimą sunaudojame tik
		// tada, kai įsimintas vardas atitinka ŠITĄ kanalą.
		// (c.mu JAU užrakintas — addMsg pradžioje.)
		limitHit := ""
		if hit := c.chanLimitHit; hit != "" && hit == sanitizeChanName(chanName) {
			c.chanLimitHit = ""
			limitHit = hit
		}
		// ĮSPĖJIMAS nurodo TIKSLŲ kanalą (limitHit), o ne atmetimo kanalą
		// — jie gali skirtis (pvz. riba pasiekta kanale A, o žinutė
		// atmesta kanale B). serverMsg() per addMsg įeina su RAKINTU —
		// todėl pranešimas iškviečiamas ATVERTAS TARPELYJE.
		c.mu.Unlock()
		if limitHit != "" {
			c.serverMsg("ĮSPĖJIMAS: pasiektas kanalų limitas — žinutė NEPRISTATYTA (" + sanitizeText(limitHit) + ")")
		}
		return
	}
	// Naudojame SANITIZUOTĄ pavadinimą — originalį gali atsiųsti priešiškas
	// serveris (ESC simboliai → terminalo injekcija, kelias už logDir ribų).
	chanName = ch.name
	ch.addMsg(m)
	// c.mu jau užrakintas (addMsg pradžioje) — naudojame Locked variantą.
	isActive := c.activeChanLocked() == ch
	if !isActive {
		// Pirmosios neperskaitytos žinutės riba — grįžus į kanalą prieš ją
		// įstatysime „── naujos žinutės ──“ (switchCh). Po ch.addMsg (ir
		// galimo limito „priptalpimo“) — len(messages)-1 yra TIKSLUS
		// šios žinutės indeksas.
		if ch.unread == 0 {
			ch.unreadFrom = len(ch.messages) - 1
		}
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
		// c.mu jau užrakintas — Locked variantas.
		if cur := c.activeChanLocked(); cur != nil {
			cur.scrollOff = c.scrollOff
		}
		c.activeIdx = idx
		// Atstatyti išsaugotą poziciją
		c.scrollOff = c.channels[idx].scrollOff
		target := c.channels[idx]
		if target.unread > 0 && target.unreadFrom > 0 && target.unreadFrom < len(target.messages) {
			// „── naujos žinutės ──“ žyma prieš pirmą neskaitytą žinutę —
			// grįžus į kanalą aišku, kur nutrūkote (v0.4.7).
			sep := Message{ts: time.Now(), text: "── naujos žinutės ──", msgType: MsgServer}
			target.messages = append(target.messages, Message{})
			copy(target.messages[target.unreadFrom+1:], target.messages[target.unreadFrom:])
			target.messages[target.unreadFrom] = sep
			target.renderGen++
			// Iškirtinis: žinutė buvo įrašyta Į VIDURĮ — galo incremental
			// cache'as (renderedMsgCount) netgalioja; išvalome eilutes,
			// kitaip žyma atsidurtų pabaigoje, o viena žinutė dubliuotųsi.
			target.rendered = nil
			target.renderedMsgCount = 0
		}
		target.unread = 0
		target.highlight = 0
		target.unreadFrom = -1
		c.nickScrollOff = 0
		c.searchMode = false
		// Paieškos būsena — KIEKVIENAM langui savo kontekste: pereinant
		// į kitą kanalą indeksas/query išvalomi (seniau indeksas likdavo
		// iš kito lango — „n“/„p“ pralėkdavo per NE TĄ žinutes).
		c.searchResultIdx = -1
		c.lastSearchQuery = ""
		// Kanalų sąrašo slinktis — aktyvus kanalas turi būti MATOMAS.
		//
		// Eilučių skaičius skaičiuojamas TAIP PAT, kaip draw() (žr.
		// visibleChannelRowsLocked): temos/paieškos juostos keičia
		// msgAreaTop. Anksčiau čia buvo KiETA c.height-4 — ji neatitiko
		// draw() be temos arba su paieškos juosta → slinktis drift'indavo
		// nuo to, ką iš tikrųjų rodo ekranas.
		if idx < c.chanScrollOff {
			// Peršokome į viršų — slinktis grįžta, kad kanalas matytųsi.
			c.chanScrollOff = idx
		} else if idx >= c.chanScrollOff+c.visibleChannelRowsLocked() {
			// Peršokome žemyn — slinktis nusikelia iki kanalo.
			c.chanScrollOff = idx
		}
		// O kai aktyvus kanalas lieka MATOMU langelyje — offsetas vis tiek
		// turi būti leistinose ribose (kitaip handleMouse spustelėjimas
		// perskaičiuotų NETEISINGĄ indeksą ir pasirinktų ne tą kanalą).
		c.clampChanScrollLocked()
	}
	c.mu.Unlock()
	c.queueRedraw()
}

// visibleChannelRowsLocked apskaičiuoja, KIEK kanalų eilučių draw()
// atvaizduos šoninėje juostoje (msgRows) su DABARTINE būsena. Pavyzdys
// turi sutapti su msgAreaTop skaičiavimu draw() (ui.go):
//
//	statuso juosta: 1 eilutė (row=1) → pirmoji sąrašo eilutė yra row=2
//	temos juosta:   +1 (tik kai aktyviame kanale tema netuščia)
//	paieškos juosta:+1 (tik kai ieškoma)
//
// Taigi msgAreaTop = 2 + (tema? 1 : 0) + (paieška? 1 : 0), o msgRows =
// (h-2) - msgAreaTop + 1 = h - 1 - msgAreaTop.
//
// TIK SU c.mu užrakintu (naudoja activeChanLocked).
func (c *Client) visibleChannelRowsLocked() int {
	top := 2
	if cur := c.activeChanLocked(); cur != nil && cur.topic != "" {
		top++
	}
	if c.searchMode {
		top++
	}
	rows := c.height - 1 - top
	if rows < 1 {
		rows = 1
	}
	return rows
}

// clampChanScrollLocked suvaržo chanScrollOff iki [0, len(channels)-rows].
//
// Offsetas gali pasenti net nespaudus nieko: terminalas PERDARYMAS
// (sumažėjęs aukštis) sumažina msgRows, tad senas offsetas tampa per
// didelis. draw() sugrubina tik SAVO vietinę kopiją — o handleMouse
// naudoja įRAŠYTĄ vertę perskaičiuodamas indekso poziciją, todėl
// KIEKVIENAS naudotojas turi būti sugrubintas (kitaip spustelėjimas
// perskaičiuoja NETEISINGĄ indeksą ir pasirenka ne tą kanalą).
//
// TIK SU c.mu užrakintu (naudoja visibleChannelRowsLocked → c.mu).
func (c *Client) clampChanScrollLocked() {
	maxChanScroll := len(c.channels) - c.visibleChannelRowsLocked()
	if maxChanScroll < 0 {
		maxChanScroll = 0
	}
	if c.chanScrollOff > maxChanScroll {
		c.chanScrollOff = maxChanScroll
	}
	if c.chanScrollOff < 0 {
		c.chanScrollOff = 0
	}
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
	//
	// Raktas naudoja SANITIZUOTĄ kanalo pavadinimą — tas pats, kas failo
	// vardas diske. clearLogs() atranda failus pagal tą patį raktą, todėl
	// abu turi sutapti (nesanitizuotas raktas pritrūkdavo prie
	// uždarymo — atviru failu likdavo neuždaryti).
	key := server + "/" + sanitizePath(chanName) + "/" + m.ts.Format("2006-01-02")
	c.mu.Lock()
	f, ok := c.logFiles[key]
	if !ok {
		dir := filepath.Join(logDir, sanitizePath(server))
		if err := os.MkdirAll(dir, 0700); err != nil {
			c.mu.Unlock()
			return
		}
		// uždarome ankstesnės dienos failą tos pačios kanalo eilutės
		// (prefiksas — su sanitizuotu vardu, kaip ir pats raktas)
		oldPrefix := server + "/" + sanitizePath(chanName) + "/"
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
	// Slaptažodžio aptikimas ŽURNALE.
	//
	// NAUDOJAME isSensitiveLogLine (ne isSensitiveHistory): žurnale yra ir
	// ĮPRASTI POKALBIAI, o platus „pirmas žodis“ tikrinimas slėpdų viską,
	// kas pradžiasi žodžiais „register“/„pass“/„auth“ — t. y. žurnalas
	// tyliai prarastų realų pokalbių tekstą („register your vote today“).
	//
	// Tikriname kūną, NICK'o lauką (serveris siunčia „:NickServ!serv@host
	// PRIVMSG man :set password …“ — nick'o laukas yra „NickServ“) ir
	// junginį (atpažinti komandą, kai kontekstas atskirtas).
	//
	// NICK'O LAUKAS gali būti „NickServ“ (serveris siunčia „:NickServ!serv@host
	// PRIVMSG man :set password …“) — tada VISAS kūnas yra komanda, todėl
	// jį tikriname kaip NickServ komandą (platis „pirmas žodis“ tikrinimas
	// čia leistinas — kontekstas aiškus).
	// EqualFold neskiria raidžių dydžio — vienas kvietimas dengia ir
	// „NickServ“, ir „nickserv“ (anksčiau trys dublioti variantai).
	nickIsServ := strings.EqualFold(nick, "NickServ")
	sensitive := isSensitiveLogLine(plain) ||
		isSensitiveLogLine(nick) ||
		isSensitiveLogLine(plain+" "+nick)
	if nickIsServ {
		// Kūnas yra NickServ komanda — tikrinime PLACIAI (be konteksto
		// komandos, pvz. „register <pass>“).
		sensitive = sensitive || isSensitiveHistory(plain)
	}
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
	if sensitive {
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
		// Raktas: server/kanalas/data.
		//
		// SANITIZUOJAME čia pat: failų VARDAI diske yra sanitizuoti
		// (sanitizePath), o raktas c.logFiles — tai server/kanalas/data
		// su ORIGINALIU kanalo pavadinimu. Jei šie skirtųsi, atviras
		// failas likdavo neuždarytas (ir rašė į ištrintą failą).
		prefix = server + "/" + sanitizePath(chanName) + "/"
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
		if filePrefix != "" && !isChannelLogFile(e.Name(), filePrefix) {
			continue
		}
		if os.Remove(filepath.Join(srvDir, e.Name())) == nil {
			removed++
		}
	}
	return removed
}

// isChannelLogFile tikrina, ar failo vardas yra LYGIAI nurodyto kanalo
// žurnalas.
//
// Failų vardai: <KANALAS>-YYYY-MM-DD.log (ir senieji <KANALAS>.log).
//
// Ankstesnė patikra buvo `strings.HasPrefix(name, kanalas+"-2")` — ji
// tikėjosi, kad data visada prasideda „2“ (metais 2xxx). Tai sukeldavo
// dviejų klaidų:
//   - „/clearlogs #k“ ištrindavo kito kanalo „#k-2“ žurnalą
//     (jo failas „#k-2-2026-10-21.log“ — prefiksas sutampa);
//   - senesnių metų žurnalai („#k-2025-03-15.log“) NEBUDavo ištrinami.
//
// Dabar tikrinama tiksli struktūra: prefiksas sutampa VISAS, o data
// turi būti reali YYYY-MM-DD data.
func isChannelLogFile(name, chanPrefix string) bool {
	// Senasis formatas (be datos).
	if name == chanPrefix+".log" {
		return true
	}
	if !strings.HasPrefix(name, chanPrefix+"-") {
		return false
	}
	rest := name[len(chanPrefix)+1:]
	if !strings.HasSuffix(rest, ".log") {
		return false
	}
	date := strings.TrimSuffix(rest, ".log")
	if len(date) != len("2006-01-02") {
		return false
	}
	_, err := time.Parse("2006-01-02", date)
	return err == nil
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

// finishWrites praneša aktyvios jungties rašytojai goroutine baigti darbą
// (buferio flush) ir jos palaukia (daugiausia timeout) — naudojama /quit'e,
// kol procesas dar gyvas: kitaip QUIT eilutė gali likti rašymo buferio
// viduje ir kiti vartotojai QUIT nematys. Dvigubas writeStop uždarymas
// (iš čia ir iš nutrūkimo tvarkos) apsaugotas sync.Once.
func (c *Client) finishWrites(timeout time.Duration) {
	c.mu.Lock()
	stopW, done := c.writeStopFn, c.writeDone
	c.mu.Unlock()
	if stopW != nil {
		stopW()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(timeout):
		}
	}
}

// hasCap — ar IRCv3 cap'as aktyvus (perskaitoma po rakinimu).
func (c *Client) hasCap(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.activeCaps[name]
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

// fingerprintsPath — atspaudų failas (TOFU pinning, v0.4.8):
// configDir/fingerprints (eina su 0600 teisėmis kūrimo metu).
func fingerprintsPath() string {
	dir := configDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "fingerprints")
}

// loadTLSPins — atspaudai iš failo: kiekviena eilutė „host:sha256hex“.
// Sugedusios eilutės praleidžiamos (failą gali būt pažeidęs vartotojas).
func loadTLSPins() map[string]string {
	pins := make(map[string]string)
	path := fingerprintsPath()
	if path == "" {
		return pins
	}
	f, err := os.Open(path)
	if err != nil {
		return pins
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.IndexByte(line, ':')
		if i <= 0 || i == len(line)-1 {
			continue
		}
		pins[strings.ToLower(line[:i])] = strings.ToLower(line[i+1:])
	}
	return pins
}

// saveTLSPin — pirmo jungimo atspaudas išsaugomas (TOFU „Trust On First
// Use“): failo prieda „host:sha256hex“, failas/katalogas 0600/0700.
func saveTLSPin(host, fp string) {
	path := fingerprintsPath()
	if path == "" {
		return
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "%s:%s\n", strings.ToLower(host), fp)
}

// certFingerprint — TLS subjekto VIEŠAJO RAKTO informacija (SPKI —
// RawSubjectPublicKeyInfo) sha256 atspaudas hex'u. SPKI — pinning'o
// praktika (sertifikato atnaujinimas su ta pačiu rakto pora atspaudą
// IŠLAIKO); kertame TOS pačios rakto poros, todėl nepiktam atnaujinimui
// niekur nereikia rankinio įsikišimo.
func certFingerprint(conn *tls.Conn) string {
	st := conn.ConnectionState()
	if len(st.PeerCertificates) == 0 {
		return ""
	}
	sum := sha256.Sum256(st.PeerCertificates[0].RawSubjectPublicKeyInfo)
	return hex.EncodeToString(sum[:])
}

// checkTLSPin (v0.4.8) — TLS atspaudas PINNING'as. Šiems atvejams NIEKO
// nedarome (pinning'išjungta): nesukonfigūruotas.
//  1. tls_pin rankinis — būtinai tas pats atspaudas (nesutapimo atveju klaida).
//  2. tls_pinning=on ir atpaudo failo įrašo nera — TOFU: saugomas.
//  3. tls_pinning=on ir įrašas yra — tikslus tapatumo tikrinimas.
func (c *Client) checkTLSPin(conn *tls.Conn, host string) error {
	if !c.tlsPinning && c.tlsPin == "" {
		return nil
	}
	hostLow := strings.ToLower(host)
	fp := certFingerprint(conn)
	if fp == "" {
		return errors.New("atspaudo negalima gauti (serveris be sertifikato?)")
	}
	if c.tlsPin != "" {
		if strings.ToLower(c.tlsPin) != fp {
			return fmt.Errorf("tls_pin nesutampa: nustatytas %s, gautas %s", strings.ToLower(c.tlsPin), fp)
		}
		return nil
	}
	pins := loadTLSPins()
	old, ok := pins[hostLow]
	if ok {
		if old != fp {
			return fmt.Errorf("saugotas %s, priešingas %s", old, fp)
		}
		return nil
	}
	saveTLSPin(hostLow, fp)
	c.serverMsg("TLS: serverio atspaudas pirmą kartą išsaugotas (" + fp[:16] + "…)")
	return nil
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
		c.mu.Lock()
		pinningOn := c.tlsPinning || c.tlsPin != ""
		c.mu.Unlock()
		if pinningOn {
			// TLS PINNING (TOFU): standartinė patikra per CA su saviraščiu
			// serverio sertifikatu nepereina — jungtis nutrūktų dar iki
			// atspaudo tikrinimo ir TOFU niekada nesuveiktų. Todėl su
			// pinning'u standartinę patikrą IŠJUNGIAME: tapatybės ženklas
			// čia yra IŠSAUGOTAS atspaudas (SPKI), o ne CA parduotuvė.
			tlsCfg.InsecureSkipVerify = true
		}
		// SASL EXTERNAL (v0.4.8): kliento sertifikatas/raktas TLS
		// rankos paspaudime. Neteisingi keliai — jungtis NEVYKDOMA (aiški
		// klaida, o ne tyla: kitaip EXTERNAL'as tyliai griūtų į PLAIN).
		if c.tlsCertPath != "" {
			cert, certErr := tls.LoadX509KeyPair(c.tlsCertPath, c.tlsKeyPath)
			if certErr != nil {
				c.errMsg("TLS kliento sertifikatas neišsiskaitoma: " + certErr.Error())
				return
			}
			tlsCfg.Certificates = []tls.Certificate{cert}
		}
		tlsConn, tlsErr := tls.DialWithDialer(&net.Dialer{Timeout: 15 * time.Second}, "tcp", srv, tlsCfg)
		conn, err = tlsConn, tlsErr
		if tlsErr == nil {
			// TLS PINNING (TOFU, v0.4.8): tikriname pirmąjį serverio
			// sertifikatą (viešojo rakto SPKI sha256). Nesutapimas —
			// jungtis NUTRAUKIAMA iki NICK/USER (identitas ir
			// slaptažodžiai nepasiekia serverio).
			//
			// TOFU: pirmas jungimas — atspaudas IŠSAUGOMAS į
			// fingerprints failą; sekantys jungimai tikrinami
			// (nesutapimas = galimas MITM).
			if fpErr := c.checkTLSPin(tlsConn, srvName); fpErr != nil {
				c.errMsg("TLS atspaudo nesutapimas: " + fpErr.Error())
				c.errMsg("Serverio sertifikatas pasikeitė! Galimas MITM. (tls_pinning=off išjungti tikrinti, žinodami ką darote)")
				conn.Close()
				return
			}
		}
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
	var writeStopOnce sync.Once
	stopWrites := func() { writeStopOnce.Do(func() { close(writeStop) }) }
	writeDone := make(chan struct{})
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
	c.writeDone = writeDone
	c.writeStopFn = stopWrites
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
		// writeDone uždaromas VISADA išėjimo metu — PO buferio flush. Per jį
		// /quit laukia, kad QUIT eilutė tikrai išsiųsta (procesas priešingu
		// atveju gali pasibaigti anksčiau nei rašytoja spėja flush'inti).
		defer close(writeDone)
		sent := 0 // eilutės šioje sekundėje
		t0 := time.Now()
		for {
			select {
			case line, ok := <-newWriteCh:
				if !ok {
					buf.Flush()
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

	// ŠKAITIKLIO BUFERIS didesnis nei IRC 512 eilutės limitas: IRCv3
	// serverio pridėti tag'ai (server-time, msgid) NEĮEINA į eilutės
	// 512 baitų biudžetą, todėl realios žinutės su tag'ais viršija 512
	// baitus — su 512 bufferiu jos sukelia ErrBufferFull ir būtų ATMESTOS
	// (pokalbis prarastas). MaxReadBuffer = 4096 — eilutės be tag'ų iki
	// 512, o su tag'ais iki ~4 KB.
	//
	// Ilgesnė nei buferio eilutė sukelia ErrBufferFull → apleidžiama ir
	// įspėjama vartotojui (žr. apačioje).
	reader := bufio.NewReaderSize(conn, maxReadBuffer)
	c.readLoop(reader)

	close(pingStop)
	stopWrites()
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

// maxAwayNotified – didžiausias awayNotified žemėlapio dydis.
//
// Žemėlapis saugo nick'ų, kuriems jau pranešta „yra away“ (throttle 10 min.
// langyje). Priešiškas serveris gali siųsti AWAY nuo šimtų tūkstančių
// skirtingų nick'ų — be ribos atmintis augtu neapribotai.
const maxAwayNotified = 5000

// pruneAwayNotifiedLocked apriboja awayNotified žemėlapio dydį.
//
// TIK SU c.mu užrakintu. Ištrinami įrašai, kurie jau „nebevėkia“
// (senesni nei throttle langas) — o jei to nepakanka, trininami seniausi.
//
// Ištrynimo eilė deterministinė: renkame seniausius laikus ir triname
// iki ribos.
func (c *Client) pruneAwayNotifiedLocked(now time.Time) {
	// Riba tikrinama SU ATVERTU TARPELIU — jei viršijimas mažas, nesi-
	// reikia nei rūšiuoti, nei ieškoti seniausių. Be to AWAY srautas
	// (priešiškas serveris siunčia AWAY nuo tūkstančių nick'ų) trig-
	// gintų pruininimą KIEKVIENĄ kartą — o kiekvienas pruininimas yra
	// O(n log n) rūšiavimas.
	const slack = maxAwayNotified / 4
	if len(c.awayNotified) <= maxAwayNotified+slack {
		return
	}
	// 1) Pirma — išvalome įrašus, kuriems throttle jau baigėsi (jie
	//    nebebus naudojami).
	for k, t := range c.awayNotified {
		if now.Sub(t) >= 10*time.Minute {
			delete(c.awayNotified, k)
		}
	}
	if len(c.awayNotified) <= maxAwayNotified {
		return
	}
	// 2) Vis dar per didelis — triname seniausius.
	type entry struct {
		key string
		ts  time.Time
	}
	entries := make([]entry, 0, len(c.awayNotified))
	for k, t := range c.awayNotified {
		entries = append(entries, entry{k, t})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ts.Before(entries[j].ts) })
	excess := len(entries) - maxAwayNotified
	for i := 0; i < excess; i++ {
		delete(c.awayNotified, entries[i].key)
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
		autoJoin := make([]string, len(c.autoJoin))
		copy(autoJoin, c.autoJoin)
		perform := make([]string, len(c.perform))
		copy(perform, c.perform)
		monitor := make([]string, len(c.monitorList))
		copy(monitor, c.monitorList)
		c.mu.Unlock()
		// NickServ IDENTIFY — TIK su explicitia NickServ slaptažodžiu.
		//
		// SAUGUMAS: anksčiau, kai slaptažodžio nebuvo, o SASL nepavyko arba
		// serveris nepasiūlė `sasl` cap, būdavo siunčiamas c.saslPass per
		// NickServ. Bet sasl= yra skirtas BUNCER/BRIDGE autentifikacijai —
		// siųsdamas jį NickServ (kitas servisas) mes LEIŽDOME slaptažodį
		// nutekti trečiajam serveriui, automatiškai, kiekvienam
		// prisijungimui. Slaptažodžiai turi likti TIK tam, kam skirti.
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
		// MONITOR (v0.4.8) — sąrašas apie visiems naujiems prisijungimams:
		// 730 (online) / 731 (offline). Po registracijos — MONITOR +.
		// Limitas MONITOR=N: viršijus serveris grąžina 734 (rodomas) —
		// netyliai išsaugojame tik pirmus N.
		if len(monitor) > 0 {
			c.send("MONITOR + " + strings.Join(monitor, ","))
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
			// Kanalas atmestas. Priežastis gali būti dvi: (a) kanalo
			// pavadinimas netinkamas (klastota komanda) — tada ignoruojame;
			// (b) pasiektas KANALŲ RIBAS — tada vartotojas turi žinoti.
			//
			// Anksčiau JOIN keliu įspėjimo NEBUVO — vartotojas matė
			// „prisijungiau į kanalą“, bet lango nebuvo.
			//
			// Įspėjimą sunaudojame tik tada, kai įsimintas vardas atitinka
			// ŠITĄ JOIN kanalą (žr. addMsg: kitaip klastoto tikslo atmetimas
			// sunaudotų kito kanalo įspėjimą).
			limitHit := ""
			if hit := c.chanLimitHit; hit != "" && hit == sanitizeChanName(ch) {
				c.chanLimitHit = ""
				limitHit = hit
			}
			c.mu.Unlock()
			if limitHit != "" {
				c.serverMsg("ĮSPĖJIMAS: pasiektas kanalų limitas — kanalas „" + sanitizeText(limitHit) + "“ neatidarytas")
			}
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
		// extended-join (v0.4.7): JOIN <kanalas> <paskyra|*> <realname> —
		// paskyra rodoma greta prisijungimo (ne „*“ — nesuregistravęs).
		joinMsg := ""
		if c.hasCap("extended-join") {
			acct := p(1)
			if acct != "" && acct != "*" {
				joinMsg = "(paskyra: " + sanitizeText(acct) + ")"
			}
		}
		c.addMsg(ch, Message{ts: now, nick: fromNick, text: joinMsg, msgType: MsgJoin})

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
		c.mu.Lock()
		// Rasti kanalus kuriuose buvo nick PRIEŠ pašalinimą.
		//
		// O(1) rinkinio patikra (hasNickKey), NE O(n) per visą nick'ų
		// sąrašą su nickEqual kas elementui: priešiškas serveris 5 000
		// QUIT'ų 5 000-nick kanale sukeltų ~25 milijonus lyginimų su
		// strings.Map alokacija kiekvienam (readLoop'as užstringa —
		// o rinkinio raktai suderinti su nickEqual, žr. normNick).
		var affected []*Channel
		for _, ch := range c.channels {
			if ch.hasNickKey(fromNick, ch.prefixSigns()) {
				affected = append(affected, ch)
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
		// NAUJŲ kanalų nekuriame (findCh, ne ensureCh) — kaip 332/353.
		channel := c.findCh(ch)
		if channel != nil {
			channel.removeNick(kicked)
		}
		c.mu.Unlock()
		if channel == nil {
			return
		}
		if c.nickEqual(kicked, me) {
			c.errMsg(fmt.Sprintf("Buvai išmestas iš %s (%s)", ch, reason))
			// Pateikti kanalą kaip neaktyvų, bet neuždaryti lango
		} else {
			c.addMsg(ch, Message{ts: now, nick: fromNick,
				text:    fmt.Sprintf("išmetė %s (%s)", kicked, reason),
				msgType: MsgServer})
		}
		c.queueRedraw()

	case "ACCOUNT":
		// account-notify (v0.4.7): :nick!u@h ACCOUNT <paskyra|*>
		acct := p(0)
		if acct == "" {
			return
		}
		if acct == "*" {
			c.serverMsg(fmt.Sprintf("%s atsijungė nuo paskyros", sanitizeText(fromNick)))
		} else {
			c.serverMsg(fmt.Sprintf("%s paskyra: %s", sanitizeText(fromNick), sanitizeText(acct)))
		}

	case "CHGHOST":
		// chghost (v0.4.7): :nick!oldU@oldH CHGHOST <vartotojas> <hostas>
		newUser, newHost := p(0), p(1)
		if newUser == "" || newHost == "" {
			return
		}
		c.serverMsg(fmt.Sprintf("%s dabar %s@%s", sanitizeText(fromNick), sanitizeText(newUser), sanitizeText(newHost)))

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
			// isupSnapshot grąžina: chantypes, prefixSigns, prefixModes,
			// casemapping, chanModes. CHANMODES yra PENKTAS — o ne
			// ketvirtas (ketvirtas yra casemapping).
			_, signs, modes, _, chanModes := c.isupSnapshot()
			c.mu.Lock()
			ch := c.findCh(target)
			if ch == nil {
				// Kanalo, kurio NESAME, MODE NEKURIA lango (kaip 332/353):
				// žemiau addMsg viduje ensureCh sukurtų langą — priešiškas
				// serveris MODE srautu prikurtų šimtą tuščių langų.
				c.mu.Unlock()
				return
			}
			if len(params) > 2 {
				// params[2..] = argumentai mode raidėms. Privalime skaičiuoti
				// VISUS mode, kurie imasi argumento (o/v/h/q/a nickams; b, k,
				// l, e, I... irgi turi parametrą) — kitaip indeksas „nuklysta“
				// (pvz. MODE #chan +kb nick mask).
				//
				// SĄRAŠAS IMASI IŠ SERVERIO CHANMODES (005) — ne kietai
				// kodintas („blkeI“). Ankstesnė kietai kodinta eilutė
				// netiko serveriams su kitais mode'ais (InspIRCd C/j/L/Q,
				// Charybdis P, Unreal z) — kiekvienas toks mode „nuklydo“
				// VISUS indeksus po savimi, todėl operatoriaus prefiksas
				// atiteko NETEISINGAM argumentui (pvz. „MODE #chan +jo 30
				// bob“ → nick'ams „[@30 bob]“).
				adding := true
				modeIdx := 2
				groups := parseChanModes(chanModes)
				for _, mr := range modeStr {
					switch {
					case mr == '+':
						adding = true
					case mr == '-':
						adding = false
					case groups.takesParam(mr, adding, modes):
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
		// NAUJŲ kanalų nekuriame (findCh, ne ensureCh) — kaip 332/353:
		// priešiškas serveris TOPIC srautu galėtų prikurti langų.
		c.mu.Lock()
		channel := c.findCh(ch)
		if channel != nil {
			channel.topic = newTopic
		}
		c.mu.Unlock()
		if channel == nil {
			return
		}
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
				// echo-message: serveris atsiunčia mūsų pačių žinutes ATGAL.
				// /me atspausdinama lokaliai iš karto (input.go) — serverio
				// AIDAS atsalinti, kitaip veiksmas atsiras DUKART (žinutes
				// PRIVMSG atsalindavo echo filtras žemiau, o ACTION grįždavo
				// anksčiau — iki apsaugos).
				c.mu.Lock()
				actionEcho := c.activeCaps["echo-message"] && c.nickEqual(fromNick, c.nick)
				c.mu.Unlock()
				if actionEcho {
					return
				}
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
		// NAUJŲ kanalų nekuriame (findCh, ne ensureCh): priešiškas
		// serveris 332 srautu galėtų užpildyti šoninę juostą ir atimti
		// kanalų biudžetą (kaip 353 — žr. ten). Temos pranešimą rodome
		// TIK kanale, kuriame iš tikrųjų esame.
		c.mu.Lock()
		channel := c.findCh(ch)
		if channel != nil {
			channel.topic = topic
		}
		c.mu.Unlock()
		if channel == nil {
			return
		}
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
		// Stulpeliai pagal LĄSTELIŲ plotį (padRight, ne %-30s — fmt
		// skaičiuoja runę: ne-ASCII kanalo vardai „kiekio“ stulpelį kiekvienoje
		// eilutėje pastums skirtingai). Ta pati pataisa, kaip 352 (WHO).
		c.serverMsg(fmt.Sprintf("  %s %5s  %s", padRight(sanitizeText(ch), 30), count, topic))
	case "323":
		c.serverMsg("── Sąrašo pabaiga ──")

	case "366":
		// End of NAMES

	case "315": // RPL_ENDOFWHO — WHO sąrašo pabaiga
		c.whoReplyMsg(fmt.Sprintf("── WHO %s: pabaiga ──", p(1)))

	case "352": // RPL_WHOREPLY: <client> <channel> <user> <host> <server> <nick> <flags> :<hopcount> <realname>
		// Anksčiau šis numerikas krisdavo į `default:` ir buvo rodomas
		// žaliai kaip „[352] …“ — vartotojas nematė, kas yra kanale.
		//
		// Stulpeliai ATSKIRAI: kanalo stulpelis pagal LĄSTELIŲ plotį
		// (fitWidth, ne %-20s — kitaip ne-ASCII kanalo vardai kiekvieną
		// eilutę pastums skirtingai) ir flag'as atskirtas TARPŪ — anksčiau
		// „%s%s“ sulipdavo „Hbob“ iš „H“ + „bob“.
		whoChan := p(1)
		whoNick := p(5)
		whoFlags := p(6)
		if whoNick != "" {
			c.whoReplyMsg(fmt.Sprintf("│  %s %s %s",
				fitWidth(sanitizeText(whoChan), 16), sanitizeText(whoFlags), sanitizeText(whoNick)))
		}

	case "354": // RPL_WHOSPCRPL — WHOIS stilingas (whowas)
		whoNick := p(1)
		whoFlags := p(2)
		whoReal := p(7)
		if whoNick != "" {
			c.whoReplyMsg(fmt.Sprintf("│  %s %s — %s", whoFlags, whoNick, whoReal))
		}

	case "367": // RPL_BANLIST: <client> <channel> <mask> <who> <when>
		c.serverMsg(fmt.Sprintf("  ban: %s", p(2)))
	case "368": // RPL_ENDOFBANLIST
		c.serverMsg("── Banų sąrašo pabaiga ──")
	case "369": // RPL_ENDOFWHOWAS
		c.serverMsg("── WHOWAS pabaiga ──")

	case "251": // RPL_LUSERCLIENT
		// Visi skaičiai — VIENAS galinis parametras (p(1) visas sakinys
		// „There are N users and M invisible on K servers“); anksčiau jis buvo
		// paskaidytas į tris laukus ir rodėsi sukiršinta eilutė.
		c.serverMsg("Serveris: " + sanitizeText(p(1)))
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
			for _, want := range ircv3WantedCaps {
				if available[want] {
					wanted = append(wanted, want)
				}
			}
			// SASL sąlygos/mechanizmas — saslDecide (bendra su
			// handleCapNew). Įspėjimas — PO atsirakinimo.
			req, saslWarn := c.saslDecide(available)
			if req {
				wanted = append(wanted, "sasl")
			}
			c.capAvailable = make(map[string]bool)
			c.mu.Unlock()
			if saslWarn {
				c.serverMsg("sasl_external=true, bet nenurodyti sertifikatai (tls_cert/tls_key) arba TLS išjungtas — SASL EXTERNAL nenaudojamas")
			}
			if len(wanted) > 0 {
				c.send("CAP REQ :" + strings.Join(wanted, " "))
			} else {
				c.send("CAP END")
			}
		case "ACK":
			saslAcked := false
			c.mu.Lock()
			for _, cap := range strings.Fields(capsStr) {
				// ATUSISAKYMO ACK („CAP * ACK :-sasl“) — cap'as PAŠALINAMAS.
				// Anksčiau „-“ buvo tiesiog nukerpmas ir atsisakymas
				// AKTYVUODAVO cap'ą: priešiškas serveris, siųsdamas „:-sasl“
				// niekada nesprašęs, apgyndavo saslPending apsaugą — klientas
				// pats siųsdavo AUTHENTICATE PLAIN ir duodu slaptažodį.
				if strings.HasPrefix(cap, "-") {
					delete(c.activeCaps, strings.ToLower(cap[1:]))
					continue
				}
				name := strings.ToLower(cap)
				c.activeCaps[name] = true
				if name == "sasl" {
					saslAcked = true
				}
			}
			// AUTHENTICATE pradedame TIK jei PATYS užsižymėjome sasl'ą
			// (capSASL būdavo nustatomas tik reikalaujant sasl'o su
			// slaptažodžiu arba sasl_external) — serverio savavališkas
			// „ACK sasl“ be mūsų prašymo SASL negali pradėti slaptažodžio
			// dialogo. Mechanizmas: EXTERNAL (sertifikatas) arba PLAIN
			// (slaptažodis) — renkamas derybų pradžioje (saslMech).
			if !c.capSASL || (c.saslMech != "EXTERNAL" && c.saslPass == "") {
				saslAcked = false
			}
			c.mu.Unlock()
			if cont {
				return // bus daugiau ACK eilučių
			}
			if saslAcked {
				c.mu.Lock()
				mech := c.saslMech
				c.saslPending = true
				c.mu.Unlock()
				c.send("AUTHENTICATE " + mech)
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
			mech := c.saslMech
			c.mu.Unlock()
			if !pending {
				// Atsakome tuščiu slaptažodžiu — neatsakymas liktų
				// serverį laukiančioje būsenoje.
				c.send("AUTHENTICATE +")
				return
			}
			if mech == "EXTERNAL" {
				// SASL EXTERNAL (v0.4.8): autentifikacija KIENTO
				// sertifikatu — be slaptažodžio payload'o. Tuščias „+“ =
				// „sertifikatas jau pateiktas TLS rankos paspaudime“.
				// Sertifikatas NETURI payload'o, kai mechanizmas EXTERNAL —
				// jokios atsarginės PLAIN kaitos čia nebūna.
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

	// MONITOR numerikai (v0.4.8): 730 online, 731 offline. Maskos
	// („nick!user@host“) — į nick'ą (prieš „!“). Žemėlapio raktas —
	// FOLDINTAS nick'as (monKey): nickEqual lygybė be įrašo dubliavimo.
	case "730": // RPL_MONONLINE
		for _, mask := range strings.Split(p(1), ",") {
			nick := strings.SplitN(mask, "!", 2)[0]
			nick = sanitizeText(strings.TrimSpace(nick))
			if nick == "" {
				continue
			}
			c.mu.Lock()
			c.monitorOnline[c.monKey(nick)] = true
			c.mu.Unlock()
			c.serverMsg("● " + nick + " prisijungė (žiūrimas)")
		}
	case "731": // RPL_MONOFFLINE
		for _, mask := range strings.Split(p(1), ",") {
			nick := strings.SplitN(mask, "!", 2)[0]
			nick = sanitizeText(strings.TrimSpace(nick))
			if nick == "" {
				continue
			}
			c.mu.Lock()
			delete(c.monitorOnline, c.monKey(nick))
			c.mu.Unlock()
			c.serverMsg("○ " + nick + " atsijungė (žiūrimas)")
		}
	case "732": // RPL_MONLIST
		for _, m := range strings.Split(p(1), ",") {
			if m = sanitizeText(strings.TrimSpace(m)); m != "" {
				c.serverMsg("MONITOR sąrašas: " + m)
			}
		}
	case "733": // RPL_MONLISTEND
		c.serverMsg("MONITOR sąrašo pabaiga")
	case "734": // RPL_MONLISTFULL — sąrašo limitas viršija
		// Formatas: <limit> <maskos,kableliais> :<priežastis> — nick'ų
		// sąrašas yra p(2), o p(1) yra tik limito SKAIČIUS (seniau paėmę
		// p(1) atmetinėjome skaičių „300“ — nieko neatmetėme).
		msg := p(2)
		// Ko nepriėmė — iš c.monitorList ištriname (v0.4.8: kitaip
		// sąraše liktų „stebimi“ nick'ai, kurių serveris NIEKADA
		// nepraneš apie online/offline).
		for _, m := range strings.Split(msg, ",") {
			nick := strings.SplitN(sanitizeText(strings.TrimSpace(m)), "!", 2)[0]
			if nick == "" {
				continue
			}
			c.mu.Lock()
			for i, exist := range c.monitorList {
				if c.nickEqual(exist, nick) {
					c.monitorList = append(c.monitorList[:i], c.monitorList[i+1:]...)
					break
				}
			}
			c.mu.Unlock()
			c.errMsg("MONITOR sąrašas pilnas — „" + nick + "“ nepridėtas")
		}
		c.saveMonitor()

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
					// Riba: be šio žemėlapis augtu be galios (priešiškas
					// serveris siunčia AWAY nuo milijono nick'ų).
					c.pruneAwayNotifiedLocked(now)
					// Nukopijuojame AKTYVAUS KANALO nick'us SU RAKINU — ne
					// po Unlock. Prieš tai `active.nicks` buvo skaitoma be
					// rakino, o parseIRC gorutina tuo pačiu keitė
					// ch.nicks (sort.Slice in-place) — duomenų lenktynė.
					active := c.activeChanLocked()
					var activeName string
					var nicks []string
					var signs string
					if active != nil {
						activeName = active.name
						nicks = append(nicks, active.nicks...)
						signs = active.prefixSigns()
					}
					c.mu.Unlock()
					if active != nil {
						for _, n := range nicks {
							if nickEqual(stripMode(n, signs), fromNick, cm) {
								c.addMsg(activeName, Message{ts: now, nick: fromNick,
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
				// Nukopijuojame nick'us SU RAKINU (žr. AWAY-away atvejo
				// komentarą) — pašalina lenktynę su /names (active.nicks = nil)
				// ir su sort.Slice.
				active := c.activeChanLocked()
				var activeName string
				var nicks []string
				var signs string
				if active != nil {
					activeName = active.name
					nicks = append(nicks, active.nicks...)
					signs = active.prefixSigns()
				}
				c.mu.Unlock()
				if wasAway && active != nil {
					for _, n := range nicks {
						if nickEqual(stripMode(n, signs), fromNick, cm) {
							c.addMsg(activeName, Message{ts: now, nick: fromNick,
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

// isValidChannelKey tikrina kanalo raktą (JOIN komandos parametras).
//
// Raktas įrašomas į „JOIN #kanalas <raktas>“ — t. y. į IRC komandą kaip
// atskirą parametrą. Tarpas yra parametrų skirtukas, todėl rakte su
// tarpais serveris gautų „JOIN #kanalas raktas papildomas“ — papildomą
// (netiesioginį) kanalą, kurio serveris nekvietė. Tai komandų injekcija.
//
// ATMETAME TIK TARPUS IR KONTROLIAUS SIMBOLIUS — visa kita leidžiame.
//
// Ankstesnė versija naudojo siaurą „leistinų simbolių“ sąrašą
// (`[A-Za-z0-9.-_]` + lietuviškos raidės). Tai buvo PER SMETINGA:
// tikri kanalų raktai dažnai turi „!“, „#“, „@“, „,“, „=“, „%“, „*“
// — ir vartotojas negalėdavo prisijungti į tokį kanalą (funkcinė
// regresija, ne saugumo pataisymas).
//
// Tikras pavojus yra VIENINTELIS: tarpas (papildomas JOIN parametras)
// ir CRLF (komandų injekcija). Todėl atmetame:
//   - tarpus (space, tab, CR, LF, VT, FF);
//   - kitus valdymo simbolius (C0 ir DEL, C1 0x80–0x9F).
//
// (send() papildomai pašalina CR/LF — bet tai tik paskutinė linija.)
func isValidChannelKey(key string) bool {
	// Tuščias raktas = rakto NĖRA (tai neleidžiama forma, ne klaida) —
	// „/join #kanalas“ be rakto yra teisinga komanda.
	if key == "" {
		return true
	}
	if len(key) > 64 {
		return false
	}
	for _, r := range key {
		// Tarpai (parametrų skirtukas) — neleidžiami.
		if r == ' ' || r == '\t' || r == '\r' || r == '\n' || r == '\v' || r == '\f' {
			return false
		}
		// Valdymo simboliai (C0, DEL, C1) — neleidžiami.
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return false
		}
	}
	return true
}

// isValidModeTarget tikrina MODE komandos TIKSLĄ (pirmas parametras).
//
// Tikslas gali būti: kanalas („#kanalas“), nick'as, arba serverio kaukė
// („*server*“). Bet — jis įrašomas į komandą kaip atskiras parametras,
// todėl negali turėti tarpų (papildomas parametras) ar CRLF.
func isValidModeTarget(t string) bool {
	if t == "" || len(t) > 100 {
		return false
	}
	// Serverio kaukė „*server*“.
	if strings.HasPrefix(t, "*") {
		return isValidMaskName(t[1:])
	}
	// Kanalas arba nick'as.
	//
	// Nick'o simboliai — TOKI pat kaip isValidNick leidžia („_-[]\\`^{}|“)
	// — ankstesnė versija jų atmetė, todėl /mode nick[1] +o buvo
	// neįmanomas, nors pats nick'as galimas.
	for _, r := range t {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == '_' || r == '#' || r == '&' || r == '!' || r == '+':
		case strings.ContainsRune("_-[]\\`^{}|", r):
		case unicode.IsLetter(r):
		default:
			return false
		}
	}
	return true
}

// isValidMaskName tikrina serverio kaukės pavadinimą (be „*“ prefikso).
//
// Leistina ir „*“ (kaukės žvaigždutė): tikra serverio kaukė yra
// „*server*“ — be vidinės „*“ ji nebūtų kaukė. Anksčiau „*“ buvo
// atmetama, todėl /mode *server* ir /w *server* (toks pats tikslas, kurį
// pati funkcija dokumentuoja) buvo atmesti.
func isValidMaskName(s string) bool {
	if s == "" || len(s) > 100 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == '_' || r == '*':
		case unicode.IsLetter(r):
		default:
			return false
		}
	}
	return true
}

// maxModeParams – didžiausias MODE komandos parametrų skaičius.
//
// Serverio MODES (005) limitas riba, bet ne visuose serveriuose nurodyta.
// Be ribos „MODE *server* +o evil +k secretkey …“ galėtų siųsti šimtus
// parametrų — bereikalingas srautas į serverį.
const maxModeParams = 4

// isValidModeSpec tikrina MODE komandos parametrus (po tikslo).
//
// Parametrai įrašomi į komandą kaip atskiri laukai — tarpas yra skirtukas.
// Tarpai jau atskirti (strings.Fields), todėl čia tikriname:
//   - parametrų skaičių (riba);
//   - kad parametrai neturi CRLF (komandų injekcija).
func isValidModeSpec(fields []string) bool {
	if len(fields) > maxModeParams {
		return false
	}
	for _, f := range fields {
		if f == "" {
			return false
		}
		if strings.ContainsAny(f, "\r\n") {
			return false
		}
	}
	return true
}

// isValidWhoTarget tikrina WHO komandos tikslą.
//
// Tikslas: kanalas, nick'as, „*“ (visi) arba „*server*“ kaukė. Negali
// turėti tarpų (papildomas parametras) ar CRLF.
func isValidWhoTarget(t string) bool {
	if t == "" || len(t) > 100 {
		return false
	}
	if t == "*" {
		return true
	}
	if strings.HasPrefix(t, "*") {
		return isValidMaskName(t[1:])
	}
	return isValidModeTarget(t)
}

// isValidListFilter tikrina LIST komandos filtrą.
//
// Filtras įrašomas į komandą kaip parametras. Gali būti kanalo pavadinimas
// arba „*“ — negali turėti tarpų (papildomas parametras) ar CRLF.
func isValidListFilter(f string) bool {
	// Tuščias filtras = filtras NĖRA (teisinga forma).
	if f == "" {
		return true
	}
	if len(f) > 64 {
		return false
	}
	for _, r := range f {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == '_' || r == '#' || r == '&' || r == '!' || r == '+' || r == '*':
		case unicode.IsLetter(r):
		default:
			return false
		}
	}
	return true
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
