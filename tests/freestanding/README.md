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
an ELF image laid out by `runtime/freestanding/src/virt.ld`; otherwise it is
an archive of the three objects to link with that script:

```
ld.lld -nostdlib -static -T runtime/freestanding/src/virt.ld -o image image.a
```

Run under QEMU (the harness does this when `ld.lld` and `qemu-system-aarch64`
are installed; the guest powers off through PSCI `SYSTEM_OFF`):

```
qemu-system-aarch64 -M virt -cpu cortex-a72 -serial stdio -display none -no-reboot -kernel image
```

x86-64 (`q35`, `isa-debug-exit` makes QEMU exit with `(code << 1) | 1`):

```
qemu-system-x86_64 -M q35 -serial stdio -display none -no-reboot -device isa-debug-exit,iobase=0xf4,iosize=0x04 -kernel image
```

Budget: 60 s to the first console byte, 300 s total. `--unsafe-no-stack-check`
drops the prologue stack-limit check (stack overflow then becomes a
trusted-boundary obligation, spec §6).
