# CHANGELOG — mircBSD

Visi reikšmingi projektų pakeitimai dokumentuojami čia.
Formatas pagal [Keep a Changelog](https://keepachangelog.com/); versioning —
pagal [SemVer](https://semver.org/).

## [0.4.3] – 2026-10-20 – protokolo, renderio ir našumo pataisymai

Antroji peržiūra (po 23 saugumo radinių) aptiko dar 24 radinius —
daugiausia protokolo nesuderinamumai, ekrano pločio klainos ir našumo
trūkumai. Visi ištaisyti, kiekvienas — su regresijos testu („blogas
atmetamas“ **ir** „teisingas priimamas“).

### Taisyta (protokolas)

- **`353` nebekuria kanalų** – serverio komanda (RPL_NAMREPLY) anksčiau
  galėjo sukurti šimtais naujų langų (užpildydamas šoninę juostą).
  Dabar naudojama tik esamo kanalo paieška. Toks pat elgesys kaip `332`.

- **CAP `NEW` / `LIST` / `DEL`** – anksčiau buvo tik `LS`/`ACK`/`NAK`.
  Serveris, kuris siūlo `sasl` (ar kitą cap’ą) po registracijos, dabar
  suderamas; `CAP LIST` atsako aktyvių cap’ų sąrašu; `CAP DEL` pašalina
  cap’ą.

- **`MODE` argumentų indeksai pagal `CHANMODES`** – režimai, kurie ima
  parametrą, buvo nustatyti kietai (`blkeI`), todėl serveris su kitomis
  `CHANMODES` praleido argumentus ir statuso prefiksas atiteko
  neteisingam nick’ui. Dabar `CHANMODES` (iš 005) yra skaitomas ir
  naudojamas.

- **`INVITE` rodo kanalą, o ne target nick’ą** – tikras formatas yra
  `INVITE <target-nick> :#kanalas`; anksčiau komandai buvo naudojami
  atvirkštiniai indeksai, todėl kanalo niekada nebuvo matoma.

- **WHO atsakymai suprantami** – `352`, `315`, `354`, `251`–`255`, `265`,
  `266`, `367`–`369` anksčiau krisdavo į `default:` ir buvo rodomi
  žaliai kaip „[352] …“. Dabar formatuojami (`/w` komanda juos siunčia).

- **WHO/WHOIS atsakymai grįžta į užklaustą kanalą** – anksčiau visi šie
  atsakymai eidavo į `*server*` langą, todėl kanale, kuriame vartotojas
  užklausė, jų nebuvo matoma (reikėjo peršokti į serverio langą).
  Dabar atsakymai rodomi ten, kur užklausta.

- **Operatoriaus prefiksas neišnyksta** – kai `353` (NAMES) atnešdavo
  nick’ą su statuso prefiksu (`@nick`), o jis jau buvo sąraše be prefikso
  (pridėtas per `JOIN`), prefiksas tyliai dingdavo — vartotojas nematė,
  kad jis operatorius. Dabar prefiksas atnaujinamas.

- **Žurnale matomas nick’o pakeitimas** – `MsgNick` neturėjo žurnalo atvejo,
  todėl rašydavo tik naują nick’ą (`Žilvinas2`) — nebuvo aišku, kas į ką
  pasikeitė. Dabar: `*** Žilvinas → Žilvinas2`. Taip pat `MsgNotice` rašomas
  su prefiksu (`-nick-`).

- **Serverio slaptažodis (`PASS`)** – naujas konfigūracijos parametras
  `password=`. Siunčiamas prieš `NICK`, kaip reikalauja RFC 2812 (bouncer,
  privatus serveris).

- **ISUPPORT ribos naudojamos** – `NICKLEN`, `CHANNELLEN`, `TOPICLEN`,
  `KICKLEN` ir `MODES` anksčiau buvo apleisti (serverio ribos ignoruotos).
  Dabar `/nick`, `/join`, `/topic` ir `/kick` patikrina serverio ribą ir
  aiškiai pasako, kiek simbolių leidžiama. Taip pat skaitomas `NETWORK`.

- **`306` nustato away būseną** – po `RPL_NOWAWAY` statuso juosta rodo
  `[AWAY]`.

- **Nick’o pakeitimas rodomas kanaluose** – kaip mIRC. Anksčiau
  pranešimas buvo tik serverio lange.

### Taisyta (elgsena)

- **Lietuviškas nick’as** – `isValidNick()` skaičiavo ilgį BAITAIS (30
  baitų riba atmetė 15 lietuviškų raidžių) ir leido tik ASCII simbolius.
  Dabar riba yra 30 simbolių ir leidžiamos lietuviškos raidės. Nick’as
  validuojamas visuose keliuose: CLI argumentai, konfigūracijos failas,
  `/connect`, `/nick`.

- **IPv6 serveriai** – `isValidServer()` atmetdavo „::1“ (kolonas nebuvo
  leistinas simbolis). Dabar palaikomi IPv6 adresai, jungiami kaip
  `[::1]:6667`.

- **`/w` siunčia WHO** – `/w` paviršius buvo užimtas langų sąrašui
  (`/window`). Dabar `/w` užklausa serverio, `/window` rodo langus.

- **Kanalų sąrašas slankosi** – anksčiau rodydavome tik pirmus 12
  kanalų, likusieji (iki 100) buvo nepasiekiami. Dabar sąrašas slinkosi
  (`Ctrl+Home` / `Ctrl+End`), rodo rodykles ▲▼, o peršokimas į
  nematomą kanalą jį paslinka.

- **Kanalų validacija pagal serverio `CHANTYPES`** – `isValidChannel()`
  kietai kodino `#&!+`, todėl serveris su `CHANTYPES=#$` atmetdavo
  `$kanalas`.

### Taisyta (stabilumas)

- **Panikos atkūrimas** – `recover()` buvo tik testuose. Panika
  `parseIRC`/`readKeys` metu palikdavo terminalą raw režimu (shell
  neveikdavo). Dabar kiekviena gorutina atkuria terminalą ir sustabdo
  klientą.

- **Senos jungties išardymas negriebia naujos** – po `/connect` sena
  jungties teardown-as nulauždavo naujos būseną (`connected=false`,
  `conn=nil` — fd nutekėjimas). Dabar tikrinama `connectGen` epocha.

- **`AUTHENTICATE` nebeatlieka slaptažodžio** – serverio nepageidaujamas
  `AUTHENTICATE +` (be mūsų SASL užklausos) dabar negauna mūsų
  slaptažodžio.

- **Duomenų lenktynės** – `doTab` skaitė nick’ų sąrašą be rakino (ryškiausia
  race visame kode); `isIgnored` duodavo tarpinį (dubliuotą) sąrašą.

- **Žurnalų eilių tvarka** – kiekvienai žinutei buvo paleidžiama
  atskira gorutina, todėl 20 lygiagrečių žinučių rašydavo failą beveik
  atsitiktine tvarka. Dabar žurnalą rašo VIENA gorutina per eilę.

### Taisyta (ekrano plotis)

- **Plotis matuojamas ląstelėmis, ne rune’ais** – CJK tekstas (2 ląstelės
  simbolis) ir emoji perlipo liniją. Įvestas pločio modulis (`width.go`)
  su grapheme clusteriais (ZWJ emoji, vėliavos, odų modifikatoriai) ir
  dvejetaine paieška intervalųose.

- **IRC spalvos išgyvena apvyniuotose eilutėse** – `findChunk()` ieškojo
  PLAIN tekste ir grąžindavo be formatavimo kodų, todėl spalva likdavo
  tik 1 eilutės žinutėse.

- **URL apvyniojamas vieną kartą** – `highlightURLs()` lygino kodus jau
  paruoštame tekste: pirmas URL gaudavo dvigubus kodus, antras —
  nukirpamas. Dabar URL aptinkamas ANSI-aware skeneriu.

- **Tab-baigimas neperpja UTF-8** – `wordBefore()` grąžindavo baitų slice,
  o ilgis skaičiuojamas rune’ais → nutrūkusi seka („žinutė kai“ →
  `žinut\xc4`).

### Našumas

- **Inkrementalus renderio cache’as** – nauja žinutė anksčiau
  nugriaždindavo VISĄ kanalo cache’ą (5000 žinučių → 5000
  `renderLines()` kiekviename braižyme). Dabar senos eilutės lieka
  galiojančios, renderiuojamos tik naujos.
  **Išmatuota: 28.8 ms → 1.76 ms (16×) 5000 žinučių kanale.**

- **Nick’ų įstatymas į surūšiuotą sąrašą** – `addNick()` kiekvieną kartą
  perrankydavo visą sąrašą (O(n log n) įterpimui). Dabar dichotominė
  paieška (O(log n) paieška).

- **`sleepReconnect`** kiekvieną 50 ms kurdavo naują `time.After` — dabar
  vienas `time.Ticker`.

- **Ping siunčiamas į savo kanalą** – senas ping gorutina rašydavo į
  bendrą `writeCh`, kuris po `/connect` jau rodo į naują jungtį →
  „Ping timeout“ klaida nors serveris atsakydavo.

### Sutvarkyta

- Pašalinti neveikiantys laukai (`pingTicker`, `searchResult`).
- Kanalų prefiksai (F1–F12) rodomi pagal realų indeksą, ne visada
  pirmuosius 12.

## [0.4.2] – 2026-10-20 – saugumo ir stabilumo pataisymai

Peržiūra aptiko 23 klaidą — dauguma jų galėjo sukelti paniką arba
kompromituoti terminalą. Visos ištaisytos; saugumo regresijos testai
įtraukti į CI (`audit_findings_test.go`, build tag `audit`).

### Taisyta (elgsena)

- **Kanalų validacija nebeatmeta teisingų kanalų** – saugumo pataisymai
  įvedė `isValidChannel()` validatoriu, bet jis neleido kanalių prefikso
  („#“, „&“, „!“, „+“), todėl `/join #kanalas` niekada nepasiekdavo
  serverio. Prefiksas leidžiamas tik pirmoje vietoje; prefiksas be
  vardo („#“ vienas) atmetamas.

- `/clearlogs` be argumentų dabar ištrina **visus** dabartinio serverio
  žurnalus (pagal `/help` tekstą) — anksčiau trynė tik aktyvų kanalą.

- `/close` dabar priima argumentą: `/close <kanalas>` uždaro nurodytą
  langą (anksčiau argumentas buvo ignoruojamas, užverdavo aktyvų).

### Taisyta (stabilumas)

- **Serverio `PART *server*` nebeišprovokuoja panikos** – kai serveris
  pranešdavo apie mūsų nick'o išeinimą iš `*server*` lango,
  `activeIdx` likdavo `-1`, o `draw()`, `addMsg()`, `switchCh()` ir AWAY
  tvarkymas panikuodavo. Dabar visi indeksai į kanalų sąrašą saugomi
  per `activeChan()`, o `*server*` langas negali būti pašalintas.

- **Aktyvus kanalas nebeslypčioja** – pašalinus kanalą *prieš* aktyvų,
  indeksas neperskaičiuojamas, ir vartotojas rašė į kitą kanalą.
  Dabar `removeCh()` perskaičiuoja `activeIdx` visais atvejais.

- **`/mode` su tik tarpais nebeišprovokuoja panikos** – klaida buvo
  pasiekiama per konfigūracijos `perform=` parametrą, t.y. be naudotojo
  įspėjimo.

- **Neribotas eilučių skaitymas nuo serverio** – priešiškas serveris
  galėjo siųsti megabaitų eilutę be `\n` ir kaupti ją atmintyje. Dabar
  naudojamas `bufio.ReadSlice` su apribojimu; per ilgos eilutės
  atmestos su įspėjimu.

- **Duomenų lenktynės (data race)** – `c.connected`, `c.nick` ir
  `ignoreList` buvo skaitomi be rakino iš kelių gorutinių. Dabar
  naudojami saugūs prieigos metodai.

- **ISUPPORT nebe globalus** – vieno serverio nustatymai (CHANTYPES,
  PREFIX, CASEMAPPING) gadino kito serverio nustatymus. Dabar jie
  saugomi kiekviename kliente atskirai.

- **Kanalų sąrašas ribojamas** – JOIN/PM flood negali sukurti šimtų
  kanalų (limitas 100).

- **Serverio komanda negali sukurti kanalų** – `332`/`353` be kanalo
  parametro ir `PART` neįjungtame kanale nebeiš naujų kanalų.

### Taisyta (saugumas)

- **Terminalo injekcija per privataus kanalo pavadinimą** – priešiškas
  serveris galėjo atsiųsti `PRIVMSG` su ESC simboliais nick'e; pavadinimas
  buvo braižomas į ekraną nesanitizuotas. Dabar kanalo pavadinimas
  sanitizuojamas jį sukuriant.

- **Žurnalų katalogų traversas** – serverio pavadinimas „..“ (ar
  panašus) galėjo parašyti žurnalus už `logDir` ribų. Dabar „..“
  blokuojamas tiek `sanitizePath()`, tiek patvirtinant serverio vardą.

- **Komandų injekcija** – serverio, kanalo ir gavėjo pavadinimai
  įrašomi į IRC komandas. Dabar jie tikrinami: be tarpų, be CRLF, be
  valdymo simbolių.

- **NickServ slaptažodžiai nebesklando į žurnalus** – `IDENTIFY`,
  `REGISTER`, `CONFIRM` ir `SET PASSWORD` slaptažodžiai žurnale
  pakeičiami žyma „[slaptažodis paslėptas]“.

- **CTCP PING atspauda ribota** – serveris negali priversti mūsų
  atspindėti šimtų KB (64 baitų limitas).

- **Visos siunčiamos eilutės ≤ 512 baitų** – ilgos `/notice`, `/topic`
  ir `/away` komandos nutrumpinamos UTF-8 saugiai (anksčiau serveris
  nutraukdavo ryšį).

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