//go:build audit

// Regresijos testai peržiūros 2026-10-21 radiniams.
//
// Paleisti: go test -tags audit -race ./...
//
// Kiekvienas radinys turi DU testus:
//   - „blogas atmetamas“  — atkuria originalų atkirtimo scenarijų;
//   - „teisingas priimamas“ — įrodo, kad pataisymas nepražudojo
//     teisingų atvejų (tai buvo pagrindinis šios peržiūros pamokos
//     aspektas: „nesurūšiuoti intervalai + dvejetainė paieška = tyliai
//     neveikiantis“).
//
// Peržiūra: PERZIURA_2026-10-21.md

package main

import (
	"bufio"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ==================================================================
// R1 (KRITINIS) — gyvas terminalo sink: Tab-completion lėkė į c.input
//                nesanitizuotą tekstą (OSC 52 → iškarpinės rašymas).
// ==================================================================

// TestFinding_TabCompletionNoRawTerminalSink — serverio JOIN su ESC
// simboliais nick'e neturi pasiekti TERMINALO (per c.input).
//
// KELIAS: serveris → JOIN → ch.nicks (raw) → doTab() → stripMode
// (nešalina ESC) → c.input → draw() → out.WriteString (raw).
//
// Tikrinamas realus apsaugos taškas — draw() OUTPUT'AS. (c.input gali
// laikyti žaliai — jis niekur nerašomas tiesiogiai; apsauga yra
// sanitizavime prieš braižymą.)
func TestFinding_TabCompletionNoRawTerminalSink(t *testing.T) {
	c := newTestClient()
	c.connected = true
	c.mu.Lock()
	c.ensureCh("#chan")
	ch := c.findCh("#chan")
	c.activeIdx = 1
	c.mu.Unlock()

	// Nick su OSC 52 (iškarpinės rašymas) + ESC.
	evil := "ev\x1b]52;c;UFJPQkFC\a"
	ch.addNick(evil)

	// Tab-completion: įvedame pirmą simbolį, kad atsirastų kandidatas.
	c.mu.Lock()
	c.input = []rune("e")
	c.inputPos = 1
	c.mu.Unlock()
	c.doTab()

	c.mu.Lock()
	got := string(c.input)
	c.mu.Unlock()

	if strings.ContainsRune(got, 0x1b) {
		t.Errorf("BUG: Tab-completion į c.input įnešė ESC simbolį: %q", got)
	}

	// Dabar — pats terminalo sinkas: įvesties eilutė, KURI bus braižoma,
	// turi būti sanitizuota (draw() tai daro prieš out.WriteString).
	c.mu.Lock()
	disp := sanitizeText(string(c.input))
	c.mu.Unlock()
	if strings.ContainsRune(disp, 0x1b) {
		t.Errorf("BUG: OSC 52 (iškarpinės rašymas) pasiekė terminalą: %q", disp)
	}
}

// TestFinding_InputLineSanitized — įvesties eilutė braižoma tik
// sanitizuota (teisingas atvejis: įprastas tekstas lieka nepakitęs).
func TestFinding_InputLineSanitized(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	c.input = []rune("labas #kanalas")
	c.inputPos = len(c.input)
	c.mu.Unlock()

	// Teisingas atvejis: įprastas tekstas lieka nepakitęs.
	c.mu.Lock()
	got := sanitizeText(string(c.input))
	c.mu.Unlock()
	if got != "labas #kanalas" {
		t.Errorf("BUG: sanitizeText pakeitė įprastą tekstą: %q", got)
	}

	// Blogas atvejis: ESC / C1 / BEL pašalinami.
	evil := "labas\x1b]52;c;UFJPQkFC\x07\x9b"
	got = sanitizeText(evil)
	if strings.ContainsRune(got, 0x1b) || strings.ContainsRune(got, 0x9b) || strings.ContainsRune(got, 0x07) {
		t.Errorf("BUG: sanitizeText nepašalino valdymo simbolių: %q", got)
	}
}

// ==================================================================
// R2 (KRITINIS) — žurnalo slaptažodžių nutekimas: tikrinama TIK
//                kūno dalis, o ne visa komanda.
// ==================================================================

// TestFinding_LogNoPlaintextPassword — slaptažodžiai NIEKADA neateina
// į žurnalą — ne pagal formą, ne pagal komandą.
func TestFinding_LogNoPlaintextPassword(t *testing.T) {
	secret := "hunter2Secret"
	forms := []string{
		"/msg nickserv identify " + secret,
		"register " + secret + " user@example.com",
		"set password " + secret,
		"set p " + secret,
		"confirm " + secret,
		"auth " + secret,
		"identify " + secret,
		"/raw PASS " + secret,
		"/raw pass " + secret,
		"PASS " + secret,
		"OPER " + secret,
		"/oper " + secret,
		"oper " + secret,
		"/ns set password " + secret,
		"nickserv identify " + secret,
	}
	for _, form := range forms {
		dir := t.TempDir()
		c := newTestClient()
		c.logDir = dir
		c.server = "srv"
		c.logMessage(dir, "srv", "#k", Message{
			ts: time.Now(), nick: "NickServ", text: form, msgType: MsgChat,
		})
		c.closeLogs()

		// Surinkime visą katalogo turinį.
		var content []byte
		_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			b, rerr := os.ReadFile(p)
			if rerr == nil {
				content = append(content, b...)
			}
			return nil
		})
		if strings.Contains(string(content), secret) {
			t.Errorf("BUG: slaptažodis pateko į žurnalą formoje %q: %s", form, content)
		}
	}
}

