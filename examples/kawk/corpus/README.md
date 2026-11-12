# The kawk comparison corpus

Each case here is an AWK program that `kawk` must run exactly as the
system awk ran it. The awk is the one-true-awk that macOS ships
(`/usr/bin/awk`, "awk version 20200816"): `kawk` follows it where it and
POSIX part ways, so what it printed is the record.

`go test ./tests -run TestKawkCorpus` builds `kawk` natively at release and
runs every case, comparing the standard output and the exit status, byte
for byte, with the record. It needs no awk on the host
(`specs/std-regex/SPEC.md` R22).

## What is here

- `cases/NNN-name.awk`: the program.
- `cases/NNN-name.in`: its standard input, when it has one of its own;
  otherwise `input.txt`.
- `cases/NNN-name.args`: its arguments, one a line, when it has any. Lines
  before a line `--` are options, placed before `-f program`; lines after
  it are operands, placed after. With no `--` every line is an operand.
  In an operand `@IN` is the case's input, which is copied into the
  working directory as `in.txt`; in an option `@PROG2` is `prog2.awk`.
- `cases/NNN-name.out`: what the system awk printed.
- `status.txt`: one line a case, `name status mark`: the exit status awk
  gave, and `host` for a case that starts a command (`system`, a pipe) or
  reads `ENVIRON`, `-` for every other.
- `record.py`: makes the `.out` files and `status.txt`.

## How a case is run

In an empty working directory named `kawkcase` (one case asks for its
name), with `LC_ALL=C`, `TZ=UTC` and `KAWK_TEST=yes` in the environment,
its input on standard input, and at most 20 seconds, after which it and
everything it started are killed. The test and `record.py` do the same.

A `host` case's record holds what macOS's own commands print (`wc` pads
its numbers there, `sort` orders by that system's rules), so the test runs
those cases on macOS and skips them, counted, on every other system.

## Adding a case, and recording

Write `cases/NNN-name.awk` with the next number (and `.in`, `.args` as
needed), then:

    python3 examples/kawk/corpus/record.py

It runs only on macOS with that awk, and stops otherwise. A case must give
the same output every time: nothing from the clock, the process or the
order `for (k in a)` visits keys in unless one key is asked for.
`record.py --check` records nothing and lists the cases whose record
would change.

A case that `kawk` fails is a defect in `kawk`, or a place where the system
awk does something `kawk` was not told to copy. The record is never
changed to make a case pass.
