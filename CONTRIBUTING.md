# Prisidėjimas prie mircBSD

Ačiū, kad norite padėti! / Thanks for contributing — English PRs and issues are welcome too.

## Kūrimo aplinka

- Rust 1.85+ (`rustup` rekomenduojamas), Python 3 (E2E testams)
- Debian arba FreeBSD (kiti Unix gali veikti, bet nėra testuojami)

```sh
cargo build
cargo test --locked
bash scripts/e2e/start.sh     # pty + netikras IRC serveris, žr. scripts/e2e/README.md
```

Prieš siųsdami PR:

```sh
cargo fmt --all
cargo clippy --locked --all-targets -- -D warnings
cargo test --locked
```

## Taisyklės

- **Kiekvienam klaidos taisymui — regresijos testas**, kuris su senu kodu
  krenta (patikrinkite laikinai atstatę seną kodą).
- Validatoriams — abu testai: „blogas atmetamas“ **ir** „teisingas priimamas“.
- Testuose tikrinkite **realų elgesį**, ne eilutę, kuri gali pasitaikyti ir
  klaidos pranešime.
- Testai niekada neturi liesti tikro `~/.config/mircbsd` — naudokite laikinus
  katalogus.
- Kliento užraktas (`parking_lot::Mutex` nėra re-entrant): pagalbinės
  funkcijos, kurios pačios ima užraktą (pvz. serverio / klaidos žinučių
  pridėjimas), negali būti kviečiamos jį jau laikant — tai deadlock.
- Serverio tekstas niekada nepatenka į terminalą be `sanitize`.
- Atnaujinkite `CHANGELOG.md` skyrių „Neišleista“.

## Commit žinutės

Trumpa santrauka (lietuviškai arba angliškai), po to — kas ir kodėl.

## Saugumas

Saugumo spragų nepraneškite viešai — žr. [SECURITY.md](SECURITY.md).