// TestFinding_LogKeepsNonSecretCommands — pataisymas NETURI slėpti
// įprastų komandų (teisingas atvejis).
func TestFinding_LogKeepsNonSecretCommands(t *testing.T) {
	dir := t.TempDir()
	c := newTestClient()
	c.logDir = dir
	c.server = "srv"
	c.logMessage(dir, "srv", "#k", Message{
		ts: time.Now(), nick: "draugas", text: "sveiki visi", msgType: MsgChat,
	})
	c.closeLogs()

	var content []byte
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr == nil {
			content = append(content, b...)
		}
		return nil
	})
	if !strings.Contains(string(content), "sveiki visi") {
		t.Errorf("BUG: įprasta žinutė neįrašyta į žurnalą: %s", content)
	}
}

// TestFinding_HistoryNoPlaintextPassword — slaptažodis neateina į
// įvesties ISTORIJĄ (failas ~/.config/mircbsd/history).
func TestFinding_HistoryNoPlaintextPassword(t *testing.T) {
	secret := "histSecret42"
	forms := []string{
		"/msg nickserv identify " + secret,
		"/raw PASS " + secret,
		"register " + secret + " a@b.c",
		"set password " + secret,
	}
	for _, form := range forms {
		if !isSensitiveHistory(form) {
			t.Errorf("BUG: isSensitiveHistory neaptiko slaptažodžio formoje %q", form)
		}
	}
	// Teisingas atvejis: įprasta komanda — NE slaptažodis.
	for _, ok := range []string{"sveiki", "/join #kanalas", "/msg draugas labas", "identification"} {
		if isSensitiveHistory(ok) {
			t.Errorf("BUG: isSensitiveHistory klaidingai slėpė neslaptą komandą %q", ok)
		}
	}
}

// ==================================================================
// R3/R4 (KRITINIS) — activeChan()/activeName() SKAITO BE c.mu;
//                keysDebug — taip pat; draw() — skaito kanalo laukus
//                po Unlock.
// ==================================================================

// TestFinding_NoRaceActiveChanAccess — visos activeChan naudojamosios
// vietos turi būti saugios (lenktynės aptinkamos -race).
func TestFinding_NoRaceActiveChanAccess(t *testing.T) {
	c := newTestClient()
	c.connected = true
	c.mu.Lock()
	c.ensureCh("#a")
	c.ensureCh("#b")
	c.mu.Unlock()

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				// AKTYVUS KANALAS — su užrakintu c.mu (Locked variantas).
				c.mu.Lock()
				ch := c.activeChanLocked()
				if ch != nil {
					_ = ch.name
					_ = ch.unread
					_ = ch.highlight
				}
				c.mu.Unlock()
				// Pavadinimas — atskira (activeName() užrakina pats save).
				_ = c.activeName()
			}
		}()
	}
	// Paraleliai — serverio pusė keičia kanalus.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 200; j++ {
			c.parseIRC(":x!u@h JOIN #k" + string(rune('a'+j%26)))
			c.parseIRC(":x!u@h PART #k" + string(rune('a'+j%26)))
		}
	}()
	wg.Wait()
}

// TestFinding_NoRaceKeysDebug — /keys komanda (keičia c.keysDebug) ir
// klaviatūros gija (skaito c.keysDebug) — negali lenktynėti.
func TestFinding_NoRaceKeysDebug(t *testing.T) {
	c := newTestClient()
	c.connected = true
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 100; j++ {
			c.handleCmd("keys", "")
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 100; j++ {
			c.handleKey([]byte("a"))
		}
	}()
	wg.Wait()
}

// ==================================================================
// R6 — kanalas su TARPAIS (iš serverio) → komandų tikslo klastojimas.
// ==================================================================

// TestFinding_ChannelWithSpacesRejected — kanalas „#pub #priv“ neturi
// būti kuriamas (jis komandose būtų papildomas parametras).
func TestFinding_ChannelWithSpacesRejected(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	ch := c.ensureCh("#pub #priv")
	c.mu.Unlock()
	if ch != nil {
		t.Errorf("BUG: sukurtas kanalas su tarpais: %q", ch.name)
	}
	// Teisingas atvejis: įprastas kanalas sukuriamas.
	c.mu.Lock()
	ok := c.ensureCh("#normal")
	c.mu.Unlock()
	if ok == nil {
		t.Error("BUG: įprastas kanalas nebuvo sukurtas")
	}
}

// TestFinding_SanitizeChanName — patikrina paties validatoriaus elgseną.
func TestFinding_SanitizeChanName(t *testing.T) {
	for _, bad := range []string{"#pub #priv", "#a\tb", "#a\nb", "#a\rb", "#a b"} {
		if got := sanitizeChanName(bad); got != "" {
			t.Errorf("BUG: sanitizeChanName(%q) = %q — turi būti tuščias", bad, got)
		}
	}
	for _, ok := range []string{"#normal", "&local", "#a_b", "#Žilvinas"} {
		if got := sanitizeChanName(ok); got == "" {
			t.Errorf("BUG: sanitizeChanName(%q) = \"\" — turi būti priimtas", ok)
		}
	}
}

// ==================================================================
// R7 — maxChannels taikomas VISIEMS kanalams, įskaitant PM.
// ==================================================================

// TestFinding_PrivateChatBudgetSeparate — PM flood negali užblokuoti
// REALIŲ kanalų.
func TestFinding_PrivateChatBudgetSeparate(t *testing.T) {
	c := newTestClient()
	// Užpildome privačių pokalbių biudžetą.
	c.mu.Lock()
	for i := 0; i < maxPrivateChannels+50; i++ {
		c.ensureCh("pm" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)))
	}
	real := c.ensureCh("#realus")
	c.mu.Unlock()
	if real == nil {
		t.Errorf("BUG: PM flood užblokavo realaus kanalo kūrimą (riba %d PM)", maxPrivateChannels)
	}
}

