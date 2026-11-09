package interp

// fionread is the ioctl that reports the bytes unread in a pipe.
const fionread = 0x4004667f

// ioctlGetTermios is the ioctl that reads a terminal's settings (TIOCGETA);
// it succeeds only on a terminal, which is how isatty asks.
const ioctlGetTermios = 0x40487413
