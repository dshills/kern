/* QEMU virt (AArch64): RAM at 0x40000000; -kernel loads a raw/ELF image there */
ENTRY(_start)
SECTIONS {
  . = 0x40080000;
  .text : { *(.text._start) *(.text*) }
  .rodata : { *(.rodata*) }
  .data : { *(.data*) }
  .bss : { *(.bss*) *(COMMON) }
}
