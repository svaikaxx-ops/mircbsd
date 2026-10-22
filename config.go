package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ---- Config ----

type Config struct {
	Server         string
	Port           string
	TLS            bool
	Nick           string
	RealName       string
	NickServ       string
	SASLPass       string // SASL PLAIN slaptažodis (vartotojas = Nick)
	ServerPass     string // serverio slaptažodis (PASS komanda) — bouncer, privatus serveris
	AutoJoin       []string
	Perform        []string // komandos vykdomos po prisijungimo
	HighlightWords []string // papildomi žodžiai highlight'inimui
	LogDir         string
	MaxMessages    int
	Reconnect      bool
	CTCPVersion    bool
	Notifications  bool   // desktop pranešimai (notify-send) highlight atveju
	Mouse          bool   // pelė: ratukas scroll'ui, paspaudimas kanalo juostoje
	TimestampColor string // gray (numatytas), blue, green, cyan, none
	Timestamps     bool   // laiko žymos prie žinučių (numatytai įjungta)
	TSFormat       string // laiko žymos formatas (Go laiko taisyklė, numatas „15:04“)
	HighlightColor string // highlight fono spalva: red (numatytas), blue, green, magenta, cyan, yellow, none
	NickColors     bool   // nick'ų spalvinimas (numatytai įjungta)
	TLSCert        string // kliento sertifikatas (PEM) — SASL EXTERNAL
	TLSKey         string // kliento raktas (PEM) — SASL EXTERNAL
	SASLExternal   bool   // SASL EXTERNAL (autentifikacija kliento sertifikatu)
	TLSPinning     bool   // TLS serverio atspaudo (fingerprint) pinning (TOFU)
	TLSPin         string // nustatytas atspaudas (sha256 hex) — pirmenybė prieš TOFU
	Theme          string // tema: dark (numatyta), light
}

func defaultConfig() Config {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	return Config{
		Port:          "6667",
		Nick:          "mircbsd",
		RealName:      "mircBSD IRC Client",
		LogDir:        filepath.Join(home, "irclogs"),
		MaxMessages:   5000,
		Reconnect:     true,
		CTCPVersion:   true,
		Notifications: true,
		Mouse:         true,
		Timestamps:    true,
		TSFormat:      "15:04",
		NickColors:    true,
	}
}

// configWarnings — įspėjimai apie neteisingus konfigūracijos parametrus.
// Rodomi vartotojui paleidimo metu, kad blogas nickas/portas nebūtų tyliai
// pakeistas.
var configWarnings []string

// loadConfigWarnings grąžina paskutinio loadConfig() metu rastus
// įspėjimus.
func loadConfigWarnings() []string {
	return configWarnings
}

func loadConfig(path string) Config {
	configWarnings = nil
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
		// Nuplaukime porines kabutes: nickserv="slaptazodis" be jų būtų su "
		if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
			v = v[1 : len(v)-1]
		}
		switch strings.ToLower(k) {
		case "server":
			cfg.Server = v
		case "port":
			// Portas turi būti skaičius 1–65535 — blogas tipas būtų
			// perduodamas į adresą kaip „localhost:blogas“.
			if isValidPort(v) {
				cfg.Port = v
			} else {
				cfg.Port = "6667"
				configWarnings = append(configWarnings, "Neteisingas portas: "+v+" (numatytas 6667)")
			}
		case "tls":
			cfg.TLS = v == "true" || v == "1" || v == "yes"
		case "nick":
			// Nick'o simbolių rinkinys ir ilgis tikrinami čia — anksčiau
			// blogas nickas iš config'o pateko tiesiog į NICK komandą.
			if isValidNick(v) {
				cfg.Nick = v
			} else {
				configWarnings = append(configWarnings, "Neteisingas nick: "+v+" (numatytas mircbsd)")
			}
		case "realname":
			cfg.RealName = v
		case "nickserv":
			cfg.NickServ = v
		case "password":
			// Serverio slaptažodis (PASS komanda) — bouncer arba privatus
			// serveris. Nesiunčiamas į žurnalus, tik į serverį.
			cfg.ServerPass = v
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
		case "notifications":
			cfg.Notifications = v == "true" || v == "1" || v == "yes"
		case "mouse":
			cfg.Mouse = v == "true" || v == "1" || v == "yes"
		case "tscolor":
			cfg.TimestampColor = v
		case "timestamps":
			cfg.Timestamps = v == "true" || v == "1" || v == "yes"
		case "ts_format":
			// Laiko žymos formatas — Go laiko taisyklė (pvz. „15:04:05“).
			// Tuščias — lieka numatytas.
			cfg.TSFormat = v
		case "highlight_color":
			cfg.HighlightColor = v
		case "nick_colors":
			cfg.NickColors = v == "true" || v == "1" || v == "yes"
		case "sasl":
			cfg.SASLPass = v
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
		case "tls_cert":
			cfg.TLSCert = expandHomePath(v)
		case "tls_key":
			cfg.TLSKey = expandHomePath(v)
		case "sasl_external":
			cfg.SASLExternal = v == "true" || v == "1" || v == "yes"
		case "tls_pinning":
			cfg.TLSPinning = v == "true" || v == "1" || v == "yes"
		case "tls_pin":
			// Neišvalome — tikriname doConnect'e (sha256 hex formatas).
			cfg.TLSPin = strings.ToLower(strings.TrimSpace(v))
		case "theme":
			cfg.Theme = strings.ToLower(v)
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

// expandHomePath — „~/“ keliai expandinami (tls_cert, tls_key).
func expandHomePath(v string) string {
	if strings.HasPrefix(v, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, v[2:])
		}
	}
	return v
}

// monitorPath — /notify sąrašo failas (kaip ignore): configDir/notify.
func monitorPath() string {
	dir := configDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "notify")
}

// saveMonitor — stebimų nick'ų sąrašas į diską (0600, atominis).
// Ribota iki 100 įrašų — serverio MONITOR limitai būna iki ~100.
func (c *Client) saveMonitor() {
	path := monitorPath()
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return
	}
	c.mu.Lock()
	const monitorMax = 100
	if len(c.monitorList) > monitorMax {
		c.monitorList = c.monitorList[:monitorMax]
	}
	list := make([]string, len(c.monitorList))
	copy(list, c.monitorList)
	c.mu.Unlock()

	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return
	}
	for _, n := range list {
		_, _ = f.WriteString(n + "\n")
	}
	_ = f.Close()
	_ = os.Rename(tmp, path)
}

// loadMonitor — /notify sąrašas iš diskо (sustabdyti nevalidūs).
func (c *Client) loadMonitor() {
	path := monitorPath()
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
		if n != "" && isValidNick(n) {
			list = append(list, n)
		}
	}
	c.mu.Lock()
	c.monitorList = list
	c.mu.Unlock()
}

// ---- Klavišų perrašymas (keys failas, v0.4.8) ----

// keyActions — /keys palaikomi veiksmų vardai: kiekvienas atvaizduojamas į
// KANONINĘ valdymo seką (baitai). Kliento handleKey perrašo gautą seką
// kanonine ir toliau elgiasi įprastai — visi veiksmai (slinktis, paieška,
// istorija, autocomplete) eina per egzistuojantį apdorojimą.
var keyActions = map[string][]byte{
	"chan-next":  {0x0e},
	"chan-prev":  {0x10},
	"nick-up":    {0x1b, '[', '1', ';', '5', 'A'},
	"nick-down":  {0x1b, '[', '1', ';', '5', 'B'},
	"word-left":  {0x1b, '[', '1', ';', '5', 'D'},
	"word-right": {0x1b, '[', '1', ';', '5', 'C'},
	"msg-up":     {0x1b, '[', '5', '~'},
	"msg-down":   {0x1b, '[', '6', '~'},
	"msg-bottom": {0x1b, '[', '6', ';', '2', '~'},
	"search":     {0x12},
	"hist-up":    {0x1b, '[', 'A'},
	"hist-down":  {0x1b, '[', 'B'},
	"complete":   {'\t'},
	"word-del":   {23},
}

// keyActionNames — surikinta vardų eilutė (sąrašui ir dokumentacijai).
func keyActionNames() []string {
	names := make([]string, 0, len(keyActions))
	for name := range keyActions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// keysPath — klavišų failas: configDir/keys ("veiksmas = sekа" eilutės).
// keysPath — klavišų failas: configDir/keys ("veiksmas = seka" eilutės).
func keysPath() string {
	dir := configDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "keys")
}

// unescapeKeySeq — keys failo sekos rašymo sintaksė: \e (ESC), \xHH,
// \n, \r, \t, \\; kiti simboliai — rašomi LITERAI („a“ — mygtukas A).
// Nevalidus \x — klaida (kad neįsitaikytų numatytos sekos klaidos).
func unescapeKeySeq(s string) ([]byte, error) {
	out := []byte{}
	i := 0
	for i < len(s) {
		ch := s[i]
		if ch != '\\' {
			out = append(out, ch)
			i++
			continue
		}
		if i+1 >= len(s) {
			return nil, errors.New("pabaigos „\\“ be simbolio")
		}
		switch s[i+1] {
		case 'e', 'E':
			out = append(out, 0x1b)
			i += 2
		case 'n':
			out = append(out, '\n')
			i += 2
		case 'r':
			out = append(out, '\r')
			i += 2
		case 't':
			out = append(out, '\t')
			i += 2
		case '\\':
			out = append(out, '\\')
			i += 2
		case 'x':
			if i+4 <= len(s) {
				v := strings.ToLower(s[i+2 : i+4])
				b, err := strconv.ParseUint(v, 16, 8)
				if err != nil {
					return nil, errors.New("nevalidus \\xXX: \\x" + v)
				}
				out = append(out, byte(b))
				i += 4
			} else {
				return nil, errors.New("\\x be dvejetainių reikšmių")
			}
		default:
			return nil, errors.New("nežinoma seka: \\" + string(s[i+1]))
		}
	}
	return out, nil
}

