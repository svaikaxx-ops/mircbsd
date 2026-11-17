# -*- coding: utf-8 -*-
# E2E patikros paleidimas: sukuria izoliuotą home + config + keys,
# paleidžia fake serverį ir mircBSD, nuosekliai dirba scenarijus ir
# tikrina GALUTINĄ ekrano kadrą (ne žaliavą), kur tai reikšminga.
import json
import os
import shutil
import sys
import time

import driver
import server as srvmod

TESTENV = os.environ.get('MIRCBSD_E2E_DIR', '/tmp/mircbsd-e2e')
HOME = os.path.join(TESTENV, 'home')
CFG = os.path.join(HOME, '.config', 'mircbsd')
LOGDIR = os.path.join(HOME, 'irclogs')
ROWS = 40

BOT = 'botas!bot@fake'


def fresh_home():
    if os.path.exists(HOME):
        shutil.rmtree(HOME)
    os.makedirs(CFG)
    os.makedirs(LOGDIR)
    with open(os.path.join(CFG, 'config'), 'w') as f:
        f.write('# E2E fake aplinka\n')
        f.write('nick=testas\n')
        f.write('realname=E2E testinis\n')
        f.write('autojoin=#testas,#antras\n')
        f.write('logdir=%s\n' % LOGDIR)
        f.write('timestamps=true\n')
        f.write('ts_format=15:04\n')
        f.write('theme=dark\n')
        f.write('maxmessages=1000\n')
    # keys: perrašom search į ESC+S; + ĮSPĖJIMO eilutė (nežinomas veiksmas)
    with open(os.path.join(CFG, 'keys'), 'w') as f:
        f.write('search = \\eS\n')
        f.write('nezinomas = \\x12\n')


