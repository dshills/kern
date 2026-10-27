# Freestanding fixtures (PLAN.md 3.1)

Every fixture here carries `//! env: freestanding`. The harness runs it in the
interpreter's freestanding emulation (`kern run --env freestanding`): the
console device at `0x0900_0000` is mapped to standard output, there is no
default allocator (`SystemAllocator.new` traps with kind `explicit`), a trap
record goes to the console, and the run ends with the halt line
`{"halt": {"code": n}}`. The `.out` file is the whole console transcript; a
trap record's `file` is the path the harness loads the fixture by, relative
to `tests/` (`freestanding/stack.kern`), and QEMU must print the same.

Native: `kern build --env freestanding -o image file.kern` compiles the
program, `runtime/freestanding/src/rt.c` and the startup code for
`aarch64-unknown-none-elf` (default; `--target=x86_64-unknown-none-elf` for
the 16550 console at port `0x3F8`). With `ld.lld` on the PATH the result is
a linked image: on AArch64 an ELF laid out by
`runtime/freestanding/src/virt.ld`; on x86-64 a Multiboot 1 image laid out
by `q35.ld` and rewritten to the 32-bit ELF class with `llvm-objcopy`, which
QEMU's Multiboot loader requires (`start_x86_64.S` carries the header and
the protected-mode stub that identity-maps the first 1 GiB and enters long
mode). Without `ld.lld` the result is an archive of the three objects to
link with the matching script:

```
ld.lld -nostdlib -static -T runtime/freestanding/src/virt.ld -o image image.a
```

The harness builds every fixture for both targets and runs each image when
`ld.lld` and that target's emulator are installed. AArch64 (the guest powers
off through PSCI `SYSTEM_OFF`, so QEMU exits 0):

```
qemu-system-aarch64 -M virt -cpu cortex-a72 -serial stdio -display none -no-reboot -kernel image
```

x86-64 (`q35`; the guest writes its halt code to `isa-debug-exit`, so QEMU
exits with `(code << 1) | 1`, of which the process status keeps the low
eight bits: 1 for code 0, 13 for code 134; the harness checks that status
against the transcript's halt line):

```
qemu-system-x86_64 -M q35 -serial stdio -display none -no-reboot -device isa-debug-exit,iobase=0xf4,iosize=0x04 -kernel image
```

The startup code enables SSE (x86-64) and FP/SIMD at EL1 (AArch64) before
entering compiled code; `float.kern` checks that on both targets.

A fixture that drives one machine's device by address carries
`//! target: aarch64` (or `x86_64`) and is built and run for that target
only; `mmio.kern` writes the PL011 data register, which q35 does not have.

Budget: 60 s to the first console byte, 300 s total. `--unsafe-no-stack-check`
drops the prologue stack-limit check (stack overflow then becomes a
trusted-boundary obligation, spec §6).
