# -*- coding: utf-8 -*-
# E2E tvarkyklė: mircBSD faiko pty (120x40), klaviatūros siuntimas,
# ekrano kadras + neinterpretuota žaliava. Aplinka izoliuota: HOME +
# XDG_CONFIG_HOME nukreipti į /tmp (tikrasis ~/.config/mircbsd neliečiamas).
import codecs
import fcntl
import os
import pty
import select
import signal
import struct
import termios
import threading
import time

import ansi

# Artefaktų katalogas (home/, drive.log) — /tmp, kad repo liktų švarus.
TESTENV = os.environ.get('MIRCBSD_E2E_DIR', '/tmp/mircbsd-e2e')
BINARY = os.environ.get('MIRCBSD_BIN', os.path.join(TESTENV, 'bin', 'mircbsd'))
COLS, ROWS = 120, 40


class Driver:
    def __init__(self, home, logdir):
        self.home = home
        self.logdir = logdir
        self.screen = ansi.Screen(COLS, ROWS)
        self.raw = bytearray()
        self.raw_pos_on_mark = 0
        self.marks = {}
        self.last_feed = 0.0
        self.alive = True
        self.lock = threading.Lock()
        self.pid = None
        self.master = None
        self.finished = None
        self.logfile = open(os.path.join(TESTENV, 'drive.log'), 'wb')

    def start(self, port):
        env = dict(os.environ)
        env.update({
            'TERM': 'xterm-256color',
            'HOME': self.home,
            'XDG_CONFIG_HOME': os.path.join(self.home, '.config'),
            'LC_ALL': 'C.UTF-8',
            'LANG': 'C.UTF-8',
            'SHELL': '/bin/false',
        })
        self.pid, self.master = pty.fork()
        if self.pid == 0:
            # Vaikas: mircBSD <serveris> <portas> — args yra server+port
            os.chdir(self.home)
            for k, v in env.items():
                os.environ[k] = v
            try:
                os.execve(BINARY,
                          ['mircbsd', '127.0.0.1', str(port)], env)
            except Exception:
                os._exit(127)
        winsz = struct.pack('HHHH', ROWS, COLS, 0, 0)
        fcntl.ioctl(self.master, termios.TIOCSWINSZ, winsz)
        threading.Thread(target=self._reader, daemon=True).start()
        threading.Thread(target=self._reaper, daemon=True).start()

    def _reader(self):
        dec = codecs.getincrementaldecoder('utf-8')(errors='replace')
        while self.alive:
            r, _, _ = select.select([self.master], [], [], 0.05)
            if not r:
                continue
            try:
                data = os.read(self.master, 65536)
            except OSError:
                break
            if not data:
                break
            with self.lock:
                self.raw.extend(data)
                text = dec.decode(data)
                self.screen.feed(text)
                self.last_feed = time.time()
            self.logfile.write(data)
            self.logfile.flush()

    def _reaper(self):
        try:
            self.finished = os.waitpid(self.pid, 0)
        except ChildProcessError:
            pass

    def send(self, data):
        try:
            os.write(self.master, data)
        except OSError:
            pass

    def keys(self, text):
        self.send(text.encode('utf-8'))

    def mark_raw(self, name):
        with self.lock:
            self.marks[name] = len(self.raw)

    def raw_since(self, name):
        with self.lock:
            return bytes(self.raw[self.marks[name]:])

    def snapshot(self):
        with self.lock:
            return self.screen.dump()

    def snapshot_since(self, ts):
        """Tik tuščias ekrano tekstas PO ts (patikrų eilėms nereiškia
        seno turinio atitikmenų) — pigi apytikrė versija: pilnas kadras
        PO N sekundžių laukimo."""
        return self.snapshot()

    def quit(self):
        self.alive = False
        try:
            os.kill(self.pid, signal.SIGKILL)
        except OSError:
            pass
        try:
            self.logfile.flush()
        except OSError:
            pass


def wait_until(fn, timeout=20, interval=0.1):
    end = time.time() + timeout
    last = False
    while time.time() < end:
        try:
            last = fn()
        except Exception:
            last = False
        if last:
            return True
        time.sleep(interval)
    return last