def main():
    fresh_home()
    srv = srvmod.FakeServer()
    srv.start()
    d = driver.Driver(HOME, LOGDIR)
    d.start(srv.port)
    time.sleep(1.0)

    S = d.snapshot
    results = []

    def record(name, fn):
        try:
            ok, note = fn()
        except Exception as e:   # noqa: BLE001 — rodom, ne krintam
            ok, note = False, 'EXC ' + repr(e)
        results.append((ok, name, note))
        if not ok:
            # Kadras derinimui: <TESTENV>/fail-<nr>.txt
            with open(os.path.join(TESTENV, 'fail-%s.txt' % name[:2]), 'w') as f:
                f.write(S())
        print('%-58s %s  (%s)' % (name, 'PASS' if ok else 'FAIL', note))

    def cmds(text):
        d.keys(text)
        time.sleep(0.4)

    def goto_srv():
        # Nepriklausomai nuo ankstsnių žingsnių būsenos — *server* lange
        d.keys('\x15')
        cmds('/win 1\r')
        driver.wait_until(lambda: '| *server* ' in d.screen.line(0), timeout=5)
        time.sleep(0.3)

    def botmsg(ch, text):
        for t in (text if isinstance(text, list) else [text]):
            srv.send_line(':' + BOT + ' PRIVMSG %s :%s' % (ch, t))
            time.sleep(0.06)

    # ---------- 1. Prisijungimas ----------
    def chk_connect():
        ok = driver.wait_until(
            lambda: ('Welcome to the fake server' in S()
                     and 'E2E aplinkos fake' in S()), timeout=15)
        return ok, '001 + MOTD ekrane' if ok else S()[-400:]

    record('01 prisijungimas (001 + MOTD)', chk_connect)

    def chk_keys_warn():
        ok = driver.wait_until(lambda: ' nežinomas veiksmas ' in S(), timeout=5)
        return ok, 'keys įspėjimas' if ok else S()[-300:]

    record('02 keys failo įspėjimas (nežinomas veiksmas)', chk_keys_warn)

    def chk_sidebar():
        ok = driver.wait_until(lambda: '#testas' in S() and '#antras' in S(), timeout=10)
        return ok, 'sidebar: #testas + #antras' if ok else S()[-300:]

    record('03 kanalų sąrašas (autojoin)', chk_sidebar)

    # ---------- 2. Kanalas #testas ----------
    # PIRMA laukiam, kad autojoin sukurtų kanalus. Pradinis aktyvusis kanalas
    # yra PASKUTINIS join'as (#antras!) — todėl atidariam #testas per /win 2
    # (f2 = 2: → *server*=1, #testas=2, #antras=3; \x0e žiedu cikliskai).
    driver.wait_until(lambda: '#testas' in S() and '#antras' in S(), timeout=10)
    cmds('/win 2\r')
    driver.wait_until(lambda: '| #testas ' in d.screen.line(0), timeout=5)
    botmsg('#testas', [
        'labas pasaulis',
        '\x0312spalvota žinutė\x0f',
        'injekcija\x1b[2Jišvalyta\x1b]0;HACK\x07 pabaiga',
        'ILGAŽINUTEPRAŠĖS-' + 'A' * 140 + '-BBB-tail',
    ])

    def chk_msg():
        ok = driver.wait_until(lambda: 'labas pasaulis' in S(), timeout=10)
        return ok, 'žinutė matoma' if ok else S()[-300:]

    record('04 PRIVMSG atvaizdavimas', chk_msg)

    def chk_color_strip():
        t = S()
        ok = 'spalvota žinutė' in t and '\x03' not in t
        return ok, 'spalvos išvalytos' if ok else 'žinutė arba \\x03 ekrane'

    record('05 IRC spalvų kodų sanavimas', chk_color_strip)

    def chk_injection():
        ok = driver.wait_until(lambda: 'injekcija' in S(), timeout=8)
        t = S()
        ok2 = 'labas pasaulis' in t     # ekrano nepalaužė \x1b[2J
        raw = d.logfile and open(os.path.join(TESTENV, 'drive.log'), 'rb').read()
        sent = '\x1b[2J' in raw.decode('utf-8', 'replace') if raw else True
        return ok and ok2 and sent, 'ESC/OSC sunaikinta' if ok and ok2 else t[-400:]

    record('06 terminalo injekcija neutralizuota', chk_injection)

    def chk_wrap():
        ok = driver.wait_until(lambda: 'BBB-tail' in S(), timeout=8)
        return ok, 'ilga žinutė apvalyta iki galo' if ok else S()[-400:]

    record('07 ilga žinutė — apvyniojimas', chk_wrap)

    # ---------- 3. Slinktis ----------
    botmsg('#testas', ['pr-%02d' % i for i in range(30)])
    time.sleep(0.4)

    def chk_scrollup():
        d.keys('\x1b[5~\x1b[5~')
        time.sleep(0.3)
        ok = driver.wait_until(lambda: 'pr-00' in S(), timeout=6)
        return ok, 'PgUp: pirmos flood žinutės matomos' if ok else S()[-400:]

    record('08 slinktis aukštyn (PgUp)', chk_scrollup)

    def chk_scrolldown():
        d.keys('\x1b[6;2~')
        time.sleep(0.3)
        ok = driver.wait_until(lambda: 'pr-29' in S(), timeout=6)
        return ok, 'Shift+PgDn: apačia' if ok else S()[-400:]

    record('09 slinktis žemyn (Shift+PgDn)', chk_scrolldown)

    # ---------- 4. Paieška (n/p) ----------
    def chk_search_np():
        cmds('/search labas\r')
        before = S()
        d.keys('n')     # tuščias input + n = paieškai pirmyn
        time.sleep(0.3)
        after_n = S()
        d.keys('p')     # atgal
        time.sleep(0.3)
        after_p = S()
        ok = after_n == before and after_p == before
        return ok, 'n ir p neĮrašo į input' if ok else 'ekranas pasikeitė po n/p'

    record('10 n/p tuščiam inputui — paieška, ne tekstas', chk_search_np)

    # ---------- 5. Paieškos būsenos atstatymas pereinant kanalą ----------
    def chk_search_reset():
        d.keys('\x0e')          # → #antras
        time.sleep(0.5)
        d.keys('n')             # DABAR įrašo n — search būsena išvalyta
        time.sleep(0.3)
        row = d.screen.line(ROWS - 1)
        ok = 'n' in row
        d.keys('\x15')          # Ctrl+U — išvalom input
        return ok, 'input gauna n (būsena išvalyta)' if ok else row

    record('11 pereinant kanalą search būsena išvalyta', chk_search_reset)

    # ---------- 6. /search all ----------
    def chk_search_all():
        cmds('/win 2\r')        # → #testas
        if not driver.wait_until(lambda: '| #testas ' in d.screen.line(0), timeout=5):
            return False, '/win 2 nepavyko: ' + d.screen.line(0)[:70]
        d.keys('\x15')
        cmds('/search all labas\r')
        cmds('/win 1\r')        # → serverio langas
        t = S()
        ok = 'Paieška „labas“' in t and ('testas <botas> labas pasaulis' in t)
        return ok, 'rezultatai *server* lange' if ok else t[-500:]

    record('12 /search all → rezultatai *server*', chk_search_all)

    # ---------- 7. /logsearch ----------
    def chk_logsearch():
        goto_srv()
        cmds('/logsearch labas\r')
        t = S()
        ok = ('.log> ' in t) and ('Žurnalų paieška:' in t)
        idx = t.find('.log>')
        return ok, t[max(0, idx):idx + 120].replace('\n', ' | ') if ok else t[-400:]

    record('13 /logsearch randa žinutę kūne', chk_logsearch)

    def chk_logsearch_date():
        goto_srv()
        before = S().count('.log> ')
        d.keys('\x15')
        cmds('/logsearch 12:34\r')
        t = S()
        ok = t.count('.log> ') == before and 'nieko nerasta' in t
        return ok, 'timestamp eilutės neatitiko (seni atitikmenys nepakartojo)' \
            if ok else '.log> kiekis %d→%d' % (before, t.count('.log> '))

    record('14 /logsearch — data timestamp’e neatitinka', chk_logsearch_date)

    # ---------- 8. /set ----------
    def chk_set_list():
        goto_srv()
        d.keys('\x15')
        cmds('/set\r')
        t = S()
        ok = 'timestamps = true' in t and 'theme = dark' in t
        return ok, '/set — sąrašas' if ok else t[-400:]

    record('15 /set — sąrašas', chk_set_list)

    def chk_set_invalid():
        goto_srv()
        cpath = os.path.join(CFG, 'config')
        before = open(cpath).read()
        d.keys('/set timestamps taip\r')
        time.sleep(0.5)
        t = S()
        after = open(cpath).read()
        ok = ('Reikšmė turi būti true/false' in t
              and 'taip' not in after and after == before)
        return ok, 'klaida + config nepaliestas' if ok else t[-300:]

    record('16 /set neteisinga bool — klaida be įrašo', chk_set_invalid)

    def chk_set_writeback():
        goto_srv()
        d.keys('\x15')
        cmds('/set ts_format 15:04:05\r')
        t = S()
        cfg = open(os.path.join(CFG, 'config')).read()
        ok = ('nustatymas: ts_format' in t and 'ts_format=15:04:05' in cfg)
        cmds('/set ts_format 15:04\r')       # grąžinam atgal
        return ok, 'ts_format live + write-back' if ok else t[-300:]

    record('17 /set ts_format — live + config write-back', chk_set_writeback)

    # ---------- 9. Tema ----------
    def chk_theme_light():
        d.keys('\x15')
        cmds('/win 2\r')        # → #testas
        if not driver.wait_until(lambda: '| #testas ' in d.screen.line(0), timeout=5):
            return False, '/win 2 nepavyko: ' + d.screen.line(0)[:70]
        # 1) žinutė TURI atsirasti ekrane — prieš temos patikrą įsitikinam,
        #    kad jungtis gyva. Po n/p paieškų slinktis lieka prie senosios
        #    pozicijos (TUI elgsena) — nauja žinutė atkeliauja ŽEMIAU, todėl
        #    pirmiau slinkam į apačią.
        d.keys('\x1b[6;2~')
        time.sleep(0.4)
        botmsg('#testas', ['tema prieš patikrą'])
        if not driver.wait_until(lambda: 'tema prieš patikrą' in S(), timeout=8):
            return False, 'žinutė neatvyko (jungtis?): ' + S()[-150:]
        d.mark_raw('theme')
        cmds('/set theme light\r')
        # 2) senos žinutės PERSIDAŽO su šviesia palette (38;5;*) —
        #    invalidateRenderCaches pakeičia draw (38;5;94m kodo reikšmė)
        if not driver.wait_until(lambda: '38;5;' in
                                 d.raw_since('theme').decode('utf-8', 'replace'), timeout=8):
            return False, 'šviesios paletės SGR neatsirado žaliavoje'
        raw = d.raw_since('theme').decode('utf-8', 'replace')
        cfg = open(os.path.join(CFG, 'config')).read()
        d.keys('/set theme dark\r')     # grąžinam atgal
        time.sleep(0.4)
        d.keys('\x10')          # → *server* — čia matom /set pranešimus
        driver.wait_until(lambda: '| ' + '127.0.0.1' in d.screen.line(0), timeout=5)
        time.sleep(0.3)
        ok = ('38;5;' in raw) and ('theme=light' in cfg) \
            and 'nustatymas: theme = light' in S()
        if ok:
            return True, 'theme=light: config + paletės persidažymas'
        with open(os.path.join(TESTENV, 'theme_raw.bin'), 'wb') as f:
            f.write(d.raw_since('theme'))
        return False, 'SGR=%r config=%r pranešimas=%r' % (
            '38;5;' in raw, 'theme=light' in cfg,
            'nustatymas: theme = light' in S())

    record('18 tema light — write-back + spalvos keičiasi', chk_theme_light)

    # ---------- 10. /notify ----------
    def chk_notify_add():
        goto_srv()
        d.keys('\x15')
        cmds('/notify add bob kitas\r')
        line = srv.wait_monitor_op('MONITOR +', timeout=10)
        ok = line is not None and 'bob,kitas' in line
        return ok, 'serveris gavo MONITOR + bob,kitas' if ok else 'serveris negavo MONITOR +'

    record('19 /notify add → MONITOR + bob,kitas', chk_notify_add)

    def chk_notify_online():
        goto_srv()
        srv.send_line(':fakas 730 testas :BOB!u@h')
        time.sleep(0.6)
        cmds('/notify list\r')
        t = S()
        ok = 'bob — online' in t
        return ok, 'BOB foldintas į bob online sąraše' if ok else t[-300:]

    record('20 730 uppercase → foldintas online sąraše', chk_notify_online)

    def chk_notify_invalid():
        goto_srv()
        d.keys('\x15')
        cmds('/notify add b@d\r')
        t = S()
        ok = 'Nevalidus nick' in t
        return ok, 'nevalidus nick atmestas' if ok else t[-300:]

    record('21 /notify add priešiškas nick', chk_notify_invalid)

    # ---------- 11. /exportjson ----------
    def chk_exportjson():
        goto_srv()
        d.keys('\x15')
        cmds('/exportjson #testas\r')
        time.sleep(0.8)
        t = S()
        path = None
        for root in (LOGDIR, HOME, CFG):
            for droot, _fs, names in os.walk(root):
                for n in names:
                    if n.endswith('.json'):
                        path = os.path.join(droot, n)
        if not path:
            p = t.find('.json')
            ok = p >= 0
            return ok, 'failo nerasta, pranešimas yra' if ok else t[-300:]
        try:
            blob = open(path).read()
            json.loads(blob)
            ok = 'labas pasaulis' in blob
            return ok, 'json failas %s (žinutė viduje: %s)' % (
                os.path.basename(path), ok)
        except Exception as e:   # noqa: BLE001
            return False, 'json failas invalid: %r' % e

    record('22 /exportjson — failas + validus JSON', chk_exportjson)

    # ---------- 12. /keys remap (search = ESC S) ----------
    def chk_keys_remap():
        d.keys('\x15')
        time.sleep(0.2)
        d.keys('\x1bS')     # perrašytas search (default: Ctrl+R)
        time.sleep(0.5)
        t = S()
        ok = ' Paieška: ' in t
        d.keys('\x15')
        return ok, 'ESC+S atidaro paiešką (keys remap)' if ok else t[-300:]

    record('23 keys failo remap (search → ESC+S)', chk_keys_remap)

    # ---------- 13. Ctrl+L perbraižymas ----------
    def chk_ctrl_l():
        goto_srv()
        d.keys('\x15')
        d.keys('\x0c')
        time.sleep(0.6)
        t = S()
        ok = '#testas' in t and 'labas pasaulis' in t
        return ok, 'ekranas atkurtas' if ok else t[-400:]

    record('24 Ctrl+L — pilnas perbraižymas', chk_ctrl_l)

    # ---------- 14. Platūs simboliai: jokia eilutė neišeina už ribų ----------
    def nick_sep_col(r):
        """Dešiniojo (nick sąrašo) skirtuko „│“ ląstelės indeksas eilutėje r."""
        cells = d.screen.cells(r)
        idx = [i for i, c in enumerate(cells) if c == '│']
        return idx[-1] if len(idx) >= 2 else None

    def message_rows(marker_chars):
        rows = []
        for r in range(1, d.screen.rows - 1):
            if any(m in d.screen.line(r) for m in marker_chars):
                rows.append(r)
        return rows

    def chk_wide_wrap():
        # 23 patikra paliko atidarytą paiešką (ESC+S) — Esc ją uždaro, kitaip
        # „/win 2“ keliautų į paieškos lauką.
        d.keys('\x1b')
        time.sleep(0.4)
        d.keys('\x15')
        cmds('/win 2\r')
        if not driver.wait_until(lambda: '| #testas ' in d.screen.line(0), timeout=5):
            return False, '/win 2 nepavyko: ' + d.screen.line(0)[:70]
        d.keys('\x1b[6;2~')
        time.sleep(0.3)
        # (a) CJK + emoji su tarpais, (b) „a‍中“ poros (ZWJ tarp ne emoji —
        # terminale 3 ląstelės), (c) tab'ai (terminale šuoliai iki 8).
        botmsg('#testas', [
            'PLATUS ' + '中文字😀 ' * 40 + 'GALAS-A',
            'ZWJ ' + 'a‍中' * 70 + ' GALAS-B',
            'TAB' + '\tx' * 30 + ' GALAS-C',
        ])
        if not driver.wait_until(lambda: 'GALAS-C' in S(), timeout=8):
            return False, 'žinutės neatvyko: ' + S()[-300:]
        time.sleep(0.4)
        sep = nick_sep_col(2)
        if sep is None:
            return False, 'nick sąrašo skirtukas nerastas: %r' % d.screen.line(2)
        bad = []
        for r in message_rows(['中', 'GALAS', 'TAB', 'ZWJ', ' x x']):
            cells = d.screen.cells(r)
            # Perpildyta eilutė terminale persikelia į kitą — tekstas atsiduria
            # kanalų juostoje (stulpeliai 0..17) arba už nick skirtuko.
            left = ''.join(cells[:18])
            right = ''.join(cells[sep + 1:])
            if ('中' in left or 'x x' in left or '中' in right or 'GALAS' in right
                    or cells[sep] != '│'):
                bad.append((r, d.screen.line(r)))
        t = S()
        whole = all(g in t for g in ('GALAS-A', 'GALAS-B', 'GALAS-C'))
        cnt = t.count('中文字😀')
        ok = not bad and whole and cnt == 40
        return ok, ('3 žinutės telpa, 中文字😀×%d' % cnt) if ok else \
            'perpildyta: %r; visos=%s; kiekis=%d' % (bad[:2], whole, cnt)

    record('25 platūs simboliai/ZWJ/tab — eilutės neperpildo', chk_wide_wrap)

    # ---------- 15. KICK savęs ----------
    def chk_kick_self():
        d.keys('\x15')
        srv.send_line(':viesas!v@fake KICK #antras testas :lauk-is-cia')
        cmds('/win 1\r')
        ok1 = driver.wait_until(
            lambda: 'išmestas iš #antras (lauk-is-cia)' in S(), timeout=8)
        ok2 = '#antras' in S()      # langas lieka kanalų juostoje
        # Savas nick dingsta iš #antras nick sąrašo.
        cmds('/win 3\r')
        driver.wait_until(lambda: '| #antras ' in d.screen.line(0), timeout=5)
        time.sleep(0.3)
        sep = nick_sep_col(2)
        nicks = ' '.join(''.join(d.screen.cells(r)[sep + 1:]) for r in range(1, 10)) \
            if sep else ''
        ok3 = 'botas' in nicks and 'testas' not in nicks
        ok = ok1 and ok2 and ok3
        return ok, 'pranešimas + langas + nick sąrašas' if ok else \
            'pranešimas=%s langas=%s nicks=%r' % (ok1, ok2, nicks.strip()[:80])

    record('26 KICK savęs — pranešimas, langas lieka', chk_kick_self)

    # ---------- 16. Lango dydžio keitimas (SIGWINCH) ----------
    def chk_resize():
        cmds('/win 2\r')
        driver.wait_until(lambda: '| #testas ' in d.screen.line(0), timeout=5)
        time.sleep(0.3)
        old_sep = nick_sep_col(2)
        d.resize(100, 30)
        botmsg('#testas', ['po dydžio keitimo ' + 'ž' * 150 + ' DYDIS-GALAS'])
        ok = driver.wait_until(
            lambda: 'DYDIS-GALAS' in S() and nick_sep_col(2) is not None
            and '| #testas ' in d.screen.line(0), timeout=8)
        time.sleep(0.4)
        new_sep = nick_sep_col(2)
        bottom = d.screen.line(29)
        # Be SIGWINCH apdorojimo klientas pieštų 120 stulpelių: CUP į 101+
        # stulpelį prispaudžiamas prie 100 → skirtukas būtų 99, ne old-20.
        ok = (ok and old_sep is not None and new_sep == old_sep - 20
              and bottom.strip() != '')
        rows_ok = all(len(d.screen.line(r)) <= 100 for r in range(30))
        d.resize(120, 40)
        driver.wait_until(lambda: nick_sep_col(2) == old_sep, timeout=8)
        return ok and rows_ok, 'skirtukas %s→%s, apačia=%r' % (
            old_sep, new_sep, bottom[:30])

    record('27 SIGWINCH — perbraižoma nauju dydžiu', chk_resize)

    # ---------- 17. Serverio kritimas → automatinis prisijungimas + 433 ----------
    def chk_reconnect_433():
        nick_before = srv.count_received('NICK ')
        # Kitas registracijos bandymas: „testas“ užimtas → 433.
        srv.nick_collisions = {'testas'}
        srv.drop_client()
        if not driver.wait_until(lambda: 'Ryšys nutrūko' in S()
                                 or 'Jungiamasi iš naujo' in S(), timeout=10):
            cmds('/win 1\r')
        ok_drop = driver.wait_until(lambda: srv.connections >= 2, timeout=20)
        ok_nick = srv.wait_received('NICK testas_', timeout=15) is not None
        ok_reg = driver.wait_until(lambda: srv.registered, timeout=15)
        ok_join = driver.wait_until(
            lambda: srv.count_received('JOIN #testas') >= 2, timeout=10)
        # Žinutė NAUJA jungtimi turi pasiekti ekraną.
        cmds('/win 2\r')
        d.keys('\x1b[6;2~')
        time.sleep(0.3)
        botmsg('#testas', ['po-persijungimo-labas'])
        ok_msg = driver.wait_until(lambda: 'po-persijungimo-labas' in S(), timeout=8)
        ok = ok_drop and ok_nick and ok_reg and ok_join and ok_msg \
            and srv.count_received('NICK ') > nick_before
        return ok, 'jungtis #%d, NICK testas_, JOIN, žinutė' % srv.connections if ok else \
            'jungtys=%d nick_=%s reg=%s join=%s msg=%s' % (
                srv.connections, ok_nick, ok_reg, ok_join, ok_msg)

    record('28 serverio kritimas → reconnect + 433 → nick_', chk_reconnect_433)

    # ---------- Rezultatai ----------
    good = sum(1 for ok, _n, _ in results if ok)
    print('\n==== E2E: %d/%d patikrų pralaužę ====' % (good, len(results)))
    for ok, name, note in results:
        if not ok:
            print('  NEPRALAUŽĘ: %s — %s' % (name, note[:200]))
    d.quit()
    return 0 if good == len(results) else 1


if __name__ == '__main__':
    sys.exit(main())