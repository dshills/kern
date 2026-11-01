package testrun

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dshills/kern/compiler/backend/native"
)

// spawnAndHang starts /bin/sleep 60, writes its pid to the file PIDFILE
// (a file write is unbuffered; native standard output is flushed only at
// exit, which a killed run never reaches), and loops.
const spawnAndHang = `module t
import "std/io"
import "std/process"
import "std/str"
func start() -> Result[(), io.IoErr] {
    cmd := process.Command.new("/bin/sleep")
    cmd.arg("60")
    ch := cmd.spawn()?
    s := str.String.new()
    s.push_str("pid ")
    s.push_u64(u64(ch.id()))
    s.push(10u8)
    io.write_file("PIDFILE", s.as_view())?
    var i: u64 = 0u64
    while true { i = wrapping_add(i, 1u64) }
    return Ok(())
}
func test_hang() {
    r := start()
}
`

// A timed-out run's children do not outlive it, in either engine
// (specs/test-timeout-children R1, R2).
func TestTimeoutKillsChildren(t *testing.T) {
	configs := []Config{{"interp", "debug"}}
	if native.Available() {
		configs = append(configs, Config{"native", "debug"})
	}
	for _, c := range configs {
		pidFile := filepath.Join(t.TempDir(), "pid")
		ss := srcs(map[string]string{
			"a.kern":      "module t\nfunc main() -> i32 { return 0i32 }\n",
			"a_test.kern": strings.Replace(spawnAndHang, "PIDFILE", pidFile, 1),
		})
		res, ds, err := Execute(ss, Options{Configs: []Config{c}, Timeout: time.Second})
		if err != nil || len(ds) > 0 {
			t.Fatalf("%v: ds=%v err=%v", c, ds, err)
		}
		if len(res.Tests) != 1 || len(res.Tests[0].Runs) != 1 || res.Tests[0].Runs[0].Outcome != "timeout" {
			t.Errorf("%v: %+v", c, res.Tests)
			continue
		}
		b, _ := os.ReadFile(pidFile)
		pid := 0
		if f := strings.Fields(string(b)); len(f) == 2 && f[0] == "pid" {
			pid, _ = strconv.Atoi(f[1])
		}
		if pid <= 0 {
			t.Errorf("%v: no child pid in %q", c, b)
			continue
		}
		gone := false
		for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
				gone = true
				break
			}
		}
		if !gone {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Errorf("%v: child %d still runs after the timeout", c, pid)
		}
	}
}
