package interp

// fionread is the ioctl that reports the bytes unread in a pipe.
const fionread = 0x541b

// ioctlGetTermios is the ioctl that reads a terminal's settings (TCGETS);
// it succeeds only on a terminal, which is how isatty asks.
const ioctlGetTermios = 0x5401
