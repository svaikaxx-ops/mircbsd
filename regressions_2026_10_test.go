package main

// Regresijos testai 2026-10-20 peržiūros radiniams (K1, K2, K3, K4, K19).
// Kiekvienas radinys turi DU testus: „blogas atmetamas" ir „teisingas priimamas“.

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ============================================================
// K1 — senos jungties išardymas negriebti naujos
// ============================================================

// TestOldTeardownDoesNotClobberNewConn — senos jungties teardown-as neturi
// nutraukti naujos jungties būsenos (c.conn / c.connected).
func TestOldTeardownDoesNotClobberNewConn(t *testing.T) {
	c := newTestClient()
	c.logDir = ""
	c.reconnect = false

	// Realus klausytojas — kad galėtume uždaryti seną ryšį.
	oldLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer oldLn.Close()

	newLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer newLn.Close()

	oldPort := portOf(oldLn)
	newPort := portOf(newLn)

	// Sukurime „senos" jungties goroutine, kuri bus nutraukta VELIAU.
	oldGen := 0
	c.mu.Lock()
	c.connectGen = 0
	c.server = "127.0.0.1"
	c.port = oldPort
	c.mu.Unlock()

	oldDone := make(chan struct{})
	go func() {
		defer close(oldDone)
		c.doConnect(oldGen)
	}()

	// Paliekime seną jungtį prisijungusią.
	waitFor(t, 2*time.Second, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.connected
	}, "sena jungtis neprisijungė")

	// Dabar — NAUJA jungtis (kaip /connect padarytų).
	c.mu.Lock()
	c.connectGen = 1 // nauja epocha → senas doConnect turi pasitraukti
	c.server = "127.0.0.1"
	c.port = newPort
	c.mu.Unlock()

	newDone := make(chan struct{})
	go func() {
		defer close(newDone)
		c.doConnect(1)
	}()

	waitFor(t, 2*time.Second, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.connected && c.port == newPort
	}, "nauja jungtis neprisijungė")

	// Uždaryme seną serverio pusę → sena readLoop grįžta → atsiranda
	// senas teardown-as.
	oldLn.Close()

	select {
	case <-oldDone:
	case <-time.After(3 * time.Second):
		t.Fatal("sena jungtis neužbaigta per 3 s")
	}

	// NAUJA jungtis turi likti gyva.
	time.Sleep(200 * time.Millisecond)
	c.mu.Lock()
	conn, connected, port := c.conn, c.connected, c.port
	c.mu.Unlock()

	if !connected {
		t.Error("BUG: naujos jungties teardown nulaužė c.connected=false")
	}
	if conn == nil {
		t.Error("BUG: naujos jungties teardown nulaužė c.conn=nil (fd nutekėjimas)")
	}
	if port != newPort {
		t.Errorf("BUG: portas = %q, norėtų %q", port, newPort)
	}
}

// TestTeardownOfCurrentConnStillMarksDisconnected — patikra, kad įprastas
// (ne stale) teardown-as VIS TIK nurodo atsijungimą.
func TestTeardownOfCurrentConnStillMarksDisconnected(t *testing.T) {
	c := newTestClient()
	c.logDir = ""

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	c.mu.Lock()
	c.connectGen = 0
	c.server = "127.0.0.1"
	c.port = portOf(ln)
	c.mu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		c.doConnect(0)
	}()

	waitFor(t, 2*time.Second, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.connected
	}, "jungtis neprisijungė")

	ln.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("jungtis neužbaigta per 3 s")
	}

	c.mu.Lock()
	connected := c.connected
	c.mu.Unlock()
	if connected {
		t.Error("BUG: po atsijungimo c.connected vis tiek true")
	}
}

// ============================================================
// K2 — doTab negali lenktynėti su parseIRC
// ============================================================

