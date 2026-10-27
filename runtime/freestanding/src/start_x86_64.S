// kern freestanding startup for x86-64 (QEMU q35, loaded by `-kernel` as a
// Multiboot 1 image). The loader enters 32-bit protected mode with paging
// off, so the stub identity-maps the first 1 GiB with 2 MiB pages, enables
// long mode and SSE (the loader leaves it off, and compiled code uses it for
// floating point and memory copies), loads a 64-bit GDT and jumps to the
// 64-bit entry, which sets up the stack and limit, runs the program and halts
// with its result.

    // Multiboot 1 header (must lie in the first 8 KiB of the image; the
    // linker script places this section first)
    .section .multiboot, "a"
    .balign 4
    .long 0x1BADB002
    .long 0
    .long -0x1BADB002

    .section .text._start, "ax"
    .code32
    .globl _start
_start:
    cli
    cld                             // Multiboot leaves DF unspecified; rep stosl and the ABI need it clear
    // clear the three page tables (12 KiB)
    movl $_kern_pml4, %edi
    xorl %eax, %eax
    movl $3072, %ecx
    rep stosl
    // page directory: 512 entries x 2 MiB, present | writable | page size
    movl $_kern_pd, %edi
    movl $0x83, %eax
    movl $512, %ecx
1:  movl %eax, (%edi)
    addl $0x200000, %eax
    addl $8, %edi
    loop 1b
    movl $_kern_pd, %eax
    orl $3, %eax
    movl %eax, _kern_pdpt
    movl $_kern_pdpt, %eax
    orl $3, %eax
    movl %eax, _kern_pml4
    movl $_kern_pml4, %eax
    movl %eax, %cr3
    movl %cr4, %eax
    orl $0x620, %eax                // PAE | OSFXSR | OSXMMEXCPT: compiled code uses SSE
    movl %eax, %cr4
    movl $0xC0000080, %ecx          // IA32_EFER
    rdmsr
    orl $0x100, %eax                // LME
    wrmsr
    movl %cr0, %eax
    andl $~0x0C, %eax               // clear EM and TS: no FPU emulation, no lazy switch
    orl $0x80000002, %eax           // PG | MP
    movl %eax, %cr0
    lgdt _kern_gdtr
    ljmpl $0x08, $_start64

    .code64
_start64:
    movw $0x10, %ax
    movw %ax, %ds
    movw %ax, %es
    movw %ax, %ss
    movw %ax, %fs
    movw %ax, %gs
    lea _kern_stack_top(%rip), %rsp
    lea _kern_stack_bottom(%rip), %rax
    add $4096, %rax                 // one guard page below the limit
    mov %rax, kern_stack_limit(%rip)
    call kern_start
    call kern_main
    mov %eax, %edi
    call kern_exit
1:  hlt
    jmp 1b

    .section .rodata
    .balign 8
_kern_gdt:
    .quad 0
    .quad 0x00AF9A000000FFFF        // 64-bit code
    .quad 0x00CF92000000FFFF        // data
_kern_gdtr:
    .word 23
    .long _kern_gdt

    .section .bss
    .balign 4096
_kern_pml4:
    .space 4096
_kern_pdpt:
    .space 4096
_kern_pd:
    .space 4096
    .balign 16
    .globl _kern_stack_bottom
_kern_stack_bottom:
    .space 65536
    .globl _kern_stack_top
_kern_stack_top:
