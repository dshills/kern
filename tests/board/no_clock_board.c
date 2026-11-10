// A board file that leaves kern_board_now_ns out: an image built with it
// does not link (specs/runtime RJ1).
#include "kern_board.h"
void kern_board_init(void) {}
void kern_board_putc(char c) { (void)c; }
_Noreturn void kern_board_poweroff(int code) { (void)code; for (;;) {} }
