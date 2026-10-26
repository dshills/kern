// kern freestanding startup for x86-64 (QEMU q35, loaded as a multiboot or
// raw image by the user's loader): set up the stack and limit, run the
// program, halt with its result.
    .section .text._start, "ax"
    .globl _start
_start:
    lea _kern_stack_top(%rip), %rsp
    lea _kern_stack_bottom(%rip), %rax
    add $4096, %rax
    mov %rax, kern_stack_limit(%rip)
    call kern_start
    call kern_main
    mov %eax, %edi
    call kern_exit
1:  hlt
    jmp 1b

    .section .bss
    .balign 16
    .globl _kern_stack_bottom
_kern_stack_bottom:
    .space 65536
    .globl _kern_stack_top
_kern_stack_top:
