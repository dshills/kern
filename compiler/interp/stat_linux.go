package interp

import "syscall"

// statMtime is the modification time of a stat result.
func statMtime(st *syscall.Stat_t) (sec, nsec int64) { return int64(st.Mtim.Sec), int64(st.Mtim.Nsec) }
