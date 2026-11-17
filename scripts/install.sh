#!/bin/sh
# mircBSD diegimas / atnaujinimas iš GitHub release.
#
#   curl -fsSL https://raw.githubusercontent.com/svaikaxx-ops/mircbsd/main/scripts/install.sh | sh
#   fetch -o - https://raw.githubusercontent.com/svaikaxx-ops/mircbsd/main/scripts/install.sh | sh   # FreeBSD
#
# Parinktys (perduodamos per `sh -s -- ...`):
#   --version vX.Y.Z   konkreti versija (numatyta: naujausia)
#   --system           į /usr/local/bin (per sudo), o ne ~/.local/bin
#                      (Termux: visada $PREFIX/bin, be sudo)
#   --dir KATALOGAS    į nurodytą katalogą
#   --force            diegti net jei ta pati versija jau įdiegta
#   --uninstall        pašalinti binarį (konfigūracija lieka)
#
# Tas pats skriptas ir diegia, ir atnaujina: konfigūracija
# (~/.config/mircbsd) niekada neperrašoma.
#
# MIRCBSD_RELEASES_URL – kitas release šaltinis (veidrodis arba CI patikra su
# vietiniu serveriu); turi būti GitHub struktūros: …/latest nukreipia į
# …/tag/vX.Y.Z, failai …/download/vX.Y.Z/<archyvas>.
set -eu

REPO="svaikaxx-ops/mircbsd"
BASE="${MIRCBSD_RELEASES_URL:-https://github.com/$REPO/releases}"

VERSION=""
DIR="${HOME}/.local/bin"
DIR_SET=0
SUDO=""
FORCE=0
UNINSTALL=0

say() { printf '%s\n' "mircbsd: $*"; }
die() { printf '%s\n' "mircbsd: KLAIDA: $*" >&2; exit 1; }

while [ $# -gt 0 ]; do
    case "$1" in
        --version) [ $# -ge 2 ] || die "--version reikia reikšmės"; VERSION="$2"; shift 2 ;;
        --system) DIR="/usr/local/bin"; SUDO="sudo"; DIR_SET=1; shift ;;
        --dir) [ $# -ge 2 ] || die "--dir reikia reikšmės"; DIR="$2"; DIR_SET=1; shift 2 ;;
        --force) FORCE=1; shift ;;
        --uninstall) UNINSTALL=1; shift ;;
        -h|--help)
            cat <<'EOF'
mircBSD diegimas / atnaujinimas iš GitHub release.
  --version vX.Y.Z   konkreti versija (numatyta: naujausia)
  --system           į /usr/local/bin (per sudo), o ne ~/.local/bin
                     (Termux: visada $PREFIX/bin, be sudo)
  --dir KATALOGAS    į nurodytą katalogą
  --force            diegti net jei ta pati versija jau įdiegta
  --uninstall        pašalinti binarį (konfigūracija lieka)
EOF
            exit 0 ;;
        *) die "nežinoma parinktis: $1 (žr. --help)" ;;
    esac
done

# Termux (Android): binarai — $PREFIX/bin (jau PATH'e), sudo nėra.
TERMUX=0
if [ -n "${TERMUX_VERSION:-}" ]; then
    TERMUX=1
else
    case "${PREFIX:-}" in *com.termux*) TERMUX=1 ;; esac
fi
if [ "$TERMUX" -eq 1 ]; then
    [ -n "${PREFIX:-}" ] || die "Termux: nenustatytas \$PREFIX"
    if [ "$SUDO" = "sudo" ] || [ "$DIR_SET" -eq 0 ]; then
        DIR="$PREFIX/bin"
    fi
    SUDO=""
fi

[ "$(id -u)" -eq 0 ] && [ -z "$SUDO" ] && [ "$DIR" = "${HOME}/.local/bin" ] &&
    say "įspėjimas: paleista kaip root – diegiama į $DIR (root namų katalogą)"
[ "$(id -u)" -eq 0 ] && SUDO=""
[ -n "$SUDO" ] && ! command -v sudo >/dev/null 2>&1 && die "--system reikia sudo"

BIN="$DIR/mircbsd"

if [ "$UNINSTALL" -eq 1 ]; then
    if [ -e "$BIN" ]; then
        $SUDO rm -f "$BIN"
        say "pašalinta: $BIN (konfigūracija ~/.config/mircbsd palikta)"
    else
        say "nerasta: $BIN"
    fi
    exit 0
fi

# --- platforma -----------------------------------------------------------
os="$(uname -s)"
arch="$(uname -m)"
case "$os" in
    Linux)
        if [ "$TERMUX" -eq 1 ]; then
            # Android: bionic binaris (Android DNS ir laiko juosta veikia;
            # statinis musl Android'e neranda /etc/resolv.conf).
            case "$arch" in
                aarch64|arm64) TARGET="aarch64-linux-android" ;;
                *) die "Termux $arch: paruošto binario nėra – kompiliuokite: pkg install rust git (README)" ;;
            esac
        else
        case "$arch" in
            x86_64|amd64) TARGET="x86_64-unknown-linux-musl" ;;
            aarch64|arm64) TARGET="aarch64-unknown-linux-musl" ;;
            armv7l|armv7*|armv8l) TARGET="armv7-unknown-linux-musleabihf" ;;
            armv6l|armv6*) TARGET="arm-unknown-linux-musleabihf" ;;   # Pi 1 / Zero / Zero W
            *) die "nepalaikoma architektūra: $arch – kompiliuokite iš šaltinio (README)" ;;
        esac
        fi ;;
    FreeBSD)
        case "$arch" in
            amd64|x86_64) TARGET="x86_64-unknown-freebsd" ;;
            *) die "FreeBSD $arch: paruošto binario nėra – kompiliuokite iš šaltinio (README)" ;;
        esac ;;
    *) die "paruošti binarai tik Linux ir FreeBSD ($os: kompiliuokite iš šaltinio, žr. README)" ;;