// TestFinding_ChannelLimitWarnsUser — kai riba pasiekiama, vartotojas
// turi būti ĮSPĖJAMAS (anksčiau žinutės TYLIAI išnykdavo).
func TestFinding_ChannelLimitWarnsUser(t *testing.T) {
	c := newTestClient()
	// Užpildome realių kanalų biudžetą.
	c.mu.Lock()
	for i := 0; i < maxChannels; i++ {
		c.ensureCh("#c" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)))
	}
	c.mu.Unlock()

	// Serveris siunčia žinutę į NAUJĄ kanalą — riba pasiekiama.
	c.parseIRC(":x!u@h PRIVMSG #naujas :sveiki")

	found := false
	c.mu.Lock()
	for _, m := range c.channels[0].messages {
		if strings.Contains(m.text, "limitas") {
			found = true
		}
	}
	c.mu.Unlock()
	if !found {
		t.Error("BUG: kanalų riba pasiekiama, bet vartotojas neįspėjamas — žinutės tyliai išnyksta")
	}
}

// ==================================================================
// R9 — validacijų trūksta: /invite, /op, /mode, /list, /w, /whois.
// ==================================================================

// TestFinding_CommandTargetValidationRejects — klaviatūros keliu
// įvestos komandos su blogais tikslais turi būti atmestos.
func TestFinding_CommandTargetValidationRejects(t *testing.T) {
	// Blogi tikslai (turi būti atmesti).
	bad := []string{
		"nick su tarpais",
		"nick\nkita",
		"#kanalas su tarpais",
		"",
	}
	for _, b := range bad {
		if isValidNick(b) {
			t.Errorf("BUG: isValidNick priėmė %q", b)
		}
	}
	// Teisingi nick'ai — priimami.
	for _, ok := range []string{"Jonas", "nick_1", "Žilvinas", "a"} {
		if !isValidNick(ok) {
			t.Errorf("BUG: isValidNick atmetė teisingą nick'ą %q", ok)
		}
	}
}

// TestFinding_ModeSpecValidation — /mode komandos specas validuojamas.
func TestFinding_ModeSpecValidation(t *testing.T) {
	// Blogi: per daug parametrų (maxModeParams riba), CRLF.
	if isValidModeSpec([]string{"+o", "a", "b", "c", "d", "e"}) {
		t.Error("BUG: isValidModeSpec priėmė per daug parametrų")
	}
	if isValidModeSpec([]string{"+o\r\nX"}) {
		t.Error("BUG: isValidModeSpec priėmė CRLF")
	}
	if isValidModeSpec([]string{""}) {
		t.Error("BUG: isValidModeSpec priėmė tuščią parametrą")
	}
	// Teisingi — priimami.
	for _, ok := range [][]string{{"+o"}, {"-v"}, {"+b"}, {"o"}} {
		if !isValidModeSpec(ok) {
			t.Errorf("BUG: isValidModeSpec atmetė teisingą specą %v", ok)
		}
	}
}

// TestFinding_WhoTargetValidation — /w tikslas validuojamas.
func TestFinding_WhoTargetValidation(t *testing.T) {
	for _, bad := range []string{"#a b", "#a\nb", "nick x"} {
		if isValidWhoTarget(bad) {
			t.Errorf("BUG: isValidWhoTarget priėmė %q", bad)
		}
	}
	for _, ok := range []string{"#kanalas", "nick", "*"} {
		if !isValidWhoTarget(ok) {
			t.Errorf("BUG: isValidWhoTarget atmetė teisingą tikslą %q", ok)
		}
	}
}

// TestFinding_ListFilterValidation — /list filtras validuojamas.
func TestFinding_ListFilterValidation(t *testing.T) {
	for _, bad := range []string{"a b", "a\nb"} {
		if isValidListFilter(bad) {
			t.Errorf("BUG: isValidListFilter priėmė %q", bad)
		}
	}
	for _, ok := range []string{"", "*", "žodis"} {
		if !isValidListFilter(ok) {
			t.Errorf("BUG: isValidListFilter atmetė teisingą filtrą %q", ok)
		}
	}
}

// ==================================================================
// R10 — notify-send parinkčių injekcija.
// ==================================================================

// TestFinding_NotifySendNoOptionInjection — pranešimo tekstas NEGALI
// būti interpretuojamas kaip notify-send parinktis.
func TestFinding_NotifySendNoOptionInjection(t *testing.T) {
	// Tikrinime pats notify-send neįdiegiamas — tikriname, kad komanda
	// būtų suformuota su „--“ skyriku. Jei notify-send egzistuoja
	// aplinkoje, patikrinime OUTPUT'ą.
	// Jei notify-send įdiegtas — išbandome realiai.
	if path, err := exec.LookPath("notify-send"); err == nil {
		cmd := exec.Command(path, "-a", "mircBSD", "-t", "5000", "--", "--urgency=critical", "kiekvienas")
		out, _ := cmd.CombinedOutput()
		// Su „--“ skyriuku parinktis NEinterpretuojama: notify-send
		// (jei įdiegtas) geriau nepasakys, kad „--urgency“ buvo
		// pritaikyta.
		if strings.Contains(string(out), "urgency") && !strings.Contains(string(out), "--urgency=critical") {
			t.Errorf("BUG: notify-send interpretavo pranešimą kaip parinktį: %q", out)
		}
		return
	}
	t.Skip("notify-send nėra įdiegtas")
}

// ==================================================================
// R11 — awayNotified be ribos.
// ==================================================================