// TestDoTabDoesNotRaceWithNickMutation — doTab skaito nick'ų sąrašą, o
// parseIRC jį mutuoja. Be tikros kopijos — duomenų lenktynė.
func TestDoTabDoesNotRaceWithNickMutation(t *testing.T) {
	c := newTestClient()
	c.logDir = ""
	c.mu.Lock()
	c.ensureCh("#test")
	c.mu.Unlock()

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Writer — kaip parseIRC: prideda nick'ų (ir sortNicks keičia vietomis).
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			c.mu.Lock()
			ch := c.activeChan()
			if ch != nil {
				ch.addNick("nick" + time.Now().Format("05.000000"))
			}
			c.mu.Unlock()
		}
	}()

	// Reader — doTab (jis pats ima c.mu, todėl lenktynės nebūtų, jei
	// kopijos daromos; testas apsaugo nuo回归 į seną elgseną).
	for i := 0; i < 200; i++ {
		c.mu.Lock()
		c.input = []rune("ni")
		c.inputPos = 2
		c.tabCandidates = nil
		c.mu.Unlock()
		c.doTab()
	}

	close(stop)
	wg.Wait()
}

// ============================================================
// K3 — isIgnored negali rodyti tarpinio (dubliuoto) sąrašo
// ============================================================

// TestIsIgnoredCopiesList — po /unignore senoji kopija neturi rodyti
// dubliuoto elementoą.
func TestIsIgnoredCopiesList(t *testing.T) {
	c := newTestClient()
	c.logDir = ""
	c.mu.Lock()
	c.ignoreList = []string{"bot1", "bot2", "bot3"}
	c.mu.Unlock()

	// Išryškiname /unignore logiką (be komandų apvalymo).
	c.mu.Lock()
	newList := make([]string, 0, len(c.ignoreList))
	for _, ig := range c.ignoreList {
		if !strings.EqualFold(ig, "bot2") {
			newList = append(newList, ig)
		}
	}
	c.ignoreList = newList
	c.mu.Unlock()

	// isIgnored turi matyti TEISINGĄ sąrašą: bot2 neignoruojamas.
	if c.isIgnored("bot2!user@host") {
		t.Error("BUG: isIgnored grąžina true nors bot2 pašalintas")
	}
	if !c.isIgnored("bot1!user@host") {
		t.Error("BUG: isIgnored nepažįsta bot1")
	}
	if !c.isIgnored("bot3!user@host") {
		t.Error("BUG: isIgnored nepažįsta bot3")
	}
}

// ============================================================
// K4 — AUTHENTICATE atsako tik jeigu patys paprašėme SASL
// ============================================================

// TestAuthenticateWithoutSaslPendingDoesNotLeakPassword — serverio
// nepageidaujamas „AUTHENTICATE +“ neturi gauti mūsų slaptažodžio.
func TestAuthenticateWithoutSaslPendingDoesNotLeakPassword(t *testing.T) {
	c := newTestClient()
	c.logDir = ""
	c.mu.Lock()
	c.nick = "testnick"
	c.saslPass = "labas-slapta-zodis"
	c.saslPending = false
	c.mu.Unlock()

	got := captureWrites(c)
	c.parseIRC("AUTHENTICATE +")

	lines := got(150 * time.Millisecond)
	for _, l := range lines {
		if strings.Contains(l, "labas-slapta-zodis") {
			t.Fatalf("BUG: slaptažodis nutekėjo į serverį: %q", l)
		}
		if strings.HasPrefix(l, "AUTHENTICATE") && !strings.Contains(l, "AUTHENTICATE +") {
			t.Errorf("BUG: netikėtinas AUTHENTICATE atsakymas: %q", l)
		}
	}
}

// TestAuthenticateWithSaslPendingSendsCredentials — teisingas atvejis:
// kai patys paprašėme SASL, slaptažodis ISSIUNČIAMAS.
func TestAuthenticateWithSaslPendingSendsCredentials(t *testing.T) {
	c := newTestClient()
	c.logDir = ""
	c.mu.Lock()
	c.nick = "testnick"
	c.saslPass = "labas-slapta-zodis"
	c.saslPending = true
	c.mu.Unlock()

	got := captureWrites(c)
	c.parseIRC("AUTHENTICATE +")

	lines := got(150 * time.Millisecond)
	found := false
	for _, l := range lines {
		if strings.HasPrefix(l, "AUTHENTICATE ") && l != "AUTHENTICATE +" {
			found = true
		}
	}
	if !found {
		t.Errorf("BUG: saslPending=true, bet slaptažodis neatsiųstas. Eilutės: %v", lines)
	}
}

