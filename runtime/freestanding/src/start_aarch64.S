// kern freestanding startup for AArch64 (QEMU virt): set up the stack and
// the stack limit, then run the program; the return value halts the machine.
    .section .text._start, "ax"
    .globl _start
_start:
    ldr x0, =_kern_stack_top
    mov sp, x0
    ldr x0, =_kern_stack_bottom
    add x0, x0, #4096          // one guard page below the limit
    ldr x1, =kern_stack_limit
    str x0, [x1]
    bl kern_start
    bl kern_main
    bl kern_exit
1:  wfi
    b 1b

    .section .bss
    .balign 16
    .globl _kern_stack_bottom
_kern_stack_bottom:
    .space 65536
    .globl _kern_stack_top
_kern_stack_top:
