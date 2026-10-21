package main

import (
	"bufio"
	"os"
	"path/filepath"
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
