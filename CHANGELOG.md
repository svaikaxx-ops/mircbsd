# CHANGELOG — mircBSD

Visi reikšmingi projektų pakeitimai dokumentuojami čia.
Formatas pagal [Keep a Changelog](https://keepachangelog.com/); versioning —
pagal [SemVer](https://semver.org/).

## [0.4.8] – 2026-10-22 – naujos funkcijos (paketas „b“)

Likusios peržiūros pasiūlytų funkcijos (vienos per kiekvieną — su
regresijos testais `funkcijos_v0_4_8_test.go`).

### Nauja

- **SASL EXTERNAL** (`sasl_external=true` + `tls_cert`/`tls_key`) —
  autentifikacija kliento sertifikatu: TLS jungimui pridedamas sertifikatas;
  derybose renkamas mechanizmas EXTERNAL (kitu atveju PLAIN); `AUTHENTICATE`
  atsakymui siunčiamas tuščias „+“ (be slaptažodžio payload'o). Neteisingi
  sertifikato keliai — jungtis neatliekama (aiški klaida).
- **Paieškos navigacija** — `n` / `p` (su TUŠČIA įvestimi): kitas /
  ankstesnis rezultatas; `/search all <žodis>` — paieška VISUOSE languose
  (rezultatai `*server*` lange, iki 50); `/logsearch [kanalas] <žodis>` —
  paieška ĮRAŠO žurnalo failuose (`~/irclogs/<serveris>/`, iki 500
  atitikmenų, faile skaitoma iki 10 MB).
- **`/set <raktas> [reikšmė]`** — gyvas nustatymų keitimas: ekrane
  perrašo (mouse SGR sekas, nick'ų spalvas, laiko žymas ir kt.) ir
  ATOMINIŠKAI įrašo į config failą (esama eilutė perrašoma, likęs failas
  nekeičiamas, 0600, tmp+rename). Jautrūs raktai (`sasl=`, `password=`,
  `server`, `tls` ir kt.) — nekeičiami per /set (apibrėžta `settableKeys`).
- **`/exportjson [kanalas]`** — visos lango žinutės į JSON
  (`~/irclogs/<serveris>/<kanalas>-YYYY-MM-DD.json`): ts (RFC3339 UTC),
  nick, tipas (`Message.type.String()`), tekstas (SANITIZUOTAS — be IRC
  formatavimo ir terminalo valdiklių). Failas 0600, katalogas 0700; kelias
  per `sanitizePath` — katalogų kelio išėjimas neįmanomas.
- **Tema (`theme=dark|light`)** — šviesiame fone šviesios nick spalvos
  (91–96, 226 ir kt.) yra neskaitymos; light tema pakeičia paletę į tamsias
  xterm-256 spalvas. Keičiama gyvai per `/set theme light`.
- **MONITOR + `/notify add|del|list`** — stebimų nick'ų sąrašas
  (persistes: configDir/notify, 0600): po prisijungimo siunčiamas
  `MONITOR + nick1,nick2`; numerikai 730/731 rodo online/offline
  („● nick prisijungė (žiūrimas)“ / „○ nick atsijungė“), 732–734 sąrašą
  ir limito 734 (nepridėti nick'ai pašalinami iš sąrašo, kitaip jie
  liktų „stebimi“ nieko nepranešdami). ISUPPORT 005 `MONITOR=N` atpažintas.
- **Klavišų perrašymai (`configDir/keys`)** — eilutės
  `veiksmas = seka`; veiksmams: `chan-next`, `chan-prev`, `nick-up`,
  `nick-down`, `word-left`, `word-right`, `word-del`, `msg-up`,
  `msg-down`, `msg-bottom`, `search`, `hist-up`, `hist-down`,
  `complete`. Sekas rašomos `\e` (ESC), `\xHH`, `\n`, `\r`, `\t`, `\\` —
  kitaip literai (pvz. `search = \eS`). handleKey gautą seką perrašo
  kanonine (ciklas apsaugotas nuo 3 iteracijų); nevalidūs veiksmo vardai
  arba sekos — praleidžiami su įspėjimu. `/keys` — aktyvių perrašymų
  sąrašas ir failo per-skaitymas (gyvai).
- **TLS atspaudo pinning (TOFU)** (`tls_pinning=true` / `tls_pin=`) —
  serverio sertifikato SPKI sha256 atspaudas: pirmą kartą išsaugomas
  (`configDir/fingerprints`, eilutė `host:hex`, 0600), vėliau tikrinamas —
  nesutapimas nutraukia jungtį iki NICK/USER (identitas ir slaptažodis
  nepasiekia serverio). `tls_pin=` — rankinis atspaudas (pirmenybė
  prieš TOFU failą).

## [0.4.7] – 2026-10-21 – naujos kokybės funkcijos (paketas „a")

Septynios „mažos pastangos / didelė nauda" funkcijos (peržiūros pasiūlymų
top-sąrašas). Kiekviena — su regresijos testu (`funkcijos_v0_4_7_test.go`).

### Nauja

- **`/query <nick>`** — atidaro PRIVATŲ langą BE žinutės; anksčiau PM
  langas atsiverdavo tik /msg su žinute arba kai kitas parašydavo pirmas.
  Lango atsako: `>nick` šoniniame sąraše; pakartotas /query nedubliuoja.
- **„── naujos žinutės ──“ ribos žyma** — grįžus į kanalą su
  neperskaitytomis žinutėmis, prieš pirmą neskaitytą įstatoma žyma
  (pirmosios neperskaitytos žinutės indeksas `unreadFrom` fiksuojamas
  addMsg). Pakartotas perjungimas nedubliuoja; inkrementalus renderio
  cache‘as nuvalomas prievartniškai (žyma įsiskverbia Į VIDURĮ — galo
  cache‘as netinka).
- **Terminalo lango antraštė (OSC 0)** — `mircBSD — #kanalas (N)` su
  neperskaitytų žinučių suma kituose languose. Tekstas sanitizuotas
  ir kirptas (40 ląstelių) — OSC antraštė su valdymo simboliais taptų
  terminalo valdymu.
- **`timestamps` / `ts_format` config raktai** — laiko žymas galima
  IŠJUNGTI arba keisti formatą (pvz. `ts_format=15:04:05`); anksčiau —
  fiksuotas „15:04“.
- **Nauji IRCv3 cap'ai** — `extended-join` (JOIN rodys paskyrą),
  `account-notify` ACCOUNT pranešimai, `chghost` ident@host pakeitimai,
  `invite-notify` pakvietimai į bendrus kanalus. Bendras `ircv3WantedCaps`
  sąrašas (CAP LS + CAP NEW nesiskiria); ACCOUNT/CHGHOST rodomi „*server*“.
- **Paros skirtiklis** — „── 2026-10-21 ──“ eilutė tarp žinučių skirtingomis
  paromis (rašymo metu — nenarom žinutes laikyti dviem ar daugiau
  dienų, kad į žurnalą neiškeliautų dukart).
- **Kanalų vardų Tab užbaigimas** — `/join #li<Tab>` papildo iš ATIDARYTŲ
  langų pagal serverio CHANTYPES (anksčiau tik /komandos ir nick'ai).

## [0.4.6] – 2026-10-21 – pilnos atskitos peržiūros pataisos (8 radiniai)

Penktoji peržiūra (pilna atskito peržiūros po merge — skaityti visi ~6 800
eilučių iki galo). Visi ištaisyti, kiekvienas — su regresijos testu
(`perziura_2026_10_21d_test.go`). findChunk testui patikrinta, kad su SENU
kodų variantu jis tikrai sugautų klaidą („begalingai“ → „galingai š“ ir t. t.).

### Taisyta (kritinis)

- **`findChunk()` — baitų indeksas perduotas kaip rune indeksas** –
  `strings.Index` grąžina **baitų** indeksą, o `plainToOriginalBounds` jį
  lygina su **rune** skaitikliu (`plainSeen++` per raidę). Su ne-ASCII
  tekstu (lietuviškos raidės, emoji) apvyniotos žinutės (2+ eilutės)
  tęsinys rodydavo „nuslystus“ gabalus: „begalingai“ → „galingai š“,
  „šokinėja“ → „kinėja“. Dabar indeksas konvertuojamas
  (`utf8.RuneCountInString`).

### Taisyta (saugumo)

- **CAP ACK atsisakymo „-“ prefiksas NEBEAKTYVUOJA cap'o** – anksčiau
  `TrimLeft(cap, "-")` tiesiog nukerpdavo minusą ir `CAP * ACK :-sasl`
  aktyvuodavo sasl'ą, pradėdamas `AUTHENTICATE PLAIN` dialogą: priešiškas
  serveris savavališku ACK galėtų gauti **SASL slaptažodį be mūsų prašymo**
  (apeidamas `saslPending` apsaugą). Dabar „-“ prefiksas PAŠALINA cap'ą,
  o AUTHENTICATE pradedamas TIK jei patys užsakėme sasl'ą (`capSASL` +
  saslPass).

### Taisyta (kiti)

- **echo-message: savo `/me` AIDAS neberodomas dukart** – CTCP ACTION
  filialas grįždavo iki echo filtro; dabar savo ACTION aidas atmetamas
  (kito vartotojo `/me` nepakitęs).
- **`/quit` dabar laukia, kol QUIT išsiunčiamas** – `finishWrites()`
  praneša rašytojai goroutine baigti darbą (buferio flush) ir jos palaukia
  (iki 300 ms); kitaip procesas gali pasibaigti dar prieš QUIT eilutės
  išsiuntimą — kiti vartotojai matytų, ryšys nutrūko. writeStop uždarymas
  apsaugotas `sync.Once` (dvigubas close /quit + nutrūkimo atveju be
  panikos).
- **MODE į neprijungtą kanalą NEBEKURIA lango** — kaip 332/353: priešiškas
  serveris MODE srautu galėtų prikurti šimtą tuščių langų (addMsg viduje
  `ensureCh` kūrė kanalą; dabar grįžtama dar prieš jį).
- **251 (RPL_LUSERCLIENT) rodo pilną žinutę** – visi skaičiai — VIENAS
  galinis parametras; anksčiau jis būdavo išskaidytas į tuščius laukus
  („vartotojų,  nežinomų,  kanalų“).
- **322 (LIST) stulpeliai pagal LĄSTELIŲ plotį** – `padRight(sanitize, 30)`
  vietoj `%-30s` (fmt skaičiuoja runes: CJK/emoji kanalų vardai pastums
  kiekio stulpelį skirtingai kiekvienoje eilutėje). Ta pati pataisa, kuri
  anksčiau padaryta 352 (WHO).
- **Statuso juostos kairysis kraštas nukirpiamas** (`clampStatusLeft()`) –
  ilgas kanalo pavadinimas (ilgio ribos nėra — priešiškas serveris) kitaip
  perlenkia juostą į antrą eilutę. Ir unread `badge` ribojamas iki eilei
  likusio ploto (milžiniškas skaičius nebe išlenda už „│“).
- **Negyvas kodas** – `nickOrder` (nepanaudotas) pašalintas;
  `nickIsServ` — vienas `EqualFold` vietoj trijų dubliuojančių.

## [0.4.5] – 2026-10-21 – peržiūros prieš merge pataisos + klavišai

Ketvirtoji peržiūra (code-review viso šakos diff'o) aptiko 10 radinių.
Visi ištaisyti, kiekvienas — su regresijos testu (`perziura_2026_10_21c_test.go`).

### Taisyta (klavišai)

- **`Ctrl+Home`/`Ctrl+End` PAŠALINTA** — kanalų sąrašo valdymo klavišai
  nereikalingi: sąrašas VISADA automatiškai suseka paskui aktyvų kanalą
  (`switchCh`), o visos klavišai (`Ctrl+↑↓` nick'ams, `Ctrl+PgUp/Dn`
  žinutėms) lieka. Rankinis sąrašo peržiūrėjimas — pelės spustelėjimais
  (ir ▲▼ rodikliai). Home/End dabar VISADA judina įvesties žymeklį.
- **Valdymo sekų buferiavimas** – `readKeys()` suburia sekas, kurias
  terminalas persiunčia per kelis skaitymus (anksčiau pirmas gabalas —
  „ESC[“ — būtų prarastas; atskiras Esc visada veikia iškart).

### Taisyta (kritiniai)

- **Skaitiklio buferis 512 → 4096 (`maxReadBuffer`)** – serverio pridėti
  IRCv3 tag'ai (`server-time`, `msgid` — kuriuos patys prašome per CAP LS)
  neįeina į eilutės 512 baitų biudžetą, todėl su 512 baitų buferiu kiekviena
  reali žinutė su tag'ais (~550 B) sukelia `bufio.ErrBufferFull` ir būdavo
  **atmetama — pokalbis prarastas**. Dabar buferis 4096; ilgesnės eilutės
  vis tiek atmestamos (DoS apsauga lieka).

- **`chanScrollOff` sugrubinamas kiekviename naudotojui** – Ctrl+End,
  `switchCh` ir `handleMouse` anksčiau naudojo/įrašinėjo nesugrubintą
  offsetą → po overscroll'o **pelės kanalų pasirinkimas** per skaiciuotų
  NETEISINGĄ indeksą (pasirinkdavo ne tą kanalą ar nieko). Dabar
  `clampChanScrollLocked()` visiems.

- **`nickEqual()` (CASEMAPPING=ascii) suliejimas sutampa su `nickSet`
  raktais** – anksčiau rinkinys naudojo `asciiFold` (ne-ASCII neliečia),
  o `nickEqual` — `strings.EqualFold` (sulieja ir ne-ASCII: Ž ≡ ž). Dviejų
  skirtingų fold'ų pasėkoje QUIT „žilvinas“ rinkinyje atrastų nick'ą
  „Žilvinas“, o `findNickIdx` pagal raktą jo NESURASTŲ → **ghost nick'ai**.
  Dabar abu naudoja tą patį `foldEqualIRC()` — vieną fold'o šaltinį.

- **332 (tema), TOPIC ir KICK nebekuria kanalo, kurio nesame** – kaip ir
  353: priešiškas serveris srauto numerikais galėtų prikurti langų ir
  atimti kanalų biudžetą. Anksčiau apsauginis `findCh` buvo be reikšmės —
  po jo sekejo `addMsg`, kuris vis tiek kūrė per `ensureCh`.

### Taisyta (kita)

- **QUIT o(n²) pašalintas ir iš trynimo kelių** – kanalų paieška QUIT metu
  per `nickSet` rinkinį (O(1) per kanalą), o `findNickIdx` palyginimas —
  be atminties alokacijų (`foldEqualIRC` rune/baitais, ne `strings.Map`
  kas elementui). 5 000 QUIT'ų 5 000-nick kanale nebeužstringą kliento.

- **Kanalų limito įspėjimas sunaudojamas tik teisingoje priežastyje** –
  klastoto tikslo (pvz. kanalas su tarpu) atmetimas anksčiau „suvalgydavo“
  įspėjimą apie KITA kanalą pasiektą limitą. Dabar `chanLimitHit` naudojamas
  tik kai įsimintas vardas atitinka atmestą kanalą (addMsg, JOIN, /join).

- **`switchCh` slinktis pagrįsta TIKRU matomų eilučių skaičiumi** –
  `visibleChannelRowsLocked()` skaičiuoja `msgAreaTop` taip pat, kaip
  `draw()` (temos juosta +1, paieškos juosta +1). Anksčiau buvo kieta
  `height-4` — neatitiko be temos arba su paieškos juosta → slinktis
  drift'ino nuo ekrano.

- **Unread žymos stulpelis ląstelėmis** – `fitWidth` vietoje `%-*s`
  (baitais): kitaip ne-ASCII kanalo pavadinimai su unread žyma išstumdavo
  už `chanListWidth` ir šoninė juosta kryptų.

- **Slaptažodžio heuristika (log'o lygiu) nebe slaptina įprastų pokalbių** –
  `isSecretArgument` buvo negyvas kodas (visi keliai → true): eilutė
  „pass the“ žurnale tamstoma „[slaptažodis paslėptas]“. Dabar forma
  tikra: skaičius / didžioji raidė / specialus simbolis (arba ilgas
  mažųjų žodis >10). Negyvas `commandForm` parametras pašalintas.

- **WHO (352/354) stulpeliai** – flagas ATSKIRAS tarpu (anksčiau „Hbob“
  iš „H“+«bob“), kanalo stulpelis `fitWidth` pagal ląstelių plotį.

## [0.4.4] – 2026-10-21 – lygiagretumo, ribų ir komandų tikslo pataisymai

Tretioji peržiūra (po 23 ir 24 radinių) aptiko dar 18 radinių — daugiausia
**lygiagretumo** trūkumai, **ribų** trūkumai ir **komandų tikslo
klastojimas**. Visi ištaisyti, kiekvienas — su regresijos testu („blogas
atmetamas“ **ir** „teisingas priimamas“).

### Taisyta (lygiagretumas — kritiniai)

- **Aktyvaus kanalo laukai skaitomi tik su `c.mu`** – `activeChan()` ir
  `activeName()` anksčiau skaito bendrą būseną **be rakino**: 12 vietų
  `input.go` + 2 `irc.go`. Tai duomenų lenktynė (patikrinta `go test -race`).
  Dabar yra trys variantai: `activeChanLocked()` (kai `c.mu` jau užrakintas),
  `activeChanSafe()` (užrakina pats save) ir `activeName()`.

- **`draw()` nebeskaito kanalo laukų po `c.mu.Unlock()`** – `ch.highlight`,
  `ch.unread`, `ch.isPrivate` buvo skaitomi be rakino, kai rašytojas
  (`irc.go`) tuo patu metu juos keitė. Dabar laukų reikšmės nukopijuojamos
  po rakinu į atskirą struktūrą.

- **`/names` nebesuna lenktynės su `AWAY`** – `active.nicks = nil` lenktyniavo
  su `AWAY` apdorojimu, kuris skaitė `active.nicks` po `Unlock`.

- **`c.keysDebug` skaitomas su `c.mu`** – `/keys` komanda jį keitė iš
  `parseIRC` gorutinos, o klaviatūros gija skaitė be rakino.

### Taisyta (ribos)

- **Kanalų biudžetai atskirti** – `maxChannels` (100 realių kanalų) ir
  `maxPrivateChannels` (200 privačių pokalbių) anksčiau buvo **vienas**:
  100 privačių pokalbių (PM flood) išnaudodavo visą biudžetą, ir po to
  **realus kanalas nebebūdavo sukuriamas** — jo žinutės tyliai išnykdavo
  (vartotojas manydavo, kad kanale nieko nėra). Dabar PM flood eikdoja tik
  privačių pokalbių biudžetą, o riba pasiekus **vartotojas įspėjamas**.

- **Nick’ų sąrašas ribojamas** (`maxNicksPerChannel` = 5000) – priešiškas
  serveris siųsdavo tūkstančius `JOIN` į vieną kanalą; sąrašas augo
  neribotai.

- **`addNick` nebeskaičiuoja O(n²)** – kiekvienas įterpimas buvo O(n)
  (per visus elementus su `stripMode` + `nickEqual`), todėl 10 000 nick’ų
  kanale užtruko **3,5 s** (užblokavus visą klientą). Dabar įterpimas yra
  O(log n) (dichotominė paieška) + O(1) (hash-rinkinys). **5000 nick’ų:
  1,06 s → 0,01 s** (100×).

- **`awayNotified` apribojamas** (`maxAwayNotified` = 5000) – be ribos
  žemėlapis augo neapribotai (priešiškas serveris siunčia `AWAY` nuo
  milijono nick’ų).

- **Serverio eilutės riba — 512 baitų** – `bufio.NewReader()` numatytuoju
  4096 baitų bufferiu leido serveriui siųsti 3 KB eilutę (ji nebuvo
  atmesta). Dabar `bufio.NewReaderSize(conn, 512)`.

### Taisyta (komandų tikslo klastojimas ir įvedimo apsauga)

- **Kanalai su tarpais atmesti** – serverio atsiųstas kanalas „#pub #priv“
  komandose būtų interpretuojamas kaip **papildomas parametras**
  (`MODE #pub #priv +o` → operatoriaus tikslas kitas kanalas). Tokie
  kanalai nebesukiami.

- **Validuoti komandų tikslai** – `/invite`, `/op`, `/deop`, `/voice`,
  `/devoice`, `/kick`, `/mode`, `/whois`, `/w`, `/list`, `/join` (raktas).
  Nick’ai, kanalai ir režimai negali turėti tarpų (papildomas parametras)
  ar CRLF.

- **`notify-send` parinkčių injekcija** – pranešimo tekstas (serverio
  kontroliuojamas) buvo interpretuojamas kaip parinktis (`--urgency=critical`).
  Dabar įterpiamas `--` skyriukas.

- **Žurnalo slaptažodžių nutekimas** – tikrinama **visos komandos** eilutė
  (anksčiau – tik kūno dalis), todėl `register`, `set p`, `set password`,
  `confirm`, `auth`, `PASS`, `OPER` (ir `/raw`/`/` formos) neateina į
  žurnalą. `/oper` forma anksčiau nebuvo aptinkama.

- **Gyvas terminalo sinkas per Tab-completion** – serverio `JOIN` su
  ESC-seką turinčiu nick’u galėjo pasiekti terminalą kaip **OSC 52**
  (rašymas į iškarpinę). Dabar kandidatai sanitizuojami, o braižoma
  eilutė valoma prieš išvestį į ekraną.

- **`MODE` argumentų indeksai pagal tikrąją `CHANMODES` specifikaciją** –
  `CHANMODES` suskirstytos į **4 grupes** (RFC 2811; ISUPPORT draftas):
  A (visada su parametru), B (visada), C (**tik nustatant**), D (niekada).
  Ankstesnė interpretacija (ir kietai kodintas `blkeI`) nulėkdavo
  indeksus – `MODE #chan +jo 30 bob` → `[@30 bob]` (prefiksas atiteko
  **argumentui**, ne nick’ui). Dabar → `[@bob 30]`.

- **`/clearlogs` veikia ir kanalui, kurio nėra šiame seanse** – jo
  žurnalai vis tiek gali būti diske; anksčiau komanda būdavo atmesta su
  klaida.

- **Žurnalų raktas nuoseklus su disko failo vardu** – `c.logFiles` raktas
  buvo `server/KANALAS/data` su **originaliu** kanalo pavadinimu, o failas
  diske — su **sanitizuotu**. Todėl `/clearlogs` (kuris ieškojo pagal
  sanitizuotą prefiksą) **nerado** atviro handle’o: failas likdavo
  neuždarytas ir toliau rašė į ištrintą failą. Dabar raktas, disko vardas
  ir `clearLogs()` prefiksas naudoja tą patį sanitizuotą pavadinimą.

- **`/clearlogs` neištrindavo kito kanalo žurnalo** – failų vardai
  `<KANALAS>-YYYY-MM-DD.log` buvo filtruojami pagal prefiksą
  `<KANALAS>-2` (tikintis, kad data visada prasideda „2“). Dėl to
  `/clearlogs #k` **ištrindavo kito kanalo `#k-2` žurnalą**, o senesnių
  metų (`#k-2025-03-15.log`) žurnalai **likdavo neištrinti**. Dabar
  tikrinama tiksli struktūra (tikslus kanalas + reali `YYYY-MM-DD` data).

- **SASL slaptažodis nebetaipa į NickServ** – `sasl=` skirtas
  bouncer/bridge autentifikacijai; anksčiau, kai SASL nepavykdavo arba
  serveris nepasiūlė `sasl` cap, jis būdavo siunčiamas per NickServ
  plaintext – slaptažodis nutekdavo trečiajam serveriui.

### Papildomai ištaisyta (savivertės diff'o patikra)

Agentas peržiūrėjo visą diff'ą ir rado dar **6** problemų — iš jų 5
patvirtintos paleidžiant kodą, 1 pripažinta sąmoningu sprendimu:

- **Žurnalas slėpė ĮPRASTĄ pokalbį** (duomenų praradimas) – slaptažodžio
  aptikimas buvo taikomas VISOMS žinutėms, o „pirmas žodis“ tikrinimas
  („register …“, „pass …“, „auth …“) slėpdė viską, kas prasideda šiais
  žodžiais – „register your vote today“ ar „pass the salt please“ → žurnalas
  tyliai prarado realų pokalbių tekstą. Dabar žurnalui naudojamas atskiras
  `isSensitiveLogLine` (tik komandų kontekstas), o įvedimo istorijai –
  pli `isSensitiveHistory`.

- **Papildomas tarpas apeikdavo slaptažodžio aptikimą** – „/raw  PASS x“
  (du tarpai) nebuvo aptinkama → slaptažodis lėkė į istoriją ir žurnalą
  plaintext. Dabar tarpai normalizuojami prieš tikrinimą.

- **Ignoruojamas serverio casemapping** – `normNick` visada naudojo RFC 1459
  lyginimą. Ant serverio su `CASEMAPPING=ascii` nick'ai „[a]“ ir „{a}“ yra
  **skirtingi** – „{a}“ būdavo atmestas kaip dublikatas, o vėlesnis QUIT
  ištrindavo ne tą nick'ą. Dabar `normNick` paiso casemapping'ą (ir
  atitinka `nickEqual()` abiem atvejais).

- **Serverio kaukė „*server*“ buvo atmesta** – `isValidMaskName` neleido
  „*“, todėl `/mode *server*` ir `/w *server*` (toks pats tikslas, kurį
  dokumentuoja pati validatoriaus funkcija) neveikė.

- **Kanalų raktai su specialiais simboliais buvo atmesti** – siauras
  leistinų simbolių sąrašas atmetė „s3cret!“, „k#1“, „p@ss“ → vartotojas
  negalėjo prisijungti į tokį kanalą. Dabar atmetami tik tarpai ir
  kontroliniai simboliai (tiek tikras pavojus).

- **MODE/WHO tikslas neleido nick'o simbolių** – `isValidModeTarget` atmetė
  „nick[1]“, nors `isValidNick` jį leidžia → `/mode nick[1] +o` buvo
  neįmanomas.

- **`awayNotified` pruininimas buvo per lėtas** – riba tikrinama kiekvieną
  kartą, o viršijus ribą atliekamas O(n log n) rūšiavimas. 5000 AWAY
  pranešimų = **1,49 s**. Dabar rūšiuojama tik kai riba viršijoma **ženkliai**
  (> riba + 25 %) → **2,87 ms** (520× greičiau), o žemėlapis lieka ribojamas.

- **Kanalų ribos įspėjimas buvo priskiriamas netinkamam kanalui** –
  `chanLimitHit` buvo globalus „lipni“ žymeklis: riba pasiekus viename
  kanale, įspėjimą gaudavo visos vėlesnės atmestos žinutės. Dabar
  saugomas **atsisakyto kanalo vardas**, o JOIN keliu įspėjimas rodomas
  iš karto (anksčiau JOIN apie ribą ne informavo visai).

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