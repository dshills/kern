/* QEMU q35 (x86-64): the Multiboot loader places the image at 1 MiB; the
   header section comes first so it lies within the first 8 KiB */
ENTRY(_start)
SECTIONS {
  . = 0x100000;
  .multiboot : { KEEP(*(.multiboot)) }
  .text : { *(.text._start) *(.text*) }
  .rodata : { *(.rodata*) }
  .data : { *(.data*) }
  .bss : { *(.bss*) *(COMMON) }
}
