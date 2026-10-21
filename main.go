package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"runtime/debug"
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
	// „--“ skyri PRIEŠ pranešimo tekstą — be jo tekstas, prasidedantis „--“,
	// būtų interpretuojamas kaip notify-send PARINKTIS, o ne kaip
	// pranešimo turinys. Pavadinimas ir tekstas čia — serverio kontroliuojami
	// (kanalo pavadinimas arba PRIVMSG), todėl „--urgency=critical“ buvo
	// galima injekcija.
	cmd := exec.Command(path, "-a", "mircBSD", "-t", "5000", "--", title, body)
	_ = cmd.Start()
}

// ---- Sustabdymas ----

var (
	stopCh   = make(chan struct{})
	stopOnce sync.Once
)

// termRestoreOnce — terminalo atkūrimas turi įvykti VISIŠKAI KARTĄ, net jei
// panika nutikptų keliuose goroutine (kitaip terminalas liktų raw režimu
// ir š Shell neveiktų).
var termRestoreOnce sync.Once

// globalStateMu saugo globalOldState (naudojama atkūrimo funkcijoms).
var (
	globalStateMu  sync.Mutex
	globalOldState *term.State
)

// restoreTerminal grąžina terminalą iš raw režimo: atkuria seną būsenį,
// parodo kursorių ir nuvalo ekraną. Saugu kvieti kelis kartus.
//
// Jei globalOldState == nil (testai) — nieko nedarome, kad neužterštume
// stdout'o ANSI kodus.
func restoreTerminal() {
	termRestoreOnce.Do(func() {
		globalStateMu.Lock()
		st := globalOldState
		globalStateMu.Unlock()
		if st == nil {
			return
		}
		term.Restore(int(os.Stdin.Fd()), st)
		fmt.Print(ansiShowCur + "\033[2J\033[H")
	})
}

// recoverPanic atkuria terminalą ir sustabdo klientą, jei goroutine viduje
// įvyko panika. Be to terminalas liktų raw režimu, o klaviatūra — neveikti.
//
// recoverPanic turi būti IŠKVIEČIAMAS per `defer` pačios funkcijos pradžioje.
func (c *Client) recoverPanic(context string) {
	if r := recover(); r != nil {
		fmt.Fprintf(os.Stderr, "\r\nmircBSD: panika (%s): %v\r\n", context, r)
		if dbg := os.Getenv("MIRCBSD_DEBUG"); dbg != "" {
			debug.PrintStack()
		}
		restoreTerminal()
		c.stop()
	}
}

func (c *Client) stop() {
	// stop() gali būti kviečiamas ir iš /quit, ir iš signalo — uždarome tik kartą
	stopOnce.Do(func() {
		if c.mouse {
			// Išjungti pelės režimą prieš gryžtant terminalui
			fmt.Print("\033[?1002l\033[?1000l\033[?1006l")
		}
		c.saveHistory()
		// Sustabdom žurnalų gorutiną (ištušindamas eilę), tik tada
		// uždarome failus — kad neprarastų jau įrašytų žinučių.
		c.stopLogWorker()
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
	// Globalus būsenas — kad panikos atkūrimas galėtų atkurti terminalą
	// iš bet kurios goroutine.
	globalStateMu.Lock()
	globalOldState = oldState
	globalStateMu.Unlock()

	cfg := loadConfig(configPath())
	setTSColor(cfg.TimestampColor)
	setHighlightColor(cfg.HighlightColor)
	setNickColors(cfg.NickColors)
	configWarn := loadConfigWarnings()

	args := os.Args[1:]
	if len(args) >= 2 {
		// Komandinės eilutės argumentai VALIDUOJAMI — anksčiau serverio
		// vardas, portas ir nick galėjo būti bet kokie (komandų injekcija
		// į IRC komandas, neveiksmingas adresas).
		if !isValidServer(args[0]) {
			configWarn = append(configWarn, "Neteisingas serverio vardas: "+args[0])
			args[0] = ""
		}
		if !isValidPort(args[1]) {
			configWarn = append(configWarn, "Neteisingas portas: "+args[1])
			args[1] = ""
		}
		if len(args) >= 2 {
			cfg.Server = args[0]
			cfg.Port = args[1]
		}
		if len(args) >= 3 {
			if !isValidNick(args[2]) {
				configWarn = append(configWarn, "Neteisingas nick: "+args[2])
			} else {
				cfg.Nick = args[2]
			}
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
	// Žurnalų gorutina — vieta eilės tvarkai (viena rašytoja).
	c.startLogWorker()

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
	for _, w := range configWarn {
		c.errMsg("ĮSPĖJIMAS: " + w)
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

	// deferred recover — panika pagrindiniame cikle (pvz. draw()) taip pat
	// turi atkurti terminalą, o ne palikti jį raw režimu.
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "\r\nmircBSD: panika (pagrindinis ciklas): %v\r\n", r)
			if dbg := os.Getenv("MIRCBSD_DEBUG"); dbg != "" {
				debug.PrintStack()
			}
			restoreTerminal()
		}
	}()

	for {
		select {
		case <-stopCh:
			restoreTerminal()
			fmt.Println("Viso gero!")
			return
		case <-c.redraw:
			c.draw()
		}
	}
}

const version = "0.4.6"
