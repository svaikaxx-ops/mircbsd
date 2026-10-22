# mircBSD

Terminalo IRC klientas, parašytas Go. Veikia **Debian** (Linux) ir FreeBSD.

## Reikalavimai

- Debian 12/13 (trixie) arba FreeBSD 14+
- Go 1.24 arba naujesnis (Debian 13 turi `golang-go` = 1.24)
- Tikrasis terminalas (TTY) – paleidžiant be TTY klientas nepasileidžia

## Diegimas – Debian

```sh
sudo apt update
sudo apt install -y golang-go ca-certificates xdg-utils

cd mircbsd
go build -o mircbsd .

# Įdiegimas sau (be root teisių, rekomenduojama)
install -Dm755 mircbsd ~/.local/bin/mircbsd

# Arba visai sistemai
sudo install -m755 mircbsd /usr/local/bin/mircbsd
```

Pastabos:

- `ca-certificates` – reikalingas TLS prisijungimui (portas 6697)
- `xdg-utils` – komandai `/open` (atidaryti URL naršyklėje); jei nėra, ieškoma
  `gio`, `sensible-browser`, `x-www-browser`, `firefox`
- Pirmas `go build` atsiųsdinėja modulius iš interneto (`golang.org/x/term`)
- Paleiskite **ne kaip root** – konfigūracija saugoma `$HOME`

## Diegimas – FreeBSD

```sh
go build -o mircbsd .
cp mircbsd /usr/local/bin/
```

## Konfigūracija

Konfigūracijos katalogas (Linux/FreeBSD XDG taisyklės):

- `$XDG_CONFIG_HOME/mircbsd` – jei `XDG_CONFIG_HOME` nustatytas
- `$HOME/.config/mircbsd` – jei ne (numatytas atvejis)

```sh
mkdir -p "${XDG_CONFIG_HOME:-$HOME/.config}/mircbsd"
cp config.example ~/.config/mircbsd/config
$EDITOR ~/.config/mircbsd/config
```

Konfigūracijos failas: `~/.config/mircbsd/config`

## Paleidimas

```sh
# Su konfigūracijos failu
mircbsd

# Tiesiogiai iš komandinės eilutės
mircbsd irc.libera.chat 6697 manick tls
mircbsd irc.data.lt 6667 manick
```

## Komandos

| Komanda | Aprašymas |
|---------|-----------|
| `/connect <serveris> <portas> [nick] [tls]` | Prisijungti prie serverio |
| `/urls` | URL istorija |
| `/open [n]` | Atidaryti URL naršyklėje |
| `/reconnect` | Jungti iš naujo |
| `/join #kanalas` | Įeiti į kanalą |
| `/part [priežastis]` | Išeiti iš kanalo |
| `/close` | Uždaryti langą (+ PART) |
| `/msg <nick> <žinutė>` | Privati žinutė |
| `/query <nick>` | Atidaryti privatų langą (be žinutės) |
| `/me <veiksmas>` | Veiksmas (/me bėga) |
| `/nick <vardas>` | Keisti slapyvardį |
| `/topic [tema]` | Rodyti / keisti temą |
| `/kick <nick> [priežastis]` | Išmesti iš kanalo |
| `/mode [target] <+/-mode>` | Nustatyti mode |
| `/away [priežastis]` | Away režimas |
| `/back` | Grįžti iš away |
| `/whois <nick>` | Informacija apie vartotoją |
| `/ignore <maskė>` | Ignoruoti vartotoją (palaiko maskes: `*!user@*`, `nick`) |
| `/unignore <nick>` | Atšaukti ignoravimą |
| `/ignores` | Ignoruojamų sąrašas |
| `/list [filtras]` | Kanalų sąrašas |
| `/search [žodis]` | Paieška (arba Ctrl+R); `n`/`p` (su tuščia įvestimi) — kitas/ankstesnis rezultatas |
| `/search all <žodis>` | Paieška visuose languose (rezultatai `*server*`) |
| `/logsearch [kanalas] <žodis>` | Paieška žurnalo failuose (iki 500 atitikmenų) |
| `/set <raktas> [reikšmė]` | Nustatymo peržiūra/keitimas gyvai (įrašoma į config failą) |
| `/exportjson [kanalas]` | Eksportuoti lango žinutes į JSON failą (`~/irclogs/`) |
| `/notify add\|del\|list <nick>` | MONITOR stebimų nick'ų sąrašas (online/offline pranešimai) |
| `/keys [debug]` | Klavišų perrašymus iš `keys` failo + valdymo sekų debug režimas |
| `/clearlogs [kanalas]` | Ištrinti log failus (numatytai: visi šiame serveryje) |
| `/raw <IRC komanda>` | Tiesioginė IRC komanda |
| `/w [nick\|kanalas]` | WHO užklausa — kas yra kanale arba internete (atsakymas rodomas užklaustame kanale) |
| `/window` | Langų sąrašas / perjungimas |
| `/quit [žinutė]` | Išeiti |
| `/help` | Komandų sąrašas |

