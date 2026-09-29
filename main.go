package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/term"
)

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

// notifyDesktop parodo darbalaukio pranešimą per notify-send
// (freedesktop.org – veikia Debian XFCE/GNOME/KDE ir FreeBSD su
// notify-send). Nebūtinai turi failą ar daemon'ą – jeigų nėra, nieko.
func notifyDesktop(title, body string) {
	path, err := exec.LookPath("notify-send")
	if err != nil {
		return
	}
	// Apriboti tekstą, kad pranešimas nebūtų milžiniškas
	title = sanitizeText(title)
	if t := []rune(title); len(t) > 60 {
		title = string(t[:60])
	}
	body = sanitizeText(body)
	if t := []rune(body); len(t) > 120 {
		body = string(t[:120]) + "…"
	}
	cmd := exec.Command(path, "-a", "mircBSD", "-t", "5000", title, body)
	_ = cmd.Start()
}

// ---- Sustabdymas ----

var (
	stopCh   = make(chan struct{})
	stopOnce sync.Once
)

func (c *Client) stop() {
	// stop() gali būti kviečiamas ir iš /quit, ir iš signalo — uždarome tik kartą
	stopOnce.Do(func() {
		if c.mouse {
			// Išjungti pelės režimą prieš gryžtant terminalui
			fmt.Print("\033[?1002l\033[?1000l\033[?1006l")
		}
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
	if c.mouse {
		// Pelės režimas (SGR 1006): ratukas scroll'ui, paspaudimai kanalams
		fmt.Print("\033[?1000h\033[?1002h\033[?1006h")
	}

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

const version = "0.3.1"
