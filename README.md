# mircBSD

Terminalo IRC klientas, parašytas Rust. Veikia **Debian** (Linux) ir FreeBSD.

```text
 mircBSD | testas @ 127.0.0.1 | #testas       PgUp/Dn:slinkti  Ctrl+N/P:langai  Tab:užbaigti  /help
 ✦ Rust, BSD ir IRC | taisyklės: https://example.org/taisykles
 1:*server*     1 │07:43 --> testas prisijungė                                    │ Nick (3)
 2:#testas        │07:43 ── naujos žinutės ──                                     │@viesas
 3:#antras        │07:43 Tema: Rust, BSD ir IRC | taisyklės:                      │+botas
 4:>draugas    !1 │      https://example.org/taisykles                            │testas
                  │07:43 --> viesas prisijungė                                    │
                  │07:43 <botas> labas visiems!                                   │
                  │07:43 <viesas> ar kas bandė naują mircBSD versiją? Ilgos       │
                  │               eilutės dabar gražiai apvyniojamos su įtrauka   │
                  │               po nick'u.                                      │
                  │07:43 * botas geria kavą                                       │
                  │07:43 <botas> testas: tavo eilė pristatyti naujienas           │
                  │07:43 <testas> Sveiki! Viskas veikia:                          │
                  │               https://example.org/mircbsd                     │
                  │07:43 * testas mojuoja                                         │
──────────────────┴───────────────────────────────────────────────────────────────┴─────────────────
> /top
```

<sub>Tikras ekrano kadras (100×18), užfiksuotas E2E aplinka `scripts/e2e`.</sub>

## Savybės

- Vieno lango TUI: kanalų sąrašas su neperskaitytų / paminėjimų skaitikliais,
  tema, nick sąrašas, žinučių istorija su slinktimi ir paieška.
- Prisitaiko prie terminalo dydžio: siaurame lange (< 60 stulpelių) kanalų
  sąrašas siaurėja, nick sąrašas slepiamas, kai žinutėms liktų < 40 ląstelių.
- Teisingas plačių simbolių (CJK, emoji) matavimas; ilgos eilutės
  apvyniojamos su įtrauka po laiko žymos ir nick'o.