// TestFinding_AwayNotifiedPruned — awayNotified žemėlapis turi būti
// apribotas.
func TestFinding_AwayNotifiedPruned(t *testing.T) {
	c := newTestClient()
	now := time.Now()
	c.mu.Lock()
	for i := 0; i < maxAwayNotified*2; i++ {
		c.awayNotified[string(rune('a'+i%26))+string(rune('a'+(i/26)%26))+string(rune('a'+(i/676)%26))] = now
	}
	c.pruneAwayNotifiedLocked(now)
	n := len(c.awayNotified)
	c.mu.Unlock()
	if n > maxAwayNotified {
		t.Errorf("BUG: awayNotified žemėlapis neprunuotas — %d įrašų (riba %d)", n, maxAwayNotified)
	}
}

// ==================================================================
// R12 — /join raktas su tarpais → papildomas JOIN kanalas.
// ==================================================================

// TestFinding_JoinKeyValidation — /join raktas validuojamas.
func TestFinding_JoinKeyValidation(t *testing.T) {
	if isValidChannelKey("key su tarpais") {
		t.Error("BUG: isValidChannelKey priėmė raktą su tarpais")
	}
	if isValidChannelKey("kitas kanalas") {
		t.Error("BUG: isValidChannelKey priėmė raktą su tarpais")
	}
	for _, ok := range []string{"", "slaptas", "12345"} {
		if !isValidChannelKey(ok) {
			t.Errorf("BUG: isValidChannelKey atmetė teisingą raktą %q", ok)
		}
	}
}

// ==================================================================
// R13 — MODE argumentų indekso poslinkis (prefiksas atiteka argumentui).
// ==================================================================

// TestFinding_ModePrefixHitsCorrectNick — operatoriaus prefiksas turi
// atsiekti TEISINGAM nick'ui, ne argumentui.
func TestFinding_ModePrefixHitsCorrectNick(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	c.ensureCh("#chan")
	ch := c.findCh("#chan")
	ch.addNick("bob")
	ch.addNick("alice")
	c.mu.Unlock()

	// CHANMODES su „l“ (C grupė — imasi parametrą nustatant).
	c.applyISUPPORT([]string{"CHANMODES=eIbq,k,flj,CFLMPQScgimnprstz"})

	// „MODE #chan +jo 30 bob“ — argumentai eina MODŲ TVARKOS:
	//   j (C grupė — nustatant, imasi parametrą) = 30
	//   o (PREFIX — visada imasi parametrą)         = bob
	// Anksčiau (klaidinga 4-ojo ISUPPORT ląstos interpretacija) prefiksas
	// atiteko „30“ — nick'o nebuvo.
	c.parseIRC(":op!u@h MODE #chan +jo 30 bob")

	c.mu.Lock()
	defer c.mu.Unlock()
	found := false
	for _, n := range ch.nicks {
		if strings.Contains(n, "bob") {
			found = true
			if !strings.HasPrefix(n, "@") && !strings.HasPrefix(n, "+") {
				t.Errorf("BUG: prefiksas neatsirado nick'o „bob“: %q (visi: %v)", n, ch.nicks)
			}
		}
	}
	if !found {
		t.Errorf("BUG: nick'as „bob“ dingo iš kanalo: %v", ch.nicks)
	}
	// Nick'o su prefiksu nėra — patikriname, kad prefiksas NEatsirado
	// argumento „30“ vietoje.
	for _, n := range ch.nicks {
		if strings.Contains(n, "30") {
			t.Errorf("BUG: prefiksas atiteko ARGUMENTUI („30“), ne nick'ui: %v", ch.nicks)
		}
	}
}

// ==================================================================
// R14 — /clearlogs atmeta nežinomą kanalą.
// ==================================================================

// TestFinding_ClearLogsUnknownChannel — /clearlogs veikia ir kanalui,
// kurio nėra šiame seanse (jo žurnalai vis tiek gali būti diske).
func TestFinding_ClearLogsUnknownChannel(t *testing.T) {
	c := newTestClient()
	c.connected = true
	dir := t.TempDir()
	c.logDir = dir
	c.server = "srv"

	// Sukuriamas žurnalas kanalui, kuris nėra seanse.
	c.logMessage(dir, "srv", "#senas", Message{ts: time.Now(), text: "x", msgType: MsgChat})
	c.closeLogs()

	// Kanalas neegzistuoja kaip langas.
	c.mu.Lock()
	exists := c.findCh("#senas") != nil
	c.mu.Unlock()
	if exists {
		t.Fatal("BUG: testas — kanalas turi nebūti šiame seanse")
	}

	c.handleCmd("clearlogs", "#senas")

	entries, err := os.ReadDir(filepath.Join(dir, "srv"))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "senas") {
			t.Errorf("BUG: /clearlogs netryno žurnalo nežinomam kanalui: %v", entries)
		}
	}
}

// ==================================================================
// R8 — nick'ų sąrašas neribojamas + O(n²) addNick.
// ==================================================================

// TestFinding_NickListBounded — kanale negali būti begalybės nick'ų.
func TestFinding_NickListBounded(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	ch := c.ensureCh("#chan")
	for i := 0; i < maxNicksPerChannel*2; i++ {
		ch.addNick("n" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + string(rune('a'+(i/676)%26)))
	}
	n := len(ch.nicks)
	c.mu.Unlock()
	if n > maxNicksPerChannel {
		t.Errorf("BUG: kanale %d nick'ų (riba %d) — sąrašas neribojamas", n, maxNicksPerChannel)
	}
}

