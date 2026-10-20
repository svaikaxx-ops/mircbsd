package main

// K12 + K13 radiniai: nick'o validacija (ilgis simboliais, ne baitais) ir
// validacija per visus įėjimo kelius (config, komandinė eilutė).
//
// Pamoka: kiekvienas validatoriumas turi DU testus — „blogas atmetamas“ IR
// „teisingas priimamas“.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ============================================================
// K12 — nick'o ilgis skaičiuojamas simboliais
// ============================================================

// TestIsValidNickAcceptsLithuanian — lietuviškasis vartotojas gali
// nustatyti savo nick'ą (15 lietuviškų raidžių = 30 baitų).
func TestIsValidNickAcceptsLithuanian(t *testing.T) {
	valid := []string{
		"jonas",
		"Žilvinas",
		"ąžuolas",
		"šuo",
		"ėdge",
		"ūkis",
		"žmuoga",
		strings.Repeat("ą", 30), // 30 simbolių = 60 baitų — riboje
	}
	for _, n := range valid {
		if !isValidNick(n) {
			t.Errorf("isValidNick(%q) = false, turėtų būti true", n)
		}
	}
}

// TestIsValidNickRejectsTooLongByRunes — 31 simbolis atmetamas, ne 31 baitas.
func TestIsValidNickRejectsTooLongByRunes(t *testing.T) {
	if isValidNick(strings.Repeat("a", 31)) {
		t.Error("isValidNick(31 simboliai) = true, turėtų būti false")
	}
	if isValidNick(strings.Repeat("ą", 31)) {
		t.Error("isValidNick(31 lietuviška raidė) = true, turėtų būti false")
	}
	// 16 lietuviškų raidžių = 32 baitai, bet 16 simbolių — PRIIMTINAS.
	if !isValidNick(strings.Repeat("ą", 16)) {
		t.Error("isValidNick(16 lietuviškų raidžių) = false, turėtų būti true")
	}
}

// TestIsValidNickStillRejectsControlAndSpaces — saugumo reikalas: valdymo
// simboliai ir tarpai lieka neleistini.
func TestIsValidNickStillRejectsControlAndSpaces(t *testing.T) {
	invalid := []string{
		"jo nas",
		"jo\rnas",
		"jo\nnas",
		"jo\x00nas",
		"jo\x1bnas",
		"jo:nas",
		"jo,nas",
		"jo.nas",
		"jo/nas",
		"",
	}
	for _, n := range invalid {
		if isValidNick(n) {
			t.Errorf("isValidNick(%q) = true, turėtų būti false", n)
		}
	}
}

// ============================================================
// isValidPort
// ============================================================

// TestIsValidPort — portai.
func TestIsValidPort(t *testing.T) {
	valid := []string{"1", "80", "6667", "6697", "65535"}
	for _, p := range valid {
		if !isValidPort(p) {
			t.Errorf("isValidPort(%q) = false, turėtų būti true", p)
		}
	}
	invalid := []string{"", "0", "65536", "99999", "abc", "66 67", "-1", "6667.0", "0x1F90"}
	for _, p := range invalid {
		if isValidPort(p) {
			t.Errorf("isValidPort(%q) = true, turėtų būti false", p)
		}
	}
}

// ============================================================
// K13 — validacija per visus įėjimo kelius
// ============================================================

// TestNewClientValidatesNickFromConfig — blogas nick iš config'o neturi
// pasiekti NICK komandos.
func TestNewClientValidatesNickFromConfig(t *testing.T) {
	cfg := defaultConfig()
	cfg.Nick = "blogas nick"
	c := newClient(cfg)
	if c.nick == "blogas nick" {
		t.Error("BUG: blogas nick iš config'o pateko į klientą")
	}
	if !isValidNick(c.nick) {
		t.Errorf("BUG: kliento nick %q nėra galiojantis", c.nick)
	}
}

