# -*- coding: utf-8 -*-
# ANSI ekrano emuliatorius (120x40): CUP, ED (0/1/2), EL, ECH, OSC praleidimas.
# mircBSD naudoja moveTo(r,c) + EraseLine + \033[nX — todėl mums to užtenka.
import re

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
        self.reset()

    def reset(self):
        self.grid = [[' '] * self.cols for _ in range(self.rows)]
        self.row = 0
        self.col = 0

    def _put(self, ch):
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
        self.col += 1

    def _newline(self):
        self.row += 1
        if self.row >= self.rows:
            self.grid.pop(0)
            self.grid.append([' '] * self.cols)
            self.row = self.rows - 1

    def feed(self, text):
        i = 0
        n = len(text)
        while i < n:
            ch = text[i]
            if ch == '\x1b':
                nxt = text[i + 1] if i + 1 < n else ''
                if nxt == '[':
                    i += 2
                    buf = ''
                    while i < n:
                        c = text[i]
                        buf += c
                        i += 1
                        if (('A' <= c <= 'Z') or ('a' <= c <= 'z') or c == '~'):
                            break
                    self._csi(buf)
                    continue
                if nxt == ']':
                    # OSC — iki BEL arba ESC\ ; TIEK PRALEIDŽIAM (antraštės,
                    # clipboard'ai negali keisti ekrano)
                    j = i + 2
                    while j < n and text[j] != '\x07' and text[j:j + 2] != '\x1b\\':
                        j += 1
                    i = j + 2 if j + 1 < n and text[j] != '\x07' else j + 1
                    continue
                # Dviejų simbolių sekos (pvz. \x1b(B) — praleidžiam
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