## Klaviatūros sparčiukai

| Klavišai | Veiksmas |
|---------|---------|
| `PgUp` / `PgDn` | Slinkti žinutėmis |
| `Shift+PgDn` | Šuolis apačion (grįžti prie naujų žinučių) |
| `↑` / `↓` | Komandų istorija |
| `Ctrl+N` / `Ctrl+P` | Kitas / ankstesnis kanalas |
| `F1`–`F12` | Kanalas pagal numerį |
| `Tab` | Automatinis užbaigimas (nick, komandos, kanalai) |
| `Ctrl+R` | Paieška |
| `Ctrl+←` / `Ctrl+→` | Žodžio navigacija |
| `Ctrl+W` | Ištrinti žodį prieš kursorių |
| `Ctrl+↑` / `Ctrl+↓` | Slinkti nick sąrašu |
| **Pelė** | Ratukas: sulinkti; paspaudimas kanalo juostoje: pakeisti (sąrašas suseka paskui aktyvų kanalą) |

## Failų vietos

| Failas | Paskirtis |
|--------|---------|
| `$XDG_CONFIG_HOME/mircbsd/config` (numatytai `~/.config/mircbsd/config`) | Konfigūracija |
| `$XDG_CONFIG_HOME/mircbsd/history` | Komandų istorija |
| `$XDG_CONFIG_HOME/mircbsd/ignore` | Ignoruojamų sąrašas |
| `$XDG_CONFIG_HOME/mircbsd/notify` | MONITOR stebimų nick'ų sąrašas (/notify) |
| `$XDG_CONFIG_HOME/mircbsd/keys` | Klavišų perrašymai (`veiksmas = seka`, v0.4.8) |
| `$XDG_CONFIG_HOME/mircbsd/fingerprints` | TLS serverių atspaudai (pinning, TOFU) |
| `~/irclogs/<serveris>/<kanalas>-YYYY-MM-DD.log` | Žurnalai (rotacija per dieną) |
| `~/irclogs/<serveris>/<kanalas>-YYYY-MM-DD.json` | JSON eksportas (/exportjson) |

## Saugumas

- **Config su slaptažodžiu** (`sasl=`, `nickserv=` arba `password=`) – apribokite teises:

  ```sh
  chmod 600 ~/.config/mircbsd/config
  ```

  Klientas kuriamiems failams (istorija, ignorų sąrašas, žurnalai) pats nustato
  `0700` katalogams ir `0600` failams, bet konfigūracijos failo, kurį kuriate
  patys redaktoriumi, teises keisti nėra ko.
- **Slaptažodžiai nekeliauja į istoriją** – eilutės su `IDENTIFY` (pvz.
  `/msg nickserv IDENTIFY pass`) nėra saugomos `history` faile.
- **Terminalo valdymo simbolių apsauga** – visos tinklo žinutės išvalomos prieš
  rodymą: ESC/C1/C0 kontrolės kodai (ANSI ekrano trynimas, OSC antraščių ir
  iškarpinės injekcija) negali nukeliauti į terminalą.