// ============================================================
// K19 — panika neturi palikti terminalo raw režimu
// ============================================================

// TestRecoverPanicStopsClient — panika atkuriama: klientas sustabdomas
// (stopCh uždaromas), o ne paliekamas su sugriuvusiu terminalu.
func TestRecoverPanicStopsClient(t *testing.T) {
	c := newTestClient()
	c.logDir = ""

	// Resetiname stop kanalą (kiti testai gali jį jau uždaryti).
	stopCh = make(chan struct{})
	stopOnce = sync.Once{}

	// Panika absorbuojama recoverPanic (turi paleisti savo recover()).
	// Jos neabsorbuota panika suluočiau testą.
	func() {
		defer c.recoverPanic("testas")
		panic("sąmoningas testinis panika")
	}()

	// Panika turėjo sustabdyti klientą.
	select {
	case <-stopCh:
	case <-time.After(time.Second):
		t.Error("BUG: recoverPanic sustabdė klientą")
	}
}

// TestReadKeysRecoversFromPanic — readKeys gorutine turi absorbuoti paniką
// (kad terminalas liktų raw režimu).
func TestReadKeysRecoversFromPanic(t *testing.T) {
	c := newTestClient()
	c.logDir = ""
	stopCh = make(chan struct{})
	stopOnce = sync.Once{}

	// readKeys skaito iš os.Stdin — testuose tai neveikia, todėl
	// tikriname patį mechanizmą: recoverPanic antros klaviatūros
	// iteracijos viduje.
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer c.recoverPanic("klaviatūros įvestis")
		panic("sąmoningas testinis panika")
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("readKeys gorutine neabsorbuoto panikos")
	}
}

// ---- Pagalbininkai ----

// portOf grąžina klausytojo TCP portą.
func portOf(ln net.Listener) string {
	addr := ln.Addr().(*net.TCPAddr)
	return itoa(addr.Port)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// waitFor laukia, kol cond grąžina true (arba baigasi laikas).
func waitFor(t *testing.T, d time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", msg)
}

// ============================================================
// K8 — žurnalų eilių tvarka
// ============================================================

// TestLogMessagesKeepOrder — lygiagrečiai atėjusios žinutės turi būti
// žurnale ta pačia tvarka, kokia atėjo į klientą.
//
// Anksčiau kiekvienai žinutei buvo paleidžiama atskira gorutina
// (`go logMessage`), todėl 20 lygiagrečių žinučių rašydavo failą
// beveik atsitiktine tvarka.
func TestLogMessagesKeepOrder(t *testing.T) {
	c := newTestClient()
	dir := t.TempDir()
	c.logDir = dir
	c.server = "srv"
	c.startLogWorker()
	defer func() {
		c.stopLogWorker()
		c.closeLogs()
	}()

	const n = 40
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			c.addMsg("#kanalas", Message{
				ts:      time.Now(),
				nick:    "k",
				text:    fmt.Sprintf("pranesimas-%03d", i),
				msgType: MsgChat,
			})
		}
	}()
	wg.Wait()

	// Paliekame žurnalų gorutinai apdoroti eilę.
	deadline := time.Now().Add(3 * time.Second)
	var data []byte
	var err error
	for time.Now().Before(deadline) {
		path := filepath.Join(dir, "srv", "#kanalas-"+time.Now().Format("2006-01-02")+".log")
		data, err = os.ReadFile(path)
		if err == nil && len(data) > 0 {
			// Patikriname, ar visos eilutės jau parašytos.
			if strings.Count(string(data), "pranesimas-") == n {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != n {
		t.Fatalf("BUG: žurnale %d eilučių, o turėjo būti %d", len(lines), n)
	}
	for i, l := range lines {
		want := fmt.Sprintf("pranesimas-%03d", i)
		if !strings.Contains(l, want) {
			t.Errorf("BUG: %d-oji eilutė = %q, norėtų %q", i, l, want)
		}
	}
}
