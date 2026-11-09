# Runtime work: status

The ledger of `specs/runtime/SPEC.md` (gate 10). One row per requirement:
the commit that met it, and any deviation from the spec's text with its
reason. Updated at the end of every phase.

| Requirement | Phase | Commit | Deviations |
|---|---|---|---|
| RA1 source layout | 1 | c45f745 |  |
| RA2 warning-clean | 1 | c45f745 |  |
| RA3 ABI conformance | 1 | c45f745 | On Linux the second architecture is checked only when its system headers are installed (CI runs both natively) |
| RA4 ABI version | 1 | c45f745 |  |
| RA5 sanitized runs | 1, final | c45f745 (mode, test, fixes; complete at gate 8) | The list of known reports is empty: RC1's leak has no fixture yet |
| RA6 static analysis | 1 | c45f745 |  |
| RB1 configuration | 2 | | |
| RB2 standard output policy | 2 | | |
| RB3 robust writes | 2 | | |
| RB4 terminal query | 2 | | |
| RB5 exit | 2 | | |
| RC1 detached threads | 3 | | |
| RC2 stack size | 3 | | |
| RC3 spawn robustness | 3 | | |
| RC4 queries | 3 | | |
| RE1 line and column | 4 | | |
| RE2 backtrace | 4 | | |
| RE3 parity | 4 | | |
| RD1 park | 5 | | |
| RD2 timed wait | 5 | | |
| RD3 try lock | 5 | | |
| RD4 conditional assertions | 5 | | |
| RD5 std/sync | 5 | | |
| RF1 checked allocator | 6 | | |
| RF2 allocation report | 6 | | |
| RF3 page allocator | 6 | | |
| RG1 error kinds | 7 | | |
| RG2 File | 7 | | |
| RG3 metadata and paths | 7 | | |
| RG4 readers and writers | 7 | | |
| RH1 addresses | 8 | | |
| RH2 TCP | 8 | | |
| RH3 UDP | 8 | | |
| RH4 rules | 8 | | |
| RI1 std/signal | 9 | | |
| RI2 std/rand | 9 | | |
| RJ1 board interface | 10 | | |
| RJ2 allocator checks | 10 | | |
| RK1 runtime reference | 11 | | |
| RK2 other documents | 11 | | |
| R-P1 performance | 11 | | |

## Found along the way

| Finding | Commit |
|---|---|
| Assignment through an access, to a field or to an element did not destroy the previous value (Rule 5.1.9) | 41b5593 |
| A temporary used as a method receiver was never destroyed (Rule 5.7.2) | 41b5593 |
| `memcpy` from a null pointer for empty sequences in the hosted runtime | c45f745 |
| Small integer arguments passed without the extension the C side assumes on x86-64 and Apple arm64 | c45f745 |