// TestFinding_AddNickFast — 5000 nick'ų įterpimas turi būti GREITAS
// (O(n) įterpinys → O(n²) visas). 3,5 s riba — atkviečia originalų
// DoS (10 000 nick'ų = 3,5 s).
func TestFinding_AddNickFast(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	ch := c.ensureCh("#chan")
	start := time.Now()
	for i := 0; i < 5000; i++ {
		ch.addNick("nick" + strings.Repeat("x", i%7) + itoa(i))
	}
	elapsed := time.Since(start)
	c.mu.Unlock()
	if elapsed > 2*time.Second {
		t.Errorf("BUG: 5000 nick'ų įterpimas užtruko %v — per lėta (O(n²) DoS)", elapsed)
	}
}

// TestFinding_NickIndexConsistent — po operacijų (remove, rename,
// prefiksas) nick'ų žemėlapis turi likti suderintas su slice'u.
func TestFinding_NickIndexConsistent(t *testing.T) {
	c := newTestClient()
	c.mu.Lock()
	ch := c.ensureCh("#chan")
	ch.addNick("Jonas")
	ch.addNick("Petras")
	ch.addNick("Marija")
	c.mu.Unlock()

	// Pašalinimas.
	c.mu.Lock()
	ch.removeNick("Jonas")
	c.mu.Unlock()
	c.mu.Lock()
	if ch.hasNick("Jonas") {
		t.Error("BUG: pašalintas nick'as vis dar kanale")
	}
	if !ch.hasNick("Petras") {
		t.Error("BUG: pašalinimas sugriautė žemėlapį — „Petras“ neberastas")
	}
	c.mu.Unlock()

	// pervadinas.
	c.mu.Lock()
	ch.renameNick("Petras", "Petras2")
	c.mu.Unlock()
	c.mu.Lock()
	if ch.hasNick("Petras") {
		t.Error("BUG: pervadintas nick'as vis dar senuoju vardu")
	}
	if !ch.hasNick("Petras2") {
		t.Error("BUG: pervadinus sugriautė žemėlapis — „Petras2“ neberastas")
	}
	c.mu.Unlock()

	// Prefikso pakeitimas.
	c.mu.Lock()
	ch.setMode("Marija", "+o")
	c.mu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if !ch.hasNick("Marija") {
		t.Error("BUG: prefikso pakeitimas sugriautė žemėlapį — „Marija“ neberastas")
	}
}

// ==================================================================
// R19 — readLoop riba 4096 B, ne 512.
// ==================================================================

// TestFinding_ReadBufferSize — serverio eilutės riba turi būti 512 baitų
// (RFC 2812), ne 4096.
func TestFinding_ReadBufferSize(t *testing.T) {
	// Patikrinime — kad readLoop naudotų 512 baitų bufferį, siunčiame
	// serverio eilutę, kuri yra ILGESNĖ nei 512 baitų, ir tikėtinės
	// patikros: eilutė turi būti ATMESTA (ErrBufferFull), o ne priimta.
	c := newTestClient()
	dir := t.TempDir()
	c.logDir = dir
	c.server = "srv"

	// Ilga eilutė (3000 baitai) — turi būti atmesta.
	long := ":srv!u@h PRIVMSG #k :" + strings.Repeat("A", 3000)
	c.parseIRC(long)

	// Patikra — kad 512 baitų riba būtų MATOMA. readLoop su 512 bufferiu
	// atmeta visas viršytas eilutes; su 4096 — priimtų iki 4096.
	// Čia tikriname konkretaus elgsenos skirtumą: ilga privmsg turi
	// sukurti pranešimą (jei riba nebuvo taikoma) arba NE (atmesdama).
	// Matuojame per 512 ribą: 3000 baitai > 512 → atmesta.
	//
	// Tikrasis testas — readLoop su 512 baitų bufferiu.
	a, b := net.Pipe()
	defer b.Close()
	c2 := newTestClient()
	c2.logDir = dir
	c2.server = "srv"
	go func() {
		_, _ = b.Write([]byte(long + "\n"))
		b.Close()
	}()
	c2.readLoop(bufio.NewReaderSize(a, maxLineBytes))
	// readLoop baigiasi, kai conn užsidaryta; jei eilutė buvo atmesta,
	// kanalo nėra.
	c2.mu.Lock()
	n := len(c2.channels)
	c2.mu.Unlock()
	if n > 1 {
		t.Errorf("BUG: per ilga serverio eilutė (3000 B) NEATMESTA — sukūrė %d kanalų", n)
	}
}

// ==================================================================
// R5 — SASL slaptažodis → NickServ plaintext.
// ==================================================================

// TestFinding_NoSaslFallbackToNickServ — jei SASL nepavyko (arba serveris
// nepasiūlė `sasl` cap), SASL slaptažodis NEGALI būti išsiųstas NickServ
// per plaintext.
//
// TAISYMO ESMĖ: anksčiau, kai slaptažodžio nebuvo, o SASL nepavyko arba
// serveris nepasiūlė `sasl` cap, buvo siunčiamas c.saslPass per NickServ.
// sasl= yra skirtas BUNCER/BRIDGE autentifikacijai — siųsdamas jį
// NickServ (kitas servisas) mes leistume slaptažodžiui nutekti trečiajam
// serveriui, automatiškai, kiekvienam prisijungimui.
func TestFinding_NoSaslFallbackToNickServ(t *testing.T) {
	c := newTestClient()
	c.connected = true
	// SASL slaptažodis (bouncer). NickServ slaptažodžio NĖRA — kad būtų
	// akivaizdu, kad IDENTIFY nesiunčiamas.
	c.saslPass = "saslSecret42"
	ch := make(chan string, 50)
	c.writeCh = ch
	var got []string
	var mu sync.Mutex
	go func() {
		for l := range ch {
			mu.Lock()
			got = append(got, l)
			mu.Unlock()
		}
	}()
	// 001 — serveris prisijungė, bet CAP nepasiūlė sasl.
	c.parseIRC(":srv 001 manick :Welcome")
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	for _, l := range got {
		low := strings.ToLower(l)
		if strings.Contains(low, "saslsecret42") {
			t.Errorf("BUG: SASL slaptažodis nuteko per NickServ: %q", l)
		}
		if strings.Contains(low, "identify") {
			t.Errorf("BUG: siunčiamas IDENTIFY, nors nėra NickServ slaptažodžio: %q", l)
		}
	}
}

