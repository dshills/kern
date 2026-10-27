// kern freestanding startup for AArch64 (QEMU virt): enable FP/SIMD at EL1
// (CPACR_EL1.FPEN is clear at reset and compiled code uses the vector
// registers), set up the stack and the stack limit, then run the program;
// the return value halts the machine.
    .section .text._start, "ax"
    .globl _start
_start:
    mov x0, #(3 << 20)             // CPACR_EL1.FPEN = 0b11: no trap at EL0/EL1
    msr cpacr_el1, x0
    isb
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