- mIRC spalvos ir formatavimas, nick'ų spalvos, `dark` / `light` tema,
  [`NO_COLOR`](https://no-color.org) palaikymas.
- TLS, SASL PLAIN / EXTERNAL, TLS atspaudo pinning (TOFU), automatinis
  jungimasis iš naujo, MONITOR (`/notify`).
- Žurnalai pagal dieną, paieška žurnaluose, JSON eksportas.
- Apsauga nuo terminalo valdymo sekų ir IRC komandų injekcijos (žr.
  [Saugumas](#saugumas)).

## Reikalavimai

- Debian 12/13 (trixie) arba FreeBSD 14+
- Rust 1.85 arba naujesnis: `rustc` ir `cargo` (jei sistemos paketai senesni, naudokite `rustup`)
- Tikrasis terminalas (TTY) – paleidžiant be TTY klientas nepasileidžia;
  mažiausias dydis 40×8

## Diegimas – Debian

```sh
sudo apt update
sudo apt install -y cargo rustc ca-certificates xdg-utils

cd mircbsd
cargo build --release

# Įdiegimas sau (be root teisių, rekomenduojama)
install -Dm755 target/release/mircbsd ~/.local/bin/mircbsd

# Arba visai sistemai
sudo install -m755 target/release/mircbsd /usr/local/bin/mircbsd
```

Pastabos:

- `ca-certificates` – reikalingas TLS prisijungimui (portas 6697)
- `xdg-utils` – komandai `/open` (atidaryti URL naršyklėje); jei nėra, ieškoma
  `gio`, `sensible-browser`, `x-www-browser`, `firefox`
- Pirmas `cargo build` atsiunčia priklausomybes iš crates.io
- Paleiskite **ne kaip root** – konfigūracija saugoma `$HOME`

## Diegimas – FreeBSD

```sh
cargo build --release
cp target/release/mircbsd /usr/local/bin/
```

## Greita pradžia

```sh
# Be konfigūracijos: serveris, portas, [nick], [tls]
mircbsd irc.libera.chat 6697 manick tls
mircbsd irc.data.lt 6667 manick

# Pagalba ir versija
mircbsd --help     # arba -h
mircbsd --version  # arba -V
```

Tada klientas viduje:

```text
/join #kanalas          įeiti į kanalą
labas!                  žinutė aktyviam kanalui
/help                   visos komandos ir klavišai
/quit                   išeiti
```

Paleidus be argumentų ir be konfigūracijos failo, `*server*` lange parodoma,
kur failą sukurti ir kaip prisijungti (`/connect …`).

## Konfigūracija

Konfigūracijos katalogas (Linux/FreeBSD XDG taisyklės):

- `$XDG_CONFIG_HOME/mircbsd` – jei `XDG_CONFIG_HOME` nustatytas
- `$HOME/.config/mircbsd` – jei ne (numatytas atvejis)

```sh
mkdir -p "${XDG_CONFIG_HOME:-$HOME/.config}/mircbsd"
cp config.example ~/.config/mircbsd/config
chmod 600 ~/.config/mircbsd/config   # jei jame slaptažodžiai
$EDITOR ~/.config/mircbsd/config
mircbsd                              # prisijungia pagal config
```

Formatas: `raktas=reikšmė`, `#` – komentaras. Visi raktai su paaiškinimais –
[`config.example`](config.example).

### Ryšys ir tapatybė

| Raktas | Numatytas | Aprašymas |
|--------|-----------|-----------|
| `server` / `port` / `tls` | – / `6667` / `false` | Serveris, portas, TLS |
| `nick` / `realname` | `mircbsd` / `mircBSD IRC Client` | Slapyvardis ir vardas |
| `sasl` | – | SASL PLAIN slaptažodis (vartotojas = `nick`) |
| `sasl_external` + `tls_cert` / `tls_key` | `false` | SASL EXTERNAL – autentifikacija kliento sertifikatu |
| `nickserv` | – | `IDENTIFY` slaptažodis (senasis būdas) |
| `password` | – | Serverio slaptažodis (`PASS`, bouncer'iams) |
| `tls_pinning` / `tls_pin` | `false` / – | TLS atspaudo pinning (TOFU) / rankinis atspaudas (sha256, SPKI) |
| `autojoin` | – | Kanalai po prisijungimo (`#a,#b`) |
| `perform` | – | Komandos po prisijungimo (`/msg X a; /join #b`) |
| `reconnect` | `true` | Jungtis iš naujo nutrūkus ryšiui |
| `ctcpversion` | `true` | Atsakyti į CTCP VERSION |

### Išvaizda ir elgsena

| Raktas | Numatytas | Aprašymas |
|--------|-----------|-----------|
| `theme` | `dark` | `light` – šviesiems fonams (tamsesnės nick'ų, mIRC ir akcentų spalvos) |
| `nick_colors` | `true` | Kiekvienas nick – sava spalva |
| `timestamps` / `ts_format` | `true` / `15:04` | Laiko žymos ir jų formatas (pvz. `15:04:05`) |
| `tscolor` | `gray` | Laiko žymos spalva: `gray`, `blue`, `green`, `cyan`, `none` |
| `highlight_color` | `red` | Paminėjimo fonas: `red`, `blue`, `green`, `magenta`, `cyan`, `yellow`, `none` (inversija) |
| `highlight_words` | – | Papildomi žodžiai paminėjimui (`linux,freebsd`) |
| `notifications` | `true` | Desktop pranešimai (`notify-send`) apie paminėjimus neaktyviuose languose |
| `mouse` | `true` | Pelė: ratukas slenka, paspaudimas kanalų sąraše perjungia langą |
| `maxmessages` | `5000` | Kiek žinučių laikyti atmintyje kiekvienam langui |
| `logdir` | `~/irclogs` | Žurnalų katalogas |

`/set` be argumentų parodo dabartines reikšmes. Keičiami gyvai (ir įrašomi
atgal į config failą) šie raktai: `theme`, `nick_colors`, `timestamps`,
`ts_format`, `tscolor`, `highlight_color`, `notifications`, `mouse`,
`maxmessages`, `reconnect`, `ctcpversion`. Ryšio ir slaptažodžių raktai per
`/set` nekeičiami. Bool reikšmės (ir config faile): `true`/`false`,
`on`/`off`, `yes`/`no`, `1`/`0`.

### Spalvos

- **Tamsus / šviesus fonas** – `theme=dark` (numatyta) arba `theme=light`.
- **Be spalvų** – nustatykite aplinkos kintamąjį `NO_COLOR` (bet kokia
  netuščia reikšmė): spalvos pašalinamos, paryškinimas lieka, o spalvoti
  fonai (statuso juosta, aktyvus langas, paminėjimai) rodomi inversija.

  ```sh
  NO_COLOR=1 mircbsd
  ```

## Ekranas

| Vieta | Kas rodoma |
|-------|-----------|
| Viršutinė juosta | `mircBSD \| nick [AWAY] @ serveris [TLS] \| aktyvus langas`; ryšio būsena `[JUNGIAMASI…]` / `[ATSIJUNGTA]` / `[NEPRISIJUNGTA]`; dešinėje – užuominos (siaurame lange trumpėja) |
| `✦` eilutė | Kanalo tema |
| Kairė | Langai su numeriais (`/win N`, `Alt+N`, `F1–F12`); `>` – privatus pokalbis; skaičius – neperskaitytos žinutės, `!N` – paminėjimai (raudonai) |
| Centras | Žinutės; `── naujos žinutės ──` žymi, kur baigėte skaityti; paslinkus – `↑ ISTORIJA` juosta |
| Dešinė | Nick sąrašas (`@` op, `%` halfop, `+` voice) su skaičiumi antraštėje |
| Apačia | Įvesties eilutė (`[AWAY]` žyma, kai pasitraukęs) |

Komandų klaidos ir `/help` rodomi lange, kuriame rašote; serverio pranešimai
ir paieškos rezultatai – `*server*` lange. Terminalo lango antraštėje
matomas aktyvus langas ir neperskaitytų žinučių kiekis.

## Komandos

Visos komandos taip pat matomos `/help` (skliaustuose – sinonimai).

### Ryšys

| Komanda | Aprašymas |
|---------|-----------|
| `/connect <serveris> <portas> [nick] [tls]` | Prisijungti prie serverio (`/server`) |
| `/reconnect` | Jungtis iš naujo |
| `/nick <vardas>` | Keisti slapyvardį (be argumento – parodo dabartinį) |
| `/away [priežastis]` | Pažymėti, kad pasitraukęs |
| `/back` | Grįžti iš away |
| `/quit [žinutė]` | Išeiti (`/exit`, `/q`) |

### Kanalai ir langai

| Komanda | Aprašymas |
|---------|-----------|
| `/join #kanalas [raktas]` | Įeiti į kanalą (`/j`) |
| `/part [priežastis]` | Išeiti iš kanalo (`/leave`) |
| `/cycle [priežastis]` | Išeiti ir vėl įeiti |
| `/close [langas]` | Uždaryti langą (+ PART; `/wc`) |
| `/window [n]` | Langų sąrašas / perjungti į n-tąjį (`/win`) |
| `/topic [tema]` | Rodyti / keisti temą |
| `/names` | Atnaujinti nick sąrašą |
| `/list [filtras]` | Serverio kanalų sąrašas |
| `/invite <nick> [#kanalas]` | Pakviesti į kanalą |

### Žinutės

| Komanda | Aprašymas |
|---------|-----------|
| `/msg <nick\|#kanalas> <tekstas>` | Žinutė (`/m`) |
| `/query <nick>` | Atidaryti privatų langą |
| `/me <veiksmas>` | Veiksmas (`* nick bėga`) |
| `/notice <nick> <tekstas>` | NOTICE žinutė |
| `/clear` | Išvalyti lango istoriją ekrane |

### Vartotojai ir moderavimas

| Komanda | Aprašymas |
|---------|-----------|
| `/whois <nick>` | Informacija apie vartotoją (`/wi`) |
| `/w [nick\|#kanalas]` | WHO užklausa (atsakymas – užklaustame kanale) |
| `/ignore <nick\|maskė>` | Ignoruoti (`nick`, `nick!user@host`, `*!*@host`) |
| `/unignore <nick\|maskė>` | Atšaukti ignoravimą |
| `/ignores` | Ignoruojamų sąrašas |
| `/notify add\|del\|list [nick]` | MONITOR: pranešimai, kai nick'as prisijungia / atsijungia |
| `/kick <nick> [priežastis]` | Išmesti iš kanalo (`/k`) |
| `/op`, `/deop`, `/voice`, `/devoice <nick>` | Suteikti / atimti teises |
| `/mode [taikinys] <+/-režimai> [arg]` | Nustatyti režimus |

### Paieška, URL ir žurnalai

| Komanda | Aprašymas |
|---------|-----------|
| `/search [žodis]` | Paieška aktyviame lange (arba `Ctrl+R`); tuščioje eilutėje `n` / `p` – kitas / ankstesnis rezultatas |
| `/search all <žodis>` | Paieška visuose languose (rezultatai `*server*`) |
| `/logsearch [kanalas] <žodis>` | Paieška žurnalo failuose (iki 500 atitikmenų) |
| `/urls` | URL istorija |
| `/open [n]` | Atidaryti URL naršyklėje (tik `http`, `https`, `ftp`) |
| `/exportjson [kanalas]` | Eksportuoti lango žinutes į JSON (`~/irclogs/<serveris>/`) |
| `/clearlogs [kanalas]` | Ištrinti žurnalus (numatytai – visus šio serverio) |

### Nustatymai ir kita

| Komanda | Aprašymas |
|---------|-----------|
| `/set [raktas] [reikšmė]` | Nustatymų sąrašas / keitimas (įrašoma į config) |
| `/keys [debug]` | Klavišų perrašymai iš `keys` failo; `debug` – rodo gaunamas klavišų sekas (`q` – išjungti) |
| `/time` | Serverio laikas |
| `/raw <IRC eilutė>` | Siųsti eilutę tiesiai serveriui |
| `/help` | Komandų ir klavišų sąrašas |

## Klavišai

| Klavišai | Veiksmas |
|----------|----------|
| `PgUp` / `PgDn` | Slinkti žinutes |
| `Shift+PgDn` | Grįžti į apačią (prie naujų žinučių) |
| `↑` / `↓` | Komandų istorija (visada; žinutės slenkamos PgUp/PgDn arba pelės ratuku) |
| `Ctrl+N` / `Ctrl+P` | Kitas / ankstesnis langas |
| `Alt+1`…`Alt+9`, `F1`…`F12` | Langas pagal numerį |
| `Tab` | Užbaigti nick'ą, komandą, kanalą (kartojant – kitas variantas) |
| `Ctrl+R` | Paieška (`Enter` – ieškoti, kartojant – toliau atgal; `Esc` – uždaryti) |
| `n` / `p` | Po paieškos, kai įvesties eilutė tuščia – kitas / ankstesnis rezultatas |
| `Ctrl+←` / `Ctrl+→` | Per žodį kairėn / dešinėn |
| `Ctrl+A` / `Ctrl+E`, `Home` / `End` | Eilutės pradžia / pabaiga |
| `Ctrl+W` | Ištrinti žodį prieš kursorių |
| `Ctrl+U` / `Ctrl+K` | Ištrinti visą eilutę / nuo kursoriaus iki galo |
| `Ctrl+↑` / `Ctrl+↓` | Slinkti nick sąrašą |
| `Ctrl+L` | Perpiešti ekraną |
| **Pelė** | Ratukas – slinkti; paspaudimas kanalų sąraše – perjungti langą |

Įklijavus kelias eilutes, klientas paklausia patvirtinimo: `Enter` – siųsti,
`Esc` – atšaukti.

### Klavišų perrašymas (`keys` failas)

`$XDG_CONFIG_HOME/mircbsd/keys`, eilutės formatu `veiksmas = seka`
(`\e` – Esc, `\xNN` – baitas, `\t`, `\r`, `\n`, `\\`). Pvz.:

```text
search = \eS
chan-next = \e[1;3C
```

Veiksmai: `chan-next`, `chan-prev`, `complete`, `hist-up`, `hist-down`,
`msg-up`, `msg-down`, `msg-bottom`, `nick-up`, `nick-down`, `search`,
`word-left`, `word-right`, `word-del`. Klavišo siunčiamą seką parodo
`/keys debug`.

## Failų vietos

| Failas | Paskirtis |
|--------|---------|
| `$XDG_CONFIG_HOME/mircbsd/config` (numatytai `~/.config/mircbsd/config`) | Konfigūracija |
| `$XDG_CONFIG_HOME/mircbsd/history` | Komandų istorija |
| `$XDG_CONFIG_HOME/mircbsd/ignore` | Ignoruojamų sąrašas |
| `$XDG_CONFIG_HOME/mircbsd/notify` | MONITOR stebimų nick'ų sąrašas (/notify) |
| `$XDG_CONFIG_HOME/mircbsd/keys` | Klavišų perrašymai (`veiksmas = seka`) |
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
  `/msg nickserv IDENTIFY pass`), SASL `AUTHENTICATE` bei `/join` su kanalo
  raktu nėra saugomos `history` faile. Atpažintos „NickServ“ slaptažodžių
  komandos paslepiamos žurnale ir JSON eksporte. Slaptų duomenų atpažinimas
  remiasi komandų forma, todėl kitų servisų ar neįprastų komandų atveju jo
  nereikėtų laikyti garantuotu.
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
  Įjungus pinning, pasitikėjimas grindžiamas atspaudu, o įprasta sertifikatų
  grandinės ir serverio vardo patikra neatliekama. TOFU pirmojo jungimosi metu
  neapsaugo nuo MITM: saugesniam pirmam prisijungimui nustatykite iš patikimo
  šaltinio gautą `tls_pin`. Nepavykus perskaityti ar išsaugoti atspaudų,
  ryšys nutraukiamas.
- **`/set`** perrašo tik nešlamčius raktus (`timestamps`, `theme` ir kt.) –
  nesaugios reikšmės (`sasl`, `password`, `server`, `tls`) per /set
  nekeičiamos.
- **Naudotojo nick/kanalų pavadinimai** tikrinami pagal RFC simbolių rinkinį,
  todėl valdymo simboliai negali patekti net į serverio komandas.
- `/open` atidaro tik `http://`, `https://`, `ftp://` prasidedančius URL.

Pranešti apie spragą – žr. [SECURITY.md](SECURITY.md).

## Kūrimas (FreeBSD/Linux)

```sh
cargo fmt --all -- --check
cargo clippy --locked --all-targets -- -D warnings
cargo test --locked
bash scripts/e2e/start.sh  # terminalo ir bandomojo IRC serverio scenarijai
cargo build --locked --release
cargo audit               # reikia atskirai įdiegto cargo-audit
```

CI atlieka Linux ir FreeBSD patikras, tikrina minimalią Rust versiją ir kas
savaitę paleidžia priklausomybių saugumo auditą. E2E patikrai reikia Python 3,
PTY ir leidimo naudoti vietinį tinklą. Daugiau – [CONTRIBUTING.md](CONTRIBUTING.md).

Pagrindiniai kodo moduliai: `client` valdo ryšį ir būseną, `proto` apdoroja
IRC protokolą, `input` – komandas ir klavišus, `ui` – atvaizdavimą, o
`persist` – failų įrašymą. Prie didesnių modulių testai laikomi atskiruose
`*_tests.rs` failuose.

## Versija

v0.4.8 – naujos funkcijos (žr. CHANGELOG.md): SASL EXTERNAL, paieškos
navigacija (`n`/`p` + /search all + /logsearch), `/set` su įrašymu į config,
`/exportjson` (JSON eksportas), tema (dark/light), MONITOR + `/notify`,
klavišų perrašymai (`keys` failas), TLS atspaudo pinning (TOFU).
