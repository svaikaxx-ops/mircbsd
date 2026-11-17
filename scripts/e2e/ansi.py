# -*- coding: utf-8 -*-
# ANSI ekrano emuliatorius (120x40): CUP, ED (0/1/2), EL, ECH, OSC praleidimas.
# mircBSD naudoja moveTo(r,c) + EraseLine + \033[nX — todėl mums to užtenka.
import re
import unicodedata


def cell_width(ch):
    """Terminalo ląstelių plotis (kaip wcwidth): derinamieji/ZW — 0,
    East Asian Wide/Fullwidth (CJK, dauguma emoji) — 2, kiti — 1."""
    o = ord(ch)
    if o in (0x200b, 0x200c, 0x200d, 0x2060, 0xfeff) or 0xfe00 <= o <= 0xfe0f:
        return 0
    if unicodedata.combining(ch) or unicodedata.category(ch) in ('Mn', 'Me'):
        return 0
    if unicodedata.east_asian_width(ch) in ('W', 'F'):
        return 2
    return 1

def _int_default(parts, idx, default=1):
    if idx < len(parts) and parts[idx].strip():
        try:
            return int(parts[idx].strip())
        except ValueError:
            return default
    return default


class Screen:
    def __init__(self, cols=120, rows=40):
        self.cols = cols
        self.rows = rows
        self.pending = ''
        self.reset()

    def reset(self):
        self.grid = [[' '] * self.cols for _ in range(self.rows)]
        self.row = 0
        self.col = 0

    def resize(self, cols, rows):
        """SIGWINCH: naujas dydis, tuščias ekranas (klientas perbraižo)."""
        self.cols = cols
        self.rows = rows
        self.reset()

    def _put(self, ch):
        w = cell_width(ch)
        if w == 0:
            # Prijungiam prie ankstesnės ląstelės (derinamasis ženklas).
            if 0 <= self.row < self.rows and self.col > 0:
                self.grid[self.row][self.col - 1] += ch
            return
        if w == 2 and self.col == self.cols - 1:
            # Platus simbolis netelpa paskutinėje ląstelėje — terminalas
            # perkelia jį į kitą eilutę.
            self.col = self.cols
        if self.col >= self.cols:
            # Įprastas terminalas perkelia į kitą eilutę; draw'erybė mircBSD
            # nesiremia apvyniojimu, bet ekrane būna ir tiesioginio prints.
            self.row += 1
            if self.row >= self.rows:
                self.grid.pop(0)
                self.grid.append([' '] * self.cols)
                self.row = self.rows - 1
            self.col = 0
        if 0 <= self.row < self.rows and 0 <= self.col < self.cols:
            self.grid[self.row][self.col] = ch
            if w == 2 and self.col + 1 < self.cols:
                # Antroji plataus simbolio ląstelė — tuščia (dump'e nematoma).
                self.grid[self.row][self.col + 1] = ''
        self.col += w

    def _newline(self):
        self.row += 1
        if self.row >= self.rows:
            self.grid.pop(0)
            self.grid.append([' '] * self.cols)
            self.row = self.rows - 1

    def feed(self, text):
        # pty read() gali perskelti escape seką per dvi dalis („\x1b[13;1“ +
        # „04H“) — nebaigta seka laikoma self.pending iki kito feed().
        text = self.pending + text
        self.pending = ''
        i = 0
        n = len(text)
        while i < n:
            ch = text[i]
            if ch == '\x1b':
                if i + 1 >= n:
                    self.pending = text[i:]
                    return
                nxt = text[i + 1]
                if nxt == '[':
                    j = i + 2
                    # CSI galutinis baitas: 0x40–0x7E (ECMA-48).
                    while j < n and not ('@' <= text[j] <= '~'):
                        j += 1
                    if j >= n:
                        self.pending = text[i:]
                        return
                    self._csi(text[i + 2:j + 1])
                    i = j + 1
                    continue
                if nxt == ']':
                    # OSC — iki BEL arba ESC\ ; TIEK PRALEIDŽIAM (antraštės,
                    # clipboard'ai negali keisti ekrano)
                    j = i + 2
                    while j < n and text[j] != '\x07' and text[j:j + 2] != '\x1b\\':
                        j += 1
                    if j >= n or (text[j] == '\x1b' and j + 1 >= n):
                        self.pending = text[i:]
                        return
                    i = j + 1 if text[j] == '\x07' else j + 2
                    continue
                if nxt in '()*+':
                    # Simbolių rinkinio parinkimas (\x1b(B) — 3 simboliai.
                    if i + 2 >= n:
                        self.pending = text[i:]
                        return
                    i += 3
                    continue
                # Kitos dviejų simbolių sekos (\x1b7, \x1b=) — praleidžiam.
                i += 2
                continue
            if ch == '\r':
                self.col = 0
            elif ch == '\n':
                self._newline()
            elif ch == '\b':
                self.col = max(0, self.col - 1)
            elif ch == '\t':
                self.col = min(self.cols - 1, ((self.col // 8) + 1) * 8)
            elif ch >= ' ':
                self._put(ch)
            i += 1

    def _csi(self, buf):
        if not buf:
            return
        final = buf[-1]
        params = buf[:-1]
        parts = params.split(';')
        if final in 'Hf':
            r = _int_default(parts, 0, 1)
            c = _int_default(parts, 1, 1)
            self.row = min(max(r - 1, 0), self.rows - 1)
            self.col = min(max(c - 1, 0), self.cols - 1)
            return
        if final == 'J':
            k = _int_default(['0' if params == '' else params, ''], 0, 0)
            if params in ('', '0'):
                for c in range(self.col, self.cols):
                    self.grid[self.row][c] = ' '
                for r in range(self.row + 1, self.rows):
                    self.grid[r] = [' '] * self.cols
            elif params == '2':
                self.grid = [[' '] * self.cols for _ in range(self.rows)]
            elif params == '1':
                for r in range(self.row):
                    self.grid[r] = [' '] * self.cols
                for c in range(self.col):
                    self.grid[self.row][c] = ' '
            return
        if final == 'K':
            k = 0 if params in ('', '0') else (2 if params == '2' else 1)
            if k == 0:
                for c in range(self.col, self.cols):
                    self.grid[self.row][c] = ' '
            elif k == 1:
                for c in range(self.col + 1):
                    self.grid[self.row][c] = ' '
            else:
                self.grid[self.row] = [' '] * self.cols
            return
        if final == 'X':
            k = _int_default([params] if params else [''], 0, 1)
            for c in range(self.col, min(self.col + k, self.cols)):
                self.grid[self.row][c] = ' '
            return
        # Kiti (m, h, l, A-D, r, s) — SGR/neefektyviai ekrano valdymui —
        # ignoruojam.

    def dump(self):
        return '\n'.join(''.join(r).rstrip() for r in self.grid)

    def line(self, r):
        return ''.join(self.grid[r]).rstrip()

    def cells(self, r):
        """Eilutės ląstelės (plačių simbolių antroji ląstelė = '')."""
        return list(self.grid[r])