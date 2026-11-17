# Saugumo politika — mircBSD

## Palaikomos versijos

Saugumo pataisymai leidžiami tik naujausiai išleistai versijai.

| Versija                  | Palaikoma |
| ------------------------ | --------- |
| 0.5.x (naujausia)        | taip      |
| < 0.5.0 ir Go versijos   | ne        |

Prieš pranešdami patikrinkite, ar klaida kartojasi naujausioje versijoje
(`main` šakoje).

## Kaip pranešti apie pažeidžiamumą

**Nekurkite viešo „issue“** saugumo klaidoms.

Praneškite privačiai per GitHub **Private vulnerability reporting**:
repozitorijos skirtukas **Security → Advisories → Report a vulnerability**
(„Report a vulnerability“ mygtukas). Pranešimą matys tik palaikytojai.

Nurodykite:

- mircBSD versiją (`/help` antraštė arba CTCP VERSION) ir OS (Debian,
  FreeBSD…), terminalo emuliatorių;
- poveikį (kas gali ką padaryti: serveris, kitas kanalo naudotojas, vietinis
  naudotojas);
- žingsnius atkurti — pvz. serverio eilutes (raw IRC), konfigūracijos
  fragmentą **be tikrų slaptažodžių**;
- jei turite — pataisymo pasiūlymą arba testą.

Ką žadame:

- patvirtinsime gavimą per **7 dienas**;
- įvertinsime ir informuosime apie planą per **14 dienų**;
- pataisymą stengsimės išleisti per **90 dienų** (kritinius — kuo greičiau)
  ir paskelbsime GitHub Security Advisory; pranešėjas paminimas, jei nori.

Prašome neviešinti detalių, kol pataisymas neišleistas arba nepraėjo 90
dienų — kas įvyks anksčiau.

## Grėsmių modelis (santrauka)

### Patikimi

- Vietinis naudotojas, kuris paleidžia klientą, ir jo failai:
  `config`, `keys`, `fingerprints`, `history`, `ignore`, `notify`
  (`$XDG_CONFIG_HOME/mircbsd/`), log katalogas.
- `perform`, `autojoin`, `/raw` — vykdomi tiksliai taip, kaip parašė
  naudotojas.

### Nepatikimi

- **IRC serveris** (taip pat ir priešiškas ar perimtas) bei **kiti tinklo
  naudotojai**: visos serverio eilutės, nick'ai, kanalų pavadinimai, temos,
  CTCP, IRCv3 žymės, ISUPPORT/CAP reikšmės.
- **Tinklas** tarp kliento ir serverio (be TLS — viskas matoma ir keičiama).

### Ką klientas garantuoja prieš nepatikimą pusę

- **Terminalas**: tinklo tekstas niekada nepasiekia terminalo kaip valdymo
  seka — C0/C1/ESC/DEL (CSI, OSC 0 antraštė, OSC 52 iškarpinė, DCS) ir bidi
  perrašymo/izoliavimo simboliai (U+202A–202E, U+2066–2069) šalinami ekrane,
  antraštėje, Tab užbaigime, pranešimuose ir žurnaluose.
- **Protokolas**: siunčiamose eilutėse CR/LF/NUL šalinami, eilutė ≤ 512 baitų;
  kanalų/nick'ų pavadinimai su tarpais ar valdymo simboliais langų nekuria
  (komandų taikinio klastojimas).
- **Kredencialai**: PASS, SASL PLAIN, NickServ IDENTIFY ir `perform`
  siunčiami **tik konfigūracijoje nurodytam serveriui** (ne `/connect kitas`);
  į neprašytą `AUTHENTICATE +` slaptažodis neatiduodamas; be TLS rodomas
  įspėjimas. Slaptažodžių komandos neįrašomos į `history` (nei į ↑ atmintį,
  nei į failą), paslepiamos ekrano aide (taigi ir `/search`), žurnaluose ir
  JSON eksporte; serveriui siunčiamos nepakeistos (euristika — žr. README).
- **Failų sistema**: kelio komponentai iš tinklo sanitizuojami (`/`, `..`);
  failai kuriami `0600`, katalogai `0700`, rašymas atominis (laikinas failas
  + rename); žurnalo failas atidaromas su `O_NOFOLLOW`, katalogai turi
  priklausyti naudotojui.
- **Išoriniai procesai**: `/open` ir `notify-send` paleidžiami be apvalkalo
  (shell); `notify-send` argumentai atskirti `--`, URL, prasidedantys `-`
  ar turintys paslėptų simbolių, neatidaromi;
  pranešimo kūnas ekranuojamas (žymėjimas).
- **DoS**: eilutė ≤ 4096 baitų, ≤ 100 kanalų + 200 privačių langų,
  ≤ 5000 nick'ų kanale, žinučių skaičius ribojamas (`maxmessages`), ribos
  AWAY/MONITOR/CAP rinkiniams, CTCP atsakymai ribojami (4 per 10 s),
  siuntimas — 5 eilutės/s; panika apdorojant serverio eilutę sugaunama.
- **TLS**: rustls (TLS 1.2+, webpki šaknys). `tls_pinning=true` — TOFU pagal
  SPKI SHA-256; pakeitus raktą ryšys nutraukiamas prieš NICK/USER/PASS.

### Žinomi apribojimai (ne pažeidžiamumai)

- TOFU **neapsaugo pirmojo** prisijungimo: jei pirmas sertifikatas
  nepatvirtintas CA, rodomas įspėjimas, bet ryšys tęsiamas. Saugiausia —
  `tls_pin=` iš patikimo šaltinio.
- Priešiškas serveris mato viską, ką siunčiate jam, ir gali siųsti daug
  duomenų: atminties naudojimas ribojamas tik langų × `maxmessages`.
- Slaptažodžių atpažinimas istorijai/ekranui/žurnalams remiasi komandų forma
  ir gavėju (`*Serv` paslaugos) — neatpažinta forma rodoma kaip įvesta.
- Konfigūracijos failo teisių klientas nekeičia (tik įspėja, jei jis su
  slaptažodžiais skaitomas kitiems).

---

## English summary

**Supported versions:** only the latest release (currently 0.4.x, Rust).
Older and Go-based versions receive no fixes.

**Reporting:** please do **not** open a public issue. Use GitHub's private
vulnerability reporting: **Security → Advisories → Report a vulnerability**
in this repository. Include the version, OS/terminal, impact, and steps to
reproduce (raw IRC lines, config snippet without real secrets). We aim to
acknowledge within 7 days, assess within 14 days and ship a fix within 90
days, published as a GitHub Security Advisory.

**Threat model:** the local user and their config/key files are trusted; the
IRC server (possibly malicious), other network users and the network path are
not. Untrusted text must never reach the terminal as control sequences
(C0/C1/ESC, OSC 0/52, bidi overrides), never inject IRC commands (CR/LF/NUL),
never write outside the log directory or follow planted symlinks, and never
cause configured credentials to be sent to a server other than the configured
one. TLS uses rustls with webpki roots; optional TOFU SPKI pinning does not
protect the very first connection (a warning is shown when that certificate is
not CA-signed). Memory use against a hostile server is bounded by
per-structure limits (windows × `maxmessages`, nick lists, CAP/MONITOR sets).
