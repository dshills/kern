// Package freestanding embeds the kern freestanding runtime (PLAN.md 3.1):
// the C runtime, the board interface with its two default boards, the
// per-architecture startup code and the linker scripts for the QEMU virt
// (AArch64) and q35 (x86-64) machines.
package freestanding

import _ "embed"

// RuntimeC is the freestanding runtime source.
//
//go:embed src/rt.c
var RuntimeC string

// Header is kern_rt_fs.h, the entries' prototypes, which RuntimeC and
// F64TextC include.
//
//go:embed src/kern_rt_fs.h
var Header string

// F64TextC is the runtime's floating-point text entries
// (specs/freestanding-float), compiled as their own object and linked only
// into an image that calls them, so other images are unchanged.
//
//go:embed src/f64text.c
var F64TextC string

// BoardHeader is kern_board.h, the four functions a board file defines
// (specs/runtime RJ1); it is on the include path of every board file.
//
//go:embed src/kern_board.h
var BoardHeader string

// BoardVirtC and BoardQ35C are the default board files: the QEMU virt
// machine for AArch64 targets and the QEMU q35 machine for x86-64 ones.
//
//go:embed src/board_virt.c
var BoardVirtC string

//go:embed src/board_q35.c
var BoardQ35C string

// StartAArch64 is the AArch64 startup assembly.
//
//go:embed src/start_aarch64.S
var StartAArch64 string

// StartX8664 is the x86-64 startup assembly.
//
//go:embed src/start_x86_64.S
var StartX8664 string

// VirtLinkerScript lays an image out for the QEMU virt machine.
//
//go:embed src/virt.ld
var VirtLinkerScript string

// Q35LinkerScript lays an image out for the QEMU q35 machine (x86-64).
//
//go:embed src/q35.ld
var Q35LinkerScript string
