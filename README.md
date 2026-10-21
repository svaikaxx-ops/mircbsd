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
| `/search [žodis]` | Paieška (arba Ctrl+R) |
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
| `Tab` | Automatinis užbaigimas (nick, komandos) |
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
| `~/irclogs/<serveris>/<kanalas>-YYYY-MM-DD.log` | Žurnalai (rotacija per dieną) |

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

v0.4.4 – lygiagretumo, ribų ir komandų tikslo pataisymai: data race (aktyvus kanalas, `draw()`, `AWAY`, `/keys`), žurnalo slaptažodžių nutekimas, gyvas terminalo sinkas per Tab-completion (OSC 52), kanalų su tarpais komandų tikslo klastojimas, PM flood užplėčiantis kanalų biudžetą, O(n²) nick'ų DoS, `CHANMODES` argumentų indeksai (zr. CHANGELOG.md)
