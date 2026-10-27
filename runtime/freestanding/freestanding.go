// Package freestanding embeds the kern freestanding runtime (PLAN.md 3.1):
// the C runtime, the per-architecture startup code and the linker scripts
// for the QEMU virt (AArch64) and q35 (x86-64) machines.
package freestanding

import _ "embed"

// RuntimeC is the freestanding runtime source.
//
//go:embed src/rt.c
var RuntimeC string

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