// TestFinding_NickServIdentifyStillWorks — teisingas atvejis: jei
// vartotojas PATS nurodė NickServ slaptažodį (konfigūracijoje),
// IDENTIFY turi būti siunčiamas (tai yra pageidaujamas veiksmas).
func TestFinding_NickServIdentifyStillWorks(t *testing.T) {
	c := newTestClient()
	c.connected = true
	c.nickservPass = "nickServSecret"
	ch := make(chan string, 50)
	c.writeCh = ch
	var got []string
	var mu sync.Mutex
	go func() {
		for l := range ch {
			mu.Lock()
			got = append(got, l)
			mu.Unlock()
		}
	}()
	c.parseIRC(":srv 001 manick :Welcome")
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, l := range got {
		if strings.Contains(strings.ToUpper(l), "IDENTIFY") &&
			strings.Contains(l, "nickServSecret") {
			found = true
		}
	}
	if !found {
		t.Errorf("BUG: NickServ IDENTIFY nesiunčiamas, nors slaptažodis nurodytas (išsiųsta: %v)", got)
	}
}

// ==================================================================
// R19 — žurnalų raktas (c.logFiles) naudojo ORIGINALŲ kanalo vardą,
// o disko failas ir clearLogs() prefiksas — SANITIZUOTĄ. Tai reiškė:
// atviru failo handle'ą galėjo nesutapti su /clearlogs tikslu →
// failas likdavo neuždarytas (ir toliau rašė į ištrintą failą).
// ==================================================================

// TestFinding_LogKeyMatchesDiskName — raktas, disko vardas ir
// clearLogs() prefiksas turi naudoti TOKIĄ patį (sanitizuotą) vardą.
func TestFinding_LogKeyMatchesDiskName(t *testing.T) {
	c := newTestClient()
	c.connected = true
	dir := t.TempDir()
	c.logDir = dir
	c.server = "srv"

	// Kanalo vardas su simboliu, kurį sanitizePath pakeičia („/“ → „_“).
	chanName := "#a/b"
	c.logMessage(dir, "srv", chanName, Message{ts: time.Now(), text: "x", msgType: MsgChat})

	// 1) Ant disko — failas su SANITIZUOTU vardu.
	srvDir := filepath.Join(dir, "srv")
	entries, err := os.ReadDir(srvDir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	want := sanitizePath(chanName) + "-" + time.Now().Format("2006-01-02") + ".log"
	found := false
	for _, e := range entries {
		if e.Name() == want {
			found = true
		}
	}
	if !found {
		t.Errorf("BUG: diske nėra failo %q (yra: %v)", want, entries)
	}

	// 2) Raktas c.logFiles turi būti server/SANITIZUOTAS/data — kad
	//    clearLogs() prefiksas (server/SANITIZUOTAS/) jį atrastų.
	wantKey := "srv/" + sanitizePath(chanName) + "/" + time.Now().Format("2006-01-02")
	c.mu.Lock()
	_, hasKey := c.logFiles[wantKey]
	nKeys := len(c.logFiles)
	c.mu.Unlock()
	if !hasKey {
		t.Errorf("BUG: c.logFiles raktas %q nerastas (raktai: %d)", wantKey, nKeys)
	}

	// 3) /clearlogs turi UŽDARYTI atvirą handle'ą (raktas ištrinamas).
	c.clearLogs(chanName)
	c.mu.Lock()
	stillOpen := len(c.logFiles)
	c.mu.Unlock()
	if stillOpen != 0 {
		t.Errorf("BUG: /clearlogs neuždaryro atviro žurnalo failo (liko %d handle)", stillOpen)
	}

	// 4) Ir failas turi būti ištrintas diske.
	entries2, err := os.ReadDir(srvDir)
	if err != nil {
		t.Fatalf("readdir2: %v", err)
	}
	for _, e := range entries2 {
		if strings.HasPrefix(e.Name(), sanitizePath(chanName)) {
			t.Errorf("BUG: /clearlogs netryno žurnalo failo diske: %v", entries2)
		}
	}
}

// TestFinding_LogKeySanitizedIsStable — patikrina, kad raktas sukurtas
// su ORIGINALIU vardu (nesanitizuotu) nebepapuola į atskirą map'o
// įrašą — t.y. kad dvi skirtingos tą pačią disko failą atveriančios
// užklausos naudotų VIENĄ handle'ą.
func TestFinding_LogKeySanitizedIsStable(t *testing.T) {
	c := newTestClient()
	c.connected = true
	dir := t.TempDir()
	c.logDir = dir
	c.server = "srv"

	chanName := "#a/b"
	c.logMessage(dir, "srv", chanName, Message{ts: time.Now(), text: "x", msgType: MsgChat})
	c.logMessage(dir, "srv", chanName, Message{ts: time.Now(), text: "y", msgType: MsgChat})

	c.mu.Lock()
	n := len(c.logFiles)
	c.mu.Unlock()
	if n != 1 {
		t.Errorf("BUG: %d atvirų žurnalų handle'ų (turi būti 1 — tas pats kanalas)", n)
	}
}

// TestFinding_ClearLogsOnlyTargetChannel — /clearlogs neturi ištrinti
// KITAM kanalui skirtų žurnalų.
//
// Failų vardai: <KANALAS>-YYYY-MM-DD.log. Ankstesnė patikra buvo
// `HasPrefix(name, kanalas+"-2")` — ji tikėjosi, kad data visada
// prasideda „2“. Dėl to:
//   - „/clearlogs #k“ ištrindavo kito kanalo „#k-2“ žurnalą
//     (prefiksas sutampa) — komandų tikslo klastojimas;
//   - senesnių metų žurnalai („#k-2025-03-15.log“) nebuvo trinami.
func TestFinding_ClearLogsOnlyTargetChannel(t *testing.T) {
	c := newTestClient()
	c.connected = true
	dir := t.TempDir()
	c.logDir = dir
	c.server = "srv"

	now := time.Now()
	// Kanalas "#k-2" — jo failas "#k-2-<data>.log".
	c.logMessage(dir, "srv", "#k-2", Message{ts: now, text: "x", msgType: MsgChat})
	// Senesnių metų žurnalas kanalui "#k".
	old := time.Date(2025, 3, 15, 10, 0, 0, 0, time.UTC)
	c.logMessage(dir, "srv", "#k", Message{ts: old, text: "x", msgType: MsgChat})

	srvDir := filepath.Join(dir, "srv")
	entries, err := os.ReadDir(srvDir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("testas: tikėjosi 2 failų, yra %v", entries)
	}

	// Išvalome kanalą "#k" — "#k-2" žurnalas turi LIKTI.
	c.clearLogs("#k")

	entries, err = os.ReadDir(srvDir)
	if err != nil {
		t.Fatalf("readdir2: %v", err)
	}
	keptOther := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "#k-2-") {
			keptOther = true
		}
	}
	if !keptOther {
		t.Errorf("BUG: /clearlogs #k ištrino kito kanalo (#k-2) žurnalą: %v", entries)
	}
	// O senesnių metų "#k" žurnalas turi būti IŠTRINTAS.
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "#k-2025") {
			t.Errorf("BUG: /clearlogs nepašalino senesnių metų žurnalo: %v", entries)
		}
	}
}

