# mircBSD E2E testavimo aplinka (v0.4.8+)

Įranga: pty.fork mircBSD (120×40, TERM=xterm-256color) + fake IRC serveris
(TCP 127.0.0.1) + ANSI ekrano emuliatorius. Patikros tikrina GALUTINĄ
ekrano kadrą, ne žaliavą; kur svarbu spalvos — d.raw_since().

## Paleidimas

    bash scripts/e2e/start.sh

Arba rankiniu būdu:

    cargo build --release
    mkdir -p /tmp/mircbsd-e2e/bin
    cp target/release/mircbsd /tmp/mircbsd-e2e/bin/mircbsd
    MIRCBSD_E2E_DIR=/tmp/mircbsd-e2e MIRCBSD_BIN=/tmp/mircbsd-e2e/bin/mircbsd \
      python3 scripts/e2e/check.py

`start.sh` kiekvienam paleidimui sukuria naują laikiną katalogą ir išveda jo
vietą. Artefaktai (home/, drive.log) lieka jame. Tikras `~/.config/mircbsd`
neliečiamas (HOME ir XDG_CONFIG_HOME nukreipti į testavimo home).

## Failai (skriptai šiame kataloge)

- `ansi.py` — ekrano emuliatorius (CUP/ED0/ED1/ED2/EL/ECH/OSC praleidimas,
  apvyniojimas, plotis pagal East Asian Width, nebaigtos escape sekos
  buferizuojamos tarp read(), `resize`).
- `server.py` — fake IRC serveris: CAP LS→REQ→ACK(visos cap'os)→END→
  001/005 (CHANTYPES, PREFIX, CHANMODES=beI,k,l,imnpst, CASEMAPPING=rfc1459,
  MONITOR=60, NICKLEN=30)→MOTD; JOIN echo + 353/366 (ir kelių kanalų `JOIN #a,#b`); **PONG** į kliento
  periodinį PING (BE JO klientas po timeout'o laiko jungtį mirusia!);
  MONITOR +/−/CLEAR įrašymas.
- `driver.py` — pty fork, ekrano tiektuvas + žaliavos buferis
  (raw_since žymėse), `wait_until`.
- `check.py` — 30 patikrų: prisijungimas (001+MOTD), keys įspėjimas,
  PRIVMSG, IRC spalvų sanavimas, terminalo injekcija, apvyniojimas,
  PgUp/Shift+PgDn slinktis, n/p tuščiam inputui, switchCh paieškos būsenos
  išvalymas, /search all (rezultatai *server*), /logsearch (kūno atitiktis +
  timestamp'e neatitinka), /set (sąrašas, neteisinga bool be įrašo į config,
  ts_format write-back), **tema light — config + paletės persidažymas**
  (invalidateRenderCaches: 38;5;* SGR atsiranda žaliavoje), /notify
  (add→MONITOR +, 730 fold'ai, nevalidus nick), /exportjson (validus JSON),
  /keys remap (search → ESC+S), Ctrl+L perbraižymas; **25** platūs
  simboliai/ZWJ/tab neperpildo eilučių (tekstas neatsiduria kanalų juostoje
  ar už nick skirtuko), **26** KICK savęs, **27** SIGWINCH (100×30 →
  nick skirtukas pasislenka 20 stulpelių), **28** serverio kritimas →
  automatinis prisijungimas, registracijoje 433 → `testas_`, autojoin ir
  žinutė nauja jungtimi, **29** kito join — blanki pilka (90, ne 32),
  **30** `/set show_joins smart`: tylaus nick'o join paslepiamas (ir
  istorijoje), kalbėjusio part rodomas, fono lango neperskaitytų skaičius
  nepasikeičia, eilutė vis tiek žurnale, `all` grąžina.
  Nepavykus — kadras `fail-<nr>.txt`.
- `ansi_selftest.py` — emuliatoriaus savitikra (paleidžiama pirma):
  escape seka, perskelta per dvi pty read() dalis bet kurioje vietoje, duoda
  tą patį kadrą; CJK/emoji = 2 ląstelės, derinamieji = 0.
- `start.sh` — kompiliuoja ir paleidžia.

## Svarbios E2E pamokos (ne kartą patikrintos)

- **Pradinis aktyvusis kanalas = PASKUTINIS join'as** (#antras), o
  `\x0e` iš jo grįžta į *server* — patikrose naviguok `/win N` (1-bazis)
  ir patikrink statuso juostą (`| #testas `), kitaip klavišai keliauja į
  ne tą langą ir patikros klysta.
- Po paieškų slinktis lieka prie senosios pozicijos — nauja žinutė
  atkeliauja ŽEMIAU matymo lauko (teisinga TUI elgsena). Prieš „įrašė“
  patikras paspausk `\x1b[6;2~`.
- E2E rado ir pataisė bug'ą, kurį vienetiniai testai negalėjo gauti:
  /set theme|nick_colors neinvalidavo renderio cache'o (senos žinutės
  nešiojo senos paletės SGR — commit 166f296).

Paleidimo kodas: 0 (visos praėjo) / 1 (bent viena NEPRAĖJO — spausdina išsamų
note + `theme_raw.bin` dump). Žaliava palaikant: `/tmp/mircbsd-e2e/drive.log`.
