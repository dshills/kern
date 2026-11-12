#!/usr/bin/env python3
"""record.py: record what the system awk prints for every case.

Each case is cases/NNN-name.awk, with an optional NNN-name.in (its standard
input; input.txt otherwise) and an optional NNN-name.args. This script runs
every case through /usr/bin/awk exactly as tests/kawk_corpus_test.go runs
kawk, and rewrites cases/NNN-name.out (the standard output, byte for byte)
and status.txt (one line a case: name, exit status, and `host` for a case
whose record holds what this system's own commands print, `-` otherwise).

It runs only where the records can be made: on macOS, with the awk the
corpus was compared against (awk version 20200816). See README.md.

  record.py            record every case
  record.py --check    record nothing; report the cases whose record differs
"""
import os
import platform
import shutil
import signal
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
CASES = os.path.join(HERE, "cases")
AWK = os.environ.get("KAWK_RECORD_AWK", "/usr/bin/awk")
WANT_VERSION = "awk version 20200816"
LIMIT = 20  # seconds a case may take


def fail(msg):
    sys.stderr.write("record.py: " + msg + "\n")
    sys.exit(2)


def preflight():
    if platform.system() != "Darwin":
        fail("the records are made on macOS only (this is %s): the marked "
             "cases record what macOS's own commands print" % platform.system())
    try:
        v = subprocess.run([AWK, "-version"], stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=10).stdout.decode().strip()
    except OSError as e:
        fail("cannot run %s: %s" % (AWK, e))
    except subprocess.TimeoutExpired:
        fail("%s did not answer -version in 10 s" % AWK)
    if v != WANT_VERSION:
        fail("%s is %r, not %r" % (AWK, v, WANT_VERSION))


def blank_literals(src):
    """The program text with strings, regular expressions and comments
    blanked, so that what is left of a | is the pipe operator."""
    out = []
    i, n, prev = 0, len(src), ""
    while i < n:
        c = src[i]
        if c == "#":
            while i < n and src[i] != "\n":
                i += 1
            continue
        if c == '"':
            i += 1
            while i < n and src[i] != '"':
                if src[i] == "\\":
                    i += 1
                i += 1
            i += 1
            out.append("S")
            prev = "S"
            continue
        if c == "/" and (prev == "" or prev in "(,~!&|{};=:?\n"):
            j, inb = i + 1, False
            while j < n and (src[j] != "/" or inb) and src[j] != "\n":
                if src[j] == "\\":
                    j += 1
                elif src[j] == "[":
                    inb = True
                elif src[j] == "]":
                    inb = False
                j += 1
            if j < n and src[j] == "/":
                i = j + 1
                out.append("R")
                prev = "R"
                continue
        out.append(c)
        if not c.isspace() or c == "\n":
            prev = c
        i += 1
    return "".join(out)


def is_host(path):
    """A case is marked `host` when it starts a command (system, a pipe)
    or reads ENVIRON."""
    s = blank_literals(open(path, encoding="latin-1").read())
    compact = "".join(s.split())
    return "system(" in compact or "ENVIRON" in s or "|" in compact.replace("||", "")


def command(case, workdir):
    """The arguments after the program name, and the standard input, for a
    case run in workdir. @IN in an operand is the case's input, copied
    into the working directory as in.txt; @PROG2 is prog2.awk."""
    base = case[:-4]
    inp = base + ".in" if os.path.exists(base + ".in") else os.path.join(HERE, "input.txt")
    opts, opers = [], []
    if os.path.exists(base + ".args"):
        lines = open(base + ".args").read().split("\n")
        if lines and lines[-1] == "":
            lines.pop()
        if "--" in lines:
            k = lines.index("--")
            opts, opers = lines[:k], lines[k + 1:]
        else:
            opers = lines
    if any("@IN" in o for o in opers):
        shutil.copyfile(inp, os.path.join(workdir, "in.txt"))
    opers = [o.replace("@IN", "in.txt") for o in opers]
    opts = [o.replace("@PROG2", os.path.join(HERE, "prog2.awk")) for o in opts]
    return opts + ["-f", case] + opers, inp


def run(case):
    parent = tempfile.mkdtemp(prefix="kawkrec-")
    workdir = os.path.join(parent, "kawkcase")
    os.mkdir(workdir)
    try:
        args, inp = command(case, workdir)
        env = dict(os.environ, LC_ALL="C", KAWK_TEST="yes", TZ="UTC")
        env.pop("POSIXLY_CORRECT", None)
        with open(inp, "rb") as f:
            p = subprocess.Popen([AWK] + args, stdin=f, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, cwd=workdir, env=env, start_new_session=True)
            try:
                out, _ = p.communicate(timeout=LIMIT)
            except subprocess.TimeoutExpired:
                os.killpg(p.pid, signal.SIGKILL)
                try:
                    p.communicate(timeout=5)
                except subprocess.TimeoutExpired:
                    pass
                fail("%s did not finish in %d s; nothing more is recorded" % (os.path.basename(case), LIMIT))
        return out, p.returncode
    finally:
        shutil.rmtree(parent, ignore_errors=True)


def main():
    check = sys.argv[1:] == ["--check"]
    if sys.argv[1:] and not check:
        fail("usage: record.py [--check]")
    preflight()
    cases = sorted(os.path.join(CASES, f) for f in os.listdir(CASES) if f.endswith(".awk"))
    lines, changed = [], []
    for case in cases:
        name = os.path.basename(case)[:-4]
        out, status = run(case)
        if status < 0:
            fail("%s: awk was killed by signal %d" % (name, -status))
        line = "%s %d %s\n" % (name, status, "host" if is_host(case) else "-")
        lines.append(line)
        outfile = case[:-4] + ".out"
        old = open(outfile, "rb").read() if os.path.exists(outfile) else None
        if old != out:
            changed.append(name)
            if not check:
                with open(outfile, "wb") as f:
                    f.write(out)
    status_file = os.path.join(HERE, "status.txt")
    text = "".join(lines)
    old = open(status_file).read() if os.path.exists(status_file) else None
    if old != text:
        changed.append("status.txt")
        if not check:
            with open(status_file, "w") as f:
                f.write(text)
    hosts = sum(1 for l in lines if l.endswith(" host\n"))
    verb = "differ" if check else "written"
    print("%d cases, %d marked host, %d %s" % (len(cases), hosts, len(changed), verb))
    for name in changed[:40]:
        print("  " + name)
    sys.exit(1 if check and changed else 0)


main()