- **IRC protokolo injekcija** – siunčiamose eilutėse CR/LF pašalinami, todėl
  žinutės tekste neįmanoma „įkišti“ naujos IRC komandos.
- **TLS** – įjungiamas su `tls=true` arba `mircbsd host 6697 nick tls`;
  sertifikatas tikrinamas (`MinVersion: TLS 1.2`). Slaptažodžiams rinkitės
  SASL (`sasl=`), kuris vyksta šifruotu kanalu – be TLS IDENTIFY per Plaintext
  eina atviru tekstu.
- **SASL EXTERNAL** (`sasl_external=true` + `tls_cert`/`tls_key`) –
  autentifikacija kliento sertifikatu, be slaptažodžio.
- **TLS pinning** (`tls_pinning=true`) – serverio sertifikato viešojo rakto
  sha256 atspaudas (TOFU): pirmo jungimo atspaudas išsaugomas
  `fingerprints` faile; radus kita atspaudą, ryšys nutraukiamas dar prieš
  NICK/USER (galimas MITM). `tls_pin=` – rankinis atspaudas.
- **`/set`** perrašo tik nešlamčius raktus (`timestamps`, `theme` ir kt.) –
  nesaugios reikšmės (`sasl`, `password`, `server`, `tls`) per /set
  nekeičiamos.
- **Naudotojo nick/kanalų pavadinimai** tikrinami pagal RFC simbolių rinkinį,
  todėl valdymo simboliai negali patekti net į serverio komandas.
- `/open` atidaro tik `http://`, `https://`, `ftp://` prasidedančius URL.

## Papildomi nustatai

| Raktas | Numatytas | Aprašymas |
|--------|-----------|-----------|
| `mouse` | `true` | Pelės palaikymas |
| `notifications` | `true` | Desktop pranešimai (notify-send) highlight atveju; be notify-send – tyliai praleidžiama |
| `highlight_color` | `red` | Highlight fono spalva (`blue`, `green`, `magenta`, `cyan`, `yellow`, `none`) |
| `nick_colors` | `true` | Nick'ų spalvinimas |
| `tscolor` | `gray` | Laiko žymos spalva |
| `timestamps` | `true` | Laiko žymos prie žinučių |
| `ts_format` | `15:04` | Laiko žymos formatas (pvz. `15:04:05`) |
| `theme` | `dark` | Tema: `light` — šviesiems fonams (tamsesnė nick'ų paletė) |
| `tls_cert` / `tls_key` | – | Kliento sertifikatas SASL EXTERNAL |
| `sasl_external` | `false` | SASL EXTERNAL (autentifikacija sertifikatu) |
| `tls_pinning` | `false` | TLS sertifikato atspaudo pinning (TOFU) |
| `tls_pin` | – | Rankinis atspaudas (sha256 hex, SPKI) |

Visi šie raktai keičiami gyvai per `/set <raktas> <reikšmė>` — reikšmė
iš karto pratąkoma ir įrašoma atgal į config failą.

## Kūrimas (FreeBSD/Linux)

```sh
gofmt -w *.go          # formatavimas
go vet ./...           # static analizė
go test ./...          # testai
CGO_ENABLED=0 go build -o mircbsd .   # statinis binary
```

Kryžminis kūrimas (pvz. iš Debian į FreeBSD):

```sh
GOOS=freebsd GOARCH=amd64 CGO_ENABLED=0 go build -o mircbsd-freebsd .
GOOS=linux   GOARCH=amd64 CGO_ENABLED=0 go build -o mircbsd-linux .
```

## Versija

v0.4.8 – naujos funkcijos (žr. CHANGELOG.md): SASL EXTERNAL, paieškos
navigacija (`n`/`p` + /search all + /logsearch), `/set` su įrašymu į config,
`/exportjson` (JSON eksportas), tema (dark/light), MONITOR + `/notify`,
klavišų perrašymai (`keys` failas), TLS atspaudo pinning (TOFU).