// TestNewClientAcceptsValidLithuanianNick — teisingas atvejis: lietuviškas
// nick iš config'o PRIIMAMAS.
func TestNewClientAcceptsValidLithuanianNick(t *testing.T) {
	cfg := defaultConfig()
	cfg.Nick = "Žilvinas"
	c := newClient(cfg)
	if c.nick != "Žilvinas" {
		t.Errorf("BUG: lietuviškas nick %q pakeistas į %q", "Žilvinas", c.nick)
	}
}

// TestNewClientValidatesPortFromConfig — blogas portas neturi pasiekti
// adreso.
func TestNewClientValidatesPortFromConfig(t *testing.T) {
	cfg := defaultConfig()
	cfg.Port = "ne-portas"
	c := newClient(cfg)
	if c.port == "ne-portas" {
		t.Error("BUG: blogas portas iš config'o pateko į klientą")
	}
	if !isValidPort(c.port) {
		t.Errorf("BUG: kliento portas %q nėra galiojantis", c.port)
	}
}

// TestNewClientValidatesServerFromConfig — blogas serverio vardas neturi
// pasiekti IRC komandų.
func TestNewClientValidatesServerFromConfig(t *testing.T) {
	cfg := defaultConfig()
	cfg.Server = "blogas\r\nJOIN #x"
	c := newClient(cfg)
	if c.server != "" {
		t.Errorf("BUG: blogas serverio vardas %q pateko į klientą", c.server)
	}
}

// TestLoadConfigRejectsBadNickAndPort — config faile blogas nick/portas
// turi būti atmesti (su įspėjimu).
func TestLoadConfigRejectsBadNickAndPort(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	content := "nick=blogas nick\nport=ne-portas\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg := loadConfig(path)
	if cfg.Nick == "blogas nick" {
		t.Error("BUG: blogas nick iš config failo priimtas")
	}
	if cfg.Port == "ne-portas" {
		t.Error("BUG: blogas portas iš config failo priimtas")
	}
	warns := loadConfigWarnings()
	if len(warns) < 2 {
		t.Errorf("BUG: tik %d įspėjimų (norėtų bent 2): %v", len(warns), warns)
	}
}

// TestLoadConfigAcceptsGoodNickAndPort — teisingas atvejis: lietuviškas nick
// ir normalus portas PRIIMAMI.
func TestLoadConfigAcceptsGoodNickAndPort(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	content := "nick=Žilvinas\nport=6697\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg := loadConfig(path)
	if cfg.Nick != "Žilvinas" {
		t.Errorf("BUG: lietuviškas nick %q pakeistas į %q", "Žilvinas", cfg.Nick)
	}
	if cfg.Port != "6697" {
		t.Errorf("BUG: portas %q pakeistas į %q", "6697", cfg.Port)
	}
	if warns := loadConfigWarnings(); len(warns) != 0 {
		t.Errorf("BUG: teisingiems parametrams atsirado įspėjimų: %v", warns)
	}
}

// ============================================================
// K17 — isValidChannel turi žinoti serverio CHANTYPES
// ============================================================

// TestIsValidChannelAcceptsCommonPrefixes — standartiniai kanalių prefiksai
// priimami.
func TestIsValidChannelAcceptsCommonPrefixes(t *testing.T) {
	for _, ch := range []string{"#kanalas", "&kanalas", "!kanalas", "+kanalas"} {
		if !isValidChannel(ch) {
			t.Errorf("isValidChannel(%q) = false, turėtų būti true", ch)
		}
	}
}

// TestIsValidChannelRejectsBad — be prefikso, su tarpu, su CRLF.
func TestIsValidChannelRejectsBad(t *testing.T) {
	for _, ch := range []string{"", "#", "# kanalas", "#kanalas\r\nJOIN #x", "#kanalas|cmd"} {
		if isValidChannel(ch) {
			t.Errorf("isValidChannel(%q) = true, turėtų būti false", ch)
		}
	}
}

