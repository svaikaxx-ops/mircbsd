# -*- coding: utf-8 -*-
# Fake IRC serveris: TCP 127.0.0.1, CAP LS→REQ→ACK→END→001-376 flow,
# JOIN echo + 353/366, MONITOR +/− įrašymas, 730/731, PONG.
import socket
import threading
import time


class FakeServer:
    def __init__(self, mynick=b'testas'):
        self.srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        self.srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        self.srv.bind(('127.0.0.1', 0))
        self.srv.listen(1)
        self.port = self.srv.getsockname()[1]
        self.mynick = mynick
        self.nick_collisions = set()   # nick'ai, kuriems registracijoje → 433
        self.connections = 0           # priimtų jungčių skaičius
        self.cap_done = False
        self.registered = False
        self.received = []
        self.monitor_ops = []          # MONITOR + / − eilutės
        self.monitor = set()           # stebimi mask'ai
        self.lock = threading.Lock()
        self.live = False
        self.conn = None
        self.reg = threading.Thread(target=self._accept, daemon=True)
        self.reg.start()

    def start(self):
        # Skaitymo gija jau įjungta __init__ (self.reg) — tai tuščia.
        pass

    def _accept(self):
        # Kelios jungtys iš eilės: klientas jungiasi iš naujo po drop_client().
        while True:
            try:
                c, _ = self.srv.accept()
            except OSError:
                return
            with self.lock:
                self.connections += 1
                self.cap_done = False
                self.registered = False
            self.conn = c
            self.live = True
            self._serve(c)
            self.live = False

    def drop_client(self):
        """Nutraukia dabartinę jungtį (serverio „kritimas“)."""
        c = self.conn
        self.conn = None
        if c is not None:
            try:
                c.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
            c.close()

    def count_received(self, sub):
        with self.lock:
            return sum(1 for _ts, l in self.received if sub in l)

    def _serve(self, c):
        buf = b''
        while True:
            try:
                d = c.recv(65536)
            except OSError:
                break
            if not d:
                break
            buf += d
            while b'\n' in buf:
                line, buf = buf.split(b'\n', 1)
                line = line.rstrip(b'\r').decode('utf-8', 'replace')
                with self.lock:
                    self.received.append((time.time(), line))
                self.on_line(line)

    def on_line(self, line):
        parts = line.split(' ')
        cmd = parts[0].upper() if parts else ''
        if cmd == 'CAP':
            if len(parts) > 1 and parts[1].upper() == 'LS':
                self.send_line('CAP * LS :multi-prefix')
            elif len(parts) > 1 and parts[1].upper() == 'REQ':
                req = ' '.join(parts[2:]).lstrip(':')
                self.send_line('CAP * ACK :' + req)
            elif len(parts) > 1 and parts[1].upper() == 'END':
                self.cap_done = True
                self.maybe_register()
            return
        if cmd == 'NICK' and len(parts) > 1:
            want = parts[1].lstrip(':')
            if not self.registered and want in self.nick_collisions:
                self.send_line(':fakas 433 * ' + want + ' :Nickname is already in use')
                return
            self.mynick = want.encode()
            self.maybe_register()
            return
        if cmd == 'JOIN' and len(parts) > 1 and parts[1].startswith('#'):
            ch = parts[1]
            n = self.mynick.decode('utf-8', 'replace')
            self.send_line(':' + n + '!e2e@fake JOIN :' + ch)
            self.send_line(':fakas 353 ' + n + ' = ' + ch + ' :' + n + ' +botas @viesas')
            self.send_line(':fakas 366 ' + n + ' ' + ch + ' :End of NAMES list')
            return
        if cmd == 'PRIVMSG':
            return
        if cmd == 'PING':
            # Kliento periodinis PING — be PONG'o klientas po timeout'o
            # laiko jungtį mirusia (E2E: žinutės nustoja ateiti ~60s+).
            self.send_line('PONG :' + (parts[1].lstrip(':') if len(parts) > 1 else 'fakas'))
            return
        if cmd == 'MONITOR' and len(parts) > 1:
            op = parts[1]
            targets = ' '.join(parts[2:])
            with self.lock:
                self.monitor_ops.append(line)
            if op == '+':
                for t in targets.split(','):
                    self.monitor.add(t.strip())
            elif op == '-':
                for t in targets.split(','):
                    self.monitor.discard(t.strip())
            elif op == 'CLEAR':
                self.monitor.clear()
            return
        if cmd in ('PART', 'QUIT', 'TOPIC', 'MODE', 'AWAY'):
            return

    def maybe_register(self):
        # 001 tik kai CAP baigtas IR nick priimtas (433 atveju — po naujo NICK).
        if self.registered or not self.cap_done:
            return
        if self.mynick.decode('utf-8', 'replace') in self.nick_collisions:
            return
        self.registered = True
        self.register()

    def register(self):
        n = self.mynick.decode('utf-8', 'replace')
        self.send_line(':fakas 001 ' + n + ' :Welcome to the fake server')
        self.send_line(':fakas 002 ' + n + ' :Via host fakas')
        self.send_line(':fakas 005 ' + n + ' CHANTYPES=#& PREFIX=(ov)@+ '
                       'CHANMODES=beI,k,l,imnpst CASEMAPPING=rfc1459 '
                       'NETWORK=Fakas MONITOR=60 NICKLEN=30 :are supported')
        self.send_line(':fakas 375 ' + n + ' :- MOTD -')
        self.send_line(':fakas 372 ' + n + ' :E2E aplinkos fake MOTD eilutė')
        self.send_line(':fakas 376 ' + n + ' :MOTD pabaiga')
        self.send_line('PING :regbaiga')

    def send_line(self, line):
        if self.conn is None:
            return
        try:
            self.conn.sendall((line + '\r\n').encode('utf-8'))
        except OSError:
            pass

    def wait_received(self, sub, timeout=10):
        end = time.time() + timeout
        while time.time() < end:
            with self.lock:
                for _ts, l in self.received:
                    if sub in l:
                        return l
            time.sleep(0.05)
        return None

    def wait_monitor_op(self, sub, timeout=10):
        end = time.time() + timeout
        while time.time() < end:
            with self.lock:
                for l in self.monitor_ops:
                    if sub in l:
                        return l
            time.sleep(0.05)
        return None