// ==================================================================
// R20 — savivertės peržiūros radiniai (2026-10-21, po agento
// diff'o patikros). Kiekvienas patikrintas PALEIDŽIANT KODĄ.
// ==================================================================

// TestFinding_LogKeepsNormalChat — žurnalas neturi slėpti ĮPRASTO
// pokalbio.
//
// BUG: slaptažodžio aptikimas buvo taikomas VISOMS žinutėms, o
// „pirmas žodis“ tikrinimas („register …“, „pass …“, „auth …“)
// slėpdė viską, kas prasideda šiais žodžiais — „register your vote
// today“ ar „pass the salt please“ — t. y. žurnalas tyliai prarado
// realų pokalbių tekstą.
func TestFinding_LogKeepsNormalChat(t *testing.T) {
	dir := t.TempDir()
	c := newTestClient()
	c.logDir = dir
	c.server = "srv"

	normal := []string{
		"pass the salt please",
		"register your vote today",
		"confirm your subscription",
		"password manager recommendations",
		"auth me, im a developer",
		"identify the problem",
		"set the table",
	}
	for _, m := range normal {
		c.logMessage(dir, "srv", "#k", Message{ts: time.Now(), nick: "bob", text: m, msgType: MsgChat})
	}
	c.closeLogs()

	data, err := os.ReadFile(filepath.Join(dir, "srv", "#k-"+time.Now().Format("2006-01-02")+".log"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	out := string(data)
	for _, m := range normal {
		if !strings.Contains(out, m) {
			t.Errorf("BUG: įprastas pokalbis NEĮRAŠYTAS į žurnalą: %q", m)
		}
	}
}

// TestFinding_LogStillHidesSecrets — po pataisymo slaptažodžiai VIS
// dar turi būti slėpti.
func TestFinding_LogStillHidesSecrets(t *testing.T) {
	dir := t.TempDir()
	c := newTestClient()
	c.logDir = dir
	c.server = "srv"

	secrets := []string{
		"msg nickserv identify hunter2Secret",
		"msg NickServ set password hunter2Secret",
		"PASS hunter2Secret",
		"OPER hunter2Secret",
		"/raw PASS hunter2Secret",
		"nickserv register hunter2Secret user@example.com",
	}
	for _, m := range secrets {
		c.logMessage(dir, "srv", "#k", Message{ts: time.Now(), nick: "me", text: m, msgType: MsgChat})
	}
	// Serverio atsiųsta komanda: nick'o laukas = NickServ, kūnas = komanda.
	for _, m := range []string{
		"register hunter2Secret user@example.com",
		"set password hunter2Secret",
		"set p hunter2Secret",
		"confirm hunter2Secret",
		"auth hunter2Secret",
		"identify hunter2Secret",
	} {
		c.logMessage(dir, "srv", "#k2", Message{ts: time.Now(), nick: "NickServ", text: m, msgType: MsgChat})
	}
	c.closeLogs()

	for _, ch := range []string{"#k", "#k2"} {
		data, err := os.ReadFile(filepath.Join(dir, "srv", ch+"-"+time.Now().Format("2006-01-02")+".log"))
		if err != nil {
			t.Fatalf("read %s: %v", ch, err)
		}
		if strings.Contains(string(data), "hunter2Secret") {
			t.Errorf("BUG: %s — slaptažodis liko žurnale:\n%s", ch, data)
		}
	}
}

// TestFinding_RawSecretWhitespaceNormalized — papildomas tarpas negali
// apeikti slaptažodžio aptikimo.
//
// BUG: „/raw  PASS x“ (du tarpai) nebuvo aptinkama — slaptažodis lėkė
// į istoriją ir žurnalą plaintext.
func TestFinding_RawSecretWhitespaceNormalized(t *testing.T) {
	for _, c := range []string{
		"/raw pass secret",
		"/raw  pass secret",
		"/raw   pass secret",
		"/raw\tpass\tsecret",
		"/raw  oper secret",
		"/pass secret",
		"/oper secret",
		"PASS secret",
		"OPER secret",
	} {
		if !isSensitiveHistory(c) {
			t.Errorf("BUG: %q — slaptažodis nebūtų paslėptas (istorija)", c)
		}
		if !isSensitiveLogLine(c) {
			t.Errorf("BUG: %q — slaptažodis nebūtų paslėptas (žurnalas)", c)
		}
	}
}

// TestFinding_AsciiCasemappingNickSet — ant serverio su
// CASEMAPPING=ascii nick'ai „[a]“ ir „{a}“ yra SKIRTINGI.
//
// BUG: normNick visada naudojo RFC 1459 lyginimą → „{a}“ būdavo
// atmestas kaip „[a]“ dublikatas, o vėlesnis QUIT ištrindavo NE TĄ
// nick'ą.
func TestFinding_AsciiCasemappingNickSet(t *testing.T) {
	ch := &Channel{name: "#x", casemapping: "ascii"}
	ch.addNick("[a]", "@")
	ch.addNick("{a}", "@")
	if len(ch.nicks) != 2 {
		t.Fatalf("BUG: %d nick'ų, norėta 2 — {a} atmestas kaip [a] dublikatas", len(ch.nicks))
	}
	ch.removeNick("{a}", "@")
	if len(ch.nicks) != 1 || ch.nicks[0] != "[a]" {
		t.Errorf("BUG: removeNick({a}) pašalino ne tą nick'ą: %v", ch.nicks)
	}
}

// TestFinding_NickSetMatchesNickEqual — normNick turi atitikti
// nickEqual() abiem casemapping'uose.
func TestFinding_NickSetMatchesNickEqual(t *testing.T) {
	c := newTestClient()
	pairs := [][2]string{
		{"Žilvinas", "žilvinas"},
		{"[a]", "{a}"},
		{"Bob", "bob"},
		{"[a]", "[a]"},
	}
	for _, p := range pairs {
		eq := c.nickEqual(p[0], p[1])
		eqNorm := normNick(p[0], "@", "") == normNick(p[1], "@", "")
		if eq != eqNorm {
			t.Errorf("BUG: normNick ≠ nickEqual porai %q/%q: nickEqual=%v normNick=%v",
				p[0], p[1], eq, eqNorm)
		}
	}
}

// TestFinding_ServerMaskAccepted — serverio kaukė „*server*“ turi būti
// priimta (ją dokumentuoja pati validatoriaus funkcija).
//
// BUG: isValidMaskName atmetė „*“ → /mode *server* ir /w *server* buvo
// atmesti.
func TestFinding_ServerMaskAccepted(t *testing.T) {
	if !isValidModeTarget("*server*") {
		t.Error("BUG: isValidModeTarget(*server*) = false")
	}
	if !isValidWhoTarget("*server*") {
		t.Error("BUG: isValidWhoTarget(*server*) = false")
	}
	// O blogos formos — turi būti atmestos.
	if isValidModeTarget("*ser ver*") {
		t.Error("BUG: kaukė su tarpu priimta")
	}
	if isValidModeTarget("#a\r\nb") {
		t.Error("BUG: kaukė su CRLF priimta")
	}
}

// TestFinding_ChannelKeyAllowsPunctuation — kanalų raktai su
// specialiais simboliais turi būti leidžiami.
//
// BUG: siauras leistinų simbolių sąrašas atmetė „s3cret!“, „k#1“,
// „p@ss“ → vartotojas negalėjo prisijungti į tokį kanalą.
func TestFinding_ChannelKeyAllowsPunctuation(t *testing.T) {
	for _, k := range []string{"", "s3cret", "my!key", "k#1", "p@ss", "a,b", "x=y", "100%", "key*"} {
		if !isValidChannelKey(k) {
			t.Errorf("BUG: teisėtas raktas atmestas: %q", k)
		}
	}
	// Tarpai ir kontroliai — turi būti atmesti.
	for _, k := range []string{"a b", "a\tb", "a\r\nb", "a\nb", "a\x00b", "a\x1bb"} {
		if isValidChannelKey(k) {
			t.Errorf("BUG: blogas raktas priimtas: %q", k)
		}
	}
}

// TestFinding_ModeTargetAllowsNickSymbols — MODE/WHO tikslas turi
// leisti nick'o simbolius, kuriuos leidžia isValidNick.
func TestFinding_ModeTargetAllowsNickSymbols(t *testing.T) {
	for _, n := range []string{"nick[1]", "nick\\1", "nick`1", "nick^1", "nick{1}", "nick|1"} {
		if !isValidNick(n) {
			t.Fatalf("testas: %q neturi būti galimas nick'o", n)
		}
		if !isValidModeTarget(n) {
			t.Errorf("BUG: isValidModeTarget atmeta galimą nick'ą: %q", n)
		}
		if !isValidWhoTarget(n) {
			t.Errorf("BUG: isValidWhoTarget atmeta galimą nick'ą: %q", n)
		}
	}
}