esac

# --- įrankiai ------------------------------------------------------------
# download URL FAILAS; latest_url – kur nukreipia $BASE/latest (…/tag/vX.Y.Z)
if command -v curl >/dev/null 2>&1; then
    download() { curl -fsSL --retry 3 -o "$2" "$1"; }
    latest_url() { curl -fsSLI -o /dev/null -w '%{url_effective}' "$BASE/latest"; }
elif command -v wget >/dev/null 2>&1; then
    download() { wget -q -O "$2" "$1"; }
    latest_url() { wget -q -S --spider "$BASE/latest" 2>&1 | sed -n 's/^ *[Ll]ocation: *//p' | tail -n 1 | tr -d '\r'; }
elif command -v fetch >/dev/null 2>&1; then
    # FreeBSD bazinė sistema: fetch(1) be curl/wget
    download() { fetch -q -o "$2" "$1"; }
    # fetch -v praneša kiekvieną nukreipimą: „302 redirect to URL“
    latest_url() { fetch -v -o /dev/null "$BASE/latest" 2>&1 | sed -n 's/^.* redirect to *//p' | tail -n 1 | tr -d '\r'; }
else
    die "reikia curl, wget arba fetch"
fi

if command -v sha256sum >/dev/null 2>&1; then
    sha256() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
    sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
elif command -v sha256 >/dev/null 2>&1; then
    sha256() { command sha256 -q "$1"; }   # FreeBSD
else
    die "reikia sha256sum, shasum arba sha256"
fi
command -v tar >/dev/null 2>&1 || die "reikia tar"

# --- versija -------------------------------------------------------------
if [ -z "$VERSION" ]; then
    url="$(latest_url)" || die "nepavyko gauti naujausios versijos"
    VERSION="${url##*/}"
    [ -n "$VERSION" ] || die "nepavyko gauti naujausios versijos ($BASE/latest)"
fi
case "$VERSION" in
    v[0-9]*) ;;
    [0-9]*) VERSION="v$VERSION" ;;
    *) die "neteisinga versija: '$VERSION'" ;;
esac

current=""
if [ -x "$BIN" ]; then
    current="v$("$BIN" --version 2>/dev/null | awk '{print $2}')" || current=""
fi
if [ "$current" = "$VERSION" ] && [ "$FORCE" -eq 0 ]; then
    say "jau įdiegta naujausia versija $VERSION ($BIN)"
    exit 0
fi

# --- atsisiuntimas ir patikra --------------------------------------------
NAME="mircbsd-$VERSION-$TARGET"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT INT TERM

say "atsisiunčiama $NAME ..."
download "$BASE/download/$VERSION/$NAME.tar.gz" "$TMP/$NAME.tar.gz" ||
    die "nepavyko atsisiųsti $VERSION ($TARGET) – ar tokia versija yra? $BASE"
download "$BASE/download/$VERSION/$NAME.tar.gz.sha256" "$TMP/$NAME.tar.gz.sha256" ||
    die "nepavyko atsisiųsti kontrolinės sumos"

want="$(cut -d' ' -f1 "$TMP/$NAME.tar.gz.sha256")"
got="$(sha256 "$TMP/$NAME.tar.gz")"
[ -n "$want" ] && [ "$want" = "$got" ] || die "sha256 nesutampa! (laukta $want, gauta $got)"
say "sha256 OK"

tar -xzf "$TMP/$NAME.tar.gz" -C "$TMP"
[ -f "$TMP/$NAME/mircbsd" ] || die "archyve nėra binario"
"$TMP/$NAME/mircbsd" --version >/dev/null 2>&1 || die "atsisiųstas binaris šioje sistemoje nepasileidžia"

# --- diegimas (atomiškai: kopija šalia + mv) -----------------------------
$SUDO mkdir -p "$DIR"
$SUDO cp "$TMP/$NAME/mircbsd" "$BIN.new"
$SUDO chmod 755 "$BIN.new"
$SUDO mv -f "$BIN.new" "$BIN"

if [ "$current" = "$VERSION" ]; then
    say "perįdiegta: $VERSION ($BIN)"
elif [ -n "$current" ] && [ "$current" != "v" ]; then
    say "atnaujinta: $current → $VERSION ($BIN)"
else
    say "įdiegta: $VERSION ($BIN)"
fi

# --- pirmas kartas: konfigūracijos pavyzdys ------------------------------
CFG_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/mircbsd"
if [ ! -e "$CFG_DIR/config" ] && [ -f "$TMP/$NAME/config.example" ]; then
    mkdir -p "$CFG_DIR"
    chmod 700 "$CFG_DIR"
    cp "$TMP/$NAME/config.example" "$CFG_DIR/config"
    chmod 600 "$CFG_DIR/config"
    say "sukurta konfigūracija: $CFG_DIR/config – pasitaisykite nick/server"
fi

# --- PATH ----------------------------------------------------------------
case ":$PATH:" in
    *":$DIR:"*) ;;
    *) say "pastaba: $DIR nėra PATH'e. Pridėkite į ~/.profile:"
       say "  export PATH=\"$DIR:\$PATH\"" ;;
esac

say "paleidimas: mircbsd   |   atnaujinimas: paleiskite šį skriptą dar kartą"
