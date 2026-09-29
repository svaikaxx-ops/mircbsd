# CHANGELOG — mircBSD

Visi reikšmingi projektų pakeitimai dokumentuojami čia.
Formatas pagal [Keep a Changelog](https://keepachangelog.com/); versioning —
pagal [SemVer](https://semver.org/).

## [0.3.1] – 2026-09-29 – saugumo patikra

### Saugumas

- **Terminalo valdymo simbolių (ANSI/OSC) injekcijos apsauga** – visos tinklo
  žinutės prieš rodymą valomos nuo ESC/C1/C0/DEL kodų (`isControlRune`,
  `sanitizeText`): nebegalimas ekrano trynimas (`\x1b[2J`), terminalo antraštės
  arba iškarpinės (OSC 52) injekcija tinkle žinutėmis. Taip pat žurnaluose
  (log failuose).
- **NickServ slaptažodžių apsaugo įvesties istorijoje** – eilutės su
  `IDENTIFY` (pvz. `/msg NickServ IDENTIFY pass`) nesaugomos `history` faile.
- **Griežtesnė `isValidNick` validacija** – RFC nick simbolių rinkinys; ESC ir
  valdymo simboliai nebepraeina į `NICK`/`USER` komandas.
- **CAP 302 tęsinių derybos** – `CAP * LS *` sąrašas kaupiamas iki paskutinės
  eilutės; `CAP REQ`/`CAP END` nebeieinama pirma laiko (ankščiau sulūždavo
  SASL derybos serveriuose su ilgu CAP sąrašu).
- **SASL atsarginis kelias** – jei serveris nedera apie SASL arba SASL nepavyko
  (902/904/905), SASL slaptažodžio galima per NickServ `IDENTIFY`.
- **README: naujas „Saugumas" skyrius** (config `chmod 600`, TLS, `/open`
  URL apribojimai).
- **Naujos regresijos testai**: terminalo injekcija, CAP daugiaeilis LS,
  istorijos slaptažodžiai, nick limitas po 433.

### Nauja

- Žinučių renderio kešavimas (`Channel.renderGen`) – nebeperrenkamos iki 5000
  žinučių kiekvienam klavišo paspaudimui.
- Papildomas `firstField()` pagalbininkas visur, kur anksčiau buvo
  `strings.Fields(input)[0]`.

### Taisyta

- **Data-races**: `send()`/`writeCh`, TLS `ServerName`, `/names`
  (`nicks = nil` už rakto), `/w` komandos ciklas.
- **Panic**: dvigubas `close(stopCh)` (SIGINT + `/quit`) → `sync.Once`;
  `strings.Fields(a)[0]` ant tuščio/tarpinio įvesties – 7 vietose.
- Nick palyginimai tapo didžiųjų/mažųjų raidžių nejautrumo (`strings.EqualFold`):
  JOIN/PART/NICK/echo-message.
- `/part` ir `/names` veikia ne tik `#`, bet ir `&` kanaluose.
- `433`: nick ilgis apribotas (nebegalimas begalinis `nick___...` prailginimas).
- `readKeys`: stdin nutrūkus klientas švariai išeina (nebėda „be klaviatūros“).
- `/clear`: žinučių kešas iš naujo perskaičiuojamas po išvalymo.

## [0.3.0] – ankstesnė versija

- Pradinė terminalo vartotojo sąsaja (tmux/konsolė, mIRC spalvos, žymės).
- IRCv3 CAP/SASL pagrindinis palaikymas, TLS (MinVersion 1.2), automatinis
  iš naujo jungimas su eksponentiniu atsitraukimu.
- Komandos: `/connect`, `/join`, `/part`, `/msg`, `/me`, `/whois`, `/ignore`,
  `/list`, `/search`, `/open`, `/raw` ir kt.
- Žurnalų saugojimas (`~/irclogs/<serveris>/<kanalas>.log`), įvesties istorija,
  URL istorija ir atidarymas naršyklėje (`/urls`, `/open`).