// TestIsValidChannelRespectsChantypes — kanalo prefiksas tikrinamas pagal
// serverio CHANTYPES (K17). Serveris su CHANTYPES="$#" turi priimti "$kanalas".
func TestIsValidChannelRespectsChantypes(t *testing.T) {
	// Standartinės: "#" ir "&" — priimami.
	if !isValidChannelFor("#kanalas", "#&") {
		t.Error("isValidChannelFor(\"#kanalas\", \"#&\") = false, turėtų būti true")
	}
	if !isValidChannelFor("&kanalas", "#&") {
		t.Error("isValidChannelFor(\"&kanalas\", \"#&\") = false, turėtų būti true")
	}
	// CHANTYPES="#$": "$" leidžiamas, "&" — ne.
	if !isValidChannelFor("$kanalas", "#$") {
		t.Error("BUG: $kanalas atmestas, nors serverio CHANTYPES=\"#$\"")
	}
	if isValidChannelFor("&kanalas", "#$") {
		t.Error("BUG: &kanalas priimtas, nors serverio CHANTYPES=\"#$\"")
	}
	// Standartinė validacija ($ kanalas standartiniuose CHANTYPES — ne).
	if isValidChannel("$kanalas") {
		t.Error("BUG: $kanalas priimtas standartiniais CHANTYPES")
	}
}

// TestIsValidTargetAcceptsNicksAndChannels — teisingi gavėjai.
func TestIsValidTargetAcceptsNicksAndChannels(t *testing.T) {
	for _, tg := range []string{"nick", "#kanalas", "Žilvinas", "nick_1", "a.b-c"} {
		if !isValidTarget(tg) {
			t.Errorf("isValidTarget(%q) = false, turėtų būti true", tg)
		}
	}
}

// TestIsValidTargetRejectsBad — blogi gavėjai.
func TestIsValidTargetRejectsBad(t *testing.T) {
	for _, tg := range []string{"", "nick nick", "nick\r\nJOIN #x", "nick\x00", "nick;cmd"} {
		if isValidTarget(tg) {
			t.Errorf("isValidTarget(%q) = true, turėtų būti false", tg)
		}
	}
}

// ============================================================
// K21 — isValidServer palaiko IPv6
// ============================================================

// TestIsValidServerAcceptsIPv6 — IPv6 adresai turi būti priimami (anksčiau
// „:“ nebuvo leistinas simbolis, todėl „::1“ atmetamas).
func TestIsValidServerAcceptsIPv6(t *testing.T) {
	good := []string{
		"::1",
		"::",
		"2001:db8::1",
		"fe80::1",
		"1:2:3:4:5:6:7:8",
		"1::2:3:4:5:6:7",
	}
	for _, s := range good {
		if !isValidServer(s) {
			t.Errorf("BUG: isValidServer(%q) = false, turėtų būti true", s)
		}
	}
}

// TestIsValidServerRejectsBadIPv6 — neteisingi IPv6 adresai atmetami.
func TestIsValidServerRejectsBadIPv6(t *testing.T) {
	bad := []string{
		"::1::2",        // du „::“
		"gggg::1",       // ne heksadekšimaliniai
		"12345::1",      // per ilga grupė
		"[::1]",         // ZIP forma (adresas ir portas atskirai)
		"fe80::1%eth0",  // zone ID
		"1:2:3:4:5:6:7", // be „::“ — ne 8 grupės
		"1:2:3:4:5:6:7:8:9",
		"",
	}
	for _, s := range bad {
		if isValidServer(s) {
			t.Errorf("BUG: isValidServer(%q) = true, turėtų būti false", s)
		}
	}
}

// TestJoinHostPortIPv6 — IPv6 adresas jungiamas su portu laiptų formatu.
func TestJoinHostPortIPv6(t *testing.T) {
	cases := []struct{ host, port, want string }{
		{"irc.libera.chat", "6697", "irc.libera.chat:6697"},
		{"127.0.0.1", "6667", "127.0.0.1:6667"},
		{"::1", "6667", "[::1]:6667"},
		{"2001:db8::1", "6697", "[2001:db8::1]:6697"},
	}
	for _, c := range cases {
		if got := joinHostPort(c.host, c.port); got != c.want {
			t.Errorf("BUG: joinHostPort(%q, %q) = %q, norėtų %q", c.host, c.port, got, c.want)
		}
	}
}
