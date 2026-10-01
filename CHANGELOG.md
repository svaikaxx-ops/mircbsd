# CHANGELOG — mircBSD

Visi reikšmingi projektų pakeitimai dokumentuojami čia.
Formatas pagal [Keep a Changelog](https://keepachangelog.com/); versioning —
pagal [SemVer](https://semver.org/).

## [0.4.1] – 2026-10-01 – slinkimo (scroll) taisymai

### Taisyta

- **Slinktis žemyn nebe pralaimi naujoms žinutėms** – būnant atslinkus
  aukštyn užimtame kanale naudotojas negalėdavo grįžti prie naujų
  žinučių: kiekviena atėjusi žinutė padidindavo `scrollOff` tiek pat,
  kiek vienas PgDn spaudimas sumažindavo (plūstant žinutėms slinktis
  žemyn nešaudavo su istorijos užšaldymu ir pralaimėdavo). Dabar slenkant
  žemyn (PgDn, ↓, ratukas) užšaldymas 1,5 s praleidžiamas, o apačioje
  esantis antgalis rodo, kiek eilučių lieka žemiau:
  `↑ SCROLL (+N eilučių žemiau)`.

- **`/clear` nebeužblokuoja ekrano** – išvalius kanalą esant atslinktam
  aukštyn vaizdui, sesijos slinktis atstatydavo iš naujo ir langas
  likdavo tuščias neribotam laikui (naujos žinutės tik dar labiau
  nutoldavo slinktį nuo apačios). Dabar `/clear` slinktį atstato į
  apačią.

- **`Ctrl+R` paieškos pozicionavimas eilutėmis, ne žinutėmis** – rasta
  žinutė, užimanti daugiau nei vieną eilutę, atsidūrdavo ne ten, kur
  turėtų (scrollOff buvo matuojamas žinutėmis, o braižymas vaikšto
  eilutėmis). Dabar rastos žinutės paskutinė eilutė atsistoja matomo
  lango apačioje.

### Nauja

- `Shift+PgDn` – šuolis apačion iš istorijos (vienas spaudimas;
  dokumentuota README ir /help).

## [0.4.0] – 2026-09-29 – patogumo funkcijos

### Nauja

- **Žurnalo rotacija per dieną** – žurnalas ženkinamas
  `<serveris>/<kanalas>-YYYY-MM-DD.log`; nauja diena → naujas failas, senojo
  failo rankena uždaroma iš karto.
- **Desktop pranešimai** – highlight neaktyviame kanale siunčia
  `notify-send` pranešimą (`notifications=true` numatytai; ≤1 per 3 s,
  tekstas sanitarizuotas ir apribotas).
- **Geresnis 433 atsarginis kelias** – užima nick'ą paeiliui `nick_`,
  `nick2`, `nick3` (nebegali nick'as augti ilgdalžiu `nick_ _ _ _`),
  po 3 bandymų pasiūlo `/nick` rankiniu būdu; skaitliukas atstatomas po
  sėkmingo prisijungimo (001).
- **`/connect` ir `/reconnect` nedelsiant nutraukia seną ryšį** – nauja
  jungtis netrukusi atsakei atsakymui: visos senos goroujūns (jungimo
  ciklas, backoff miegas, užsienio jungtys) pasitraukia per
  epochos skaitliukus `connectGen`/`connectingGen`.
- **Pelės palaikymas (SGR 1006)** – ratukas scrollina žinutes, atleistas
  paspaudimas ant kanalo juostos perjungia kanalą (`mouse=true`
  numatytai, išjungiama `mouse=false`).
- **Konfigūruojamos spalvos** – `highlight_color` (`red` numatyta, `blue`,
  `green`, `magenta`, `cyan`, `yellow`, `none` = reverse video) ir
  `nick_colors` (išjungia nick'ų spalvinimą).
- **CI workflow** (.github/workflows/ci.yml): `go vet`, test'ai su race
  detectoriumi, fuzz smoke testai, kryžminis GNU/Linux ir FreeBSD amd64
  paruošimas.

### Taisyta

- `/connect` prie kito serverio nebelaukia senos jungties, atsako
  backoff'e arba „jau jungiamasi" klaidos — jungtis iš naujo galima
  bet kada.
- Log failo pavadinimas nebe `#kanalas.log` fiksuotas — po dienos
  rotacijos nieko nebeprarandama.

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