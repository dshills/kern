package interp

import (
	"bytes"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/dshills/kern/compiler/ir"
)

// KillChildren signals the run's unreaped children and never a reaped one,
// whose pid may belong to another process (specs/test-timeout-children R3).
// The reaped entry here carries this test process's own pid: signalling it
// would end the test.
func TestKillChildrenSkipsReaped(t *testing.T) {
	var out, errb bytes.Buffer
	it := New(&ir.Module{}, "t.kern", &out, &errb)
	pid, err := syscall.ForkExec("/bin/sleep", []string{"sleep", "60"}, &syscall.ProcAttr{Files: []uintptr{0, 1, 2}})
	if err != nil {
		t.Fatal(err)
	}
	it.sh.procs = map[int64]*procEntry{
		1: {pid: os.Getpid(), reaped: true},
		2: {pid: pid},
	}
	it.KillChildren()
	gone := false
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			gone = true
			break
		}
	}
	if !gone {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("the unreaped child %d still runs", pid)
	}
	if !it.sh.procs[2].reaped || !it.sh.procsKilled {
		t.Errorf("table after KillChildren: %+v, killed %v", it.sh.procs[2], it.sh.procsKilled)
	}
}
