# -*- coding: utf-8 -*-
# Ekrano emuliatoriaus (ansi.py) savitikra — paleidžiama prieš E2E.
# 1) Escape seka, perskelta per dvi pty read() dalis ties BET KURIA vieta,
#    turi duoti tą patį kadrą kaip ir neperskelta (anksčiau ekrane likdavo
#    „13;104H“ / „[16X“ šiukšlės → nestabilios patikros).
# 2) Platūs simboliai (CJK/emoji) užima 2 ląsteles, derinamieji — 0.
import sys

import ansi

SAMPLE = ('\x1b[2J\x1b[H\x1b[1;1Hlabas\x1b[13;104Hdesinė\x1b[0m\x1b(B'
          '\x1b]0;antraštė\x07\x1b[2;5H\x1b[16Xpo\x1b]2;x\x1b\\tęsinys'
          '\x1b[3;1H中文😀éx\x1b[38;5;94mspalva\x1b[0m\x1b[4;1Habc\x1b[K')


def frame(chunks, cols=120, rows=20):
    s = ansi.Screen(cols, rows)
    for c in chunks:
        s.feed(c)
    return s


def main():
    fails = []
    whole = frame([SAMPLE]).dump()
    for cut in range(1, len(SAMPLE)):
        got = frame([SAMPLE[:cut], SAMPLE[cut:]]).dump()
        if got != whole:
            fails.append('perskėlimas ties %d (%r|%r)' % (
                cut, SAMPLE[max(0, cut - 6):cut], SAMPLE[cut:cut + 6]))
            break
    # Vienas simbolis per feed() — blogiausias atvejis.
    if frame(list(SAMPLE)).dump() != whole:
        fails.append('po vieną simbolį — kitas kadras')
    for junk in ('13;104H', '[16X', '(B', ']0;', '38;5;94m'):
        if junk in whole:
            fails.append('šiukšlės ekrane: %r' % junk)
    s = frame([SAMPLE])
    if s.line(12)[103:].rstrip() != 'desinė':
        fails.append('CUP 13;104: %r' % s.line(12)[100:])
    row = list(s.grid[2])
    # 中 文 😀 — po 2 ląsteles, e+akūtas — 1, x — 1 → 'spalva' nuo 9 stulpelio.
    if row[:9] != ['中', '', '文', '', '😀', '', 'é', 'x', 's']:
        fails.append('plačių simbolių ląstelės: %r' % row[:9])
    # Platus simbolis paskutinėje ląstelėje perkeliamas į kitą eilutę.
    w = frame(['\x1b[1;5H' + 'ab中'], cols=6)
    if w.line(0) != '    ab' or w.line(1) != '中':
        fails.append('platus simbolis eilutės gale: %r / %r' % (w.line(0), w.line(1)))
    if 'tęsinys' not in s.line(1) or 'po' not in s.line(1):
        fails.append('OSC su ESC\\ terminatoriumi: %r' % s.line(1))
    for f in fails:
        print('ansi_selftest NEPRALAUŽĖ: ' + f)
    print('ansi_selftest: %s' % ('OK' if not fails else '%d klaidos' % len(fails)))
    return 1 if fails else 0


if __name__ == '__main__':
    sys.exit(main())
