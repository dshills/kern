#include <fcntl.h>
#include <stdint.h>
// c_cloexec_stdout marks descriptor 1 close-on-exec and reports whether the
// flag is now set.
int32_t c_cloexec_stdout(void) {
    if (fcntl(1, F_SETFD, FD_CLOEXEC) != 0) return 0;
    return (fcntl(1, F_GETFD) & FD_CLOEXEC) != 0;
}