// parseKeysFile — keys failo turinys: eilutės „veiksmas = seka“.
// # komentarai ir tuščios eilutės praleidžiamos. Nevalidus veiksmo vardas
// arba seka — praleidžiama (grąžinant įspėjimą).
func parseKeysFile(path string) (map[string]string, []string) {
	binds := map[string]string{}
	var warns []string
	f, err := os.Open(path)
	if err != nil {
		return binds, nil
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			warns = append(warns, fmt.Sprintf("keys:%d: nėra „=“", lineNo))
			continue
		}
		action := strings.ToLower(strings.TrimSpace(parts[0]))
		seq, err := unescapeKeySeq(strings.TrimSpace(parts[1]))
		if err != nil {
			warns = append(warns, fmt.Sprintf("keys:%d: %s", lineNo, err.Error()))
			continue
		}
		if len(seq) == 0 || len(seq) > 16 {
			warns = append(warns, fmt.Sprintf("keys:%d: seka per ilga (maks. 16 baitų)", lineNo))
			continue
		}
		if _, ok := keyActions[action]; !ok {
			known := strings.Join(keyActionNames(), ", ")
			warns = append(warns, fmt.Sprintf("keys:%d: nežinomas veiksmas „%s“ (žinomi: %s)", lineNo, action, known))
			continue
		}
		if _, clash := binds[string(seq)]; clash {
			warns = append(warns, fmt.Sprintf("keys:%d: seka jau perrašyta kitu veiksmu", lineNo))
			continue
		}
		binds[string(seq)] = action
	}
	return binds, warns
}

// settableKeys — raktai, kuriuos leidžiama keisti per /set (v0.4.8).
// SENSĖYNGI raktai (sasl, password, nickserv, tls, server, port, nick,
// perform, autojoin, logdir) tyčia NĖRA sąraše: jų keitimas per /set arba
// saugus tik prie naujos jungties (tls/server/port), arba rašytų
// slaptažodį į failą iš terminalo istorijos.
var settableKeys = []string{
	"mouse", "notifications", "ctcpversion", "reconnect",
	"timestamps", "ts_format", "tscolor", "highlight_color",
	"nick_colors", "maxmessages", "theme",
}

// isSettable — ar raktas pasiimamas per /set (mazgai tikslūs —
// „TsColor“ ir „tscolor“ sutampa).
func isSettable(key string) bool {
	for _, k := range settableKeys {
		if k == strings.ToLower(key) {
			return true
		}
	}
	return false
}

// saveConfigSetting perrašo VENO rakto reikšmę konfigūracijos faile:
// esama eilutė su tokiu raktu (be didžiųjų skirtumo, komentariuose ne)
// pakeičiama, likęs failas išliekas NEEKITO. Rakto nesant — eilutė
// pridedama failo gale. Rašymas atominis (tmp + rename), teisės 0600.
func saveConfigSetting(key, value string) error {
	path := configPath()
	if path == "" {
		return errors.New("nepavyko nustatyti konfigūracijos kelio")
	}
	keyLow := strings.ToLower(key)
	var lines []string
	replaced := false
	if b, err := os.ReadFile(path); err == nil {
		sc := bufio.NewScanner(strings.NewReader(string(b)))
		for sc.Scan() {
			line := sc.Text()
			writeLine := line
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, "#") {
				if parts := strings.SplitN(trimmed, "=", 2); len(parts) == 2 {
					if strings.EqualFold(strings.TrimSpace(parts[0]), keyLow) {
						// Saugome originalią rakto rašybą („Nick_Colors“ kaip
						// „nick_colors“), tik reikšmę pakeičiame.
						writeLine = line[:strings.Index(line, "=")+1] + value
						replaced = true
					}
				}
			}
			lines = append(lines, writeLine)
		}
	}
	if !replaced {
		lines = append(lines, key+"="+value)
	}
	out := strings.Join(lines, "\n")
	if out != "" {
		out += "\n"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(out); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// parseBoolCfg — /set logikos reikšmė (true/1/yes = teigiama).
// atitinka loadConfig tikrinimą.
func parseBoolCfg(v string) bool {
	return v == "true" || v == "1" || v == "yes"
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
