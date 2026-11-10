package tests

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dshills/kern/compiler/backend/native"
)

// What a fixture cannot show of specs/runtime area M: a fault in a mapped
// file, which ends the program, and a scoped address on a real link,
// which not every machine has.

// ranEngine runs a program natively or in the interpreter, with
// arguments, and returns what it wrote and how it ended.
func ranEngine(t *testing.T, src string, interpreted bool, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var cmd *exec.Cmd
	if interpreted {
		file := filepath.Join(t.TempDir(), "t.kern")
		if werr := os.WriteFile(file, []byte(src), 0o644); werr != nil {
			t.Fatal(werr)
		}
		cmd = exec.Command(kernBinary(t), append([]string{"run", file, "--"}, args...)...)
	} else {
		cmd = exec.Command(buildProgram(t, src, native.Options{}), args...)
	}
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	return out.String(), errb.String(), err
}

// trapped reports whether a program ended as a trap ends one: the abort
// signal natively, status 134 interpreted.
func trapped(err error) bool {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return false
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	return ok && (ws.Signaled() && ws.Signal() == syscall.SIGABRT || ws.Exited() && ws.ExitStatus() == 134)
}

// A program that maps a file, cuts the file short and reads a byte of the
// mapping that the file no longer has.
const mappedThenCut = `module t
import "std/io"
import "std/mem"
func say(s: view[]u8) { print_str(s); println() }
func cut(dir: view[]u8) -> Result[u8, io.IoErr] {
    path := io.join(dir, "data")
    o := io.options()
    o.read = true
    o.write = true
    o.create = true
    f := io.open(path.as_view(), o)?
    big := Vec[u8].new()
    big.resize(262144usize, 7u8)
    f.write_all(big.as_view())?
    m := mem.map_file(f, false)?
    var v: u8 = 0u8
    with b := m.bytes() { v = b[200000usize] }
    print_str("the byte was "); print_u64(u64(v)); println()
    f.set_len(0u64)?
    io.remove_dir_all(dir)?
    say("cut")
    with b := m.bytes() { v = b[200000usize] }
    return Ok(v)
}
func main() -> i32 {
    match io.make_temp_dir("kern-cut-") {
        Ok(dir) => {
            match cut(dir.as_view()) {
                Ok(v) => { print_str("read a byte the file does not have: "); print_u64(u64(v)); println() }
                Err(e) => say("failed")
            }
        }
        Err(e) => say("no temporary directory")
    }
    return 0i32
}
`

// A program that writes, in unsafe code, through a mapping made for
// reading.
const mappedReadOnlyWrite = `module t
import "std/io"
import "std/mem"
func say(s: view[]u8) { print_str(s); println() }
contract { requires "nothing: the write is the fault this program is for" }
func write(dir: view[]u8) -> Result[(), io.IoErr] {
    path := io.join(dir, "data")
    io.write_file(path.as_view(), "read only")?
    f := io.open_read(path.as_view())?
    unsafe {
        r := mem.mem_map_file(f.raw_fd(), 9usize, false)
        if r < 0i64 { return Err(io.neg(r)) }
        p := ptr_from_addr[u8](usize(r))
        print_str("the first byte is "); print_u64(u64(ptr_load(p))); println()
        io.remove_dir_all(dir)?
        say("writing")
        ptr_store(p, 1u8)
    }
    return Ok(())
}
func main() -> i32 {
    match io.make_temp_dir("kern-ro-") {
        Ok(dir) => {
            match write(dir.as_view()) {
                Ok(u) => say("wrote through a mapping made for reading")
                Err(e) => say("failed")
            }
        }
        Err(e) => say("no temporary directory")
    }
    return 0i32
}
`

// A fault in mapped memory is a trap of kind unreachable in both engines
// (specs/runtime RM5): the interpreter maps the file as the runtime does,
// so the system faults the same accesses, and the interpreter survives
// the fault to report it.
func TestMappedFileFaultsAreTraps(t *testing.T) {
	for _, c := range []struct{ name, src, out string }{
		{"cut short", mappedThenCut, "the byte was 7\ncut\n"},
		{"written through a read-only mapping", mappedReadOnlyWrite, "the first byte is 114\nwriting\n"},
	} {
		for _, engine := range []string{"native", "interpreter"} {
			t.Run(c.name+"/"+engine, func(t *testing.T) {
				stdout, stderr, err := ranEngine(t, c.src, engine == "interpreter")
				if stdout != c.out {
					t.Errorf("the program wrote %q, want %q", stdout, c.out)
				}
				if !trapped(err) {
					t.Errorf("the program ended with %v, want a trap", err)
				}
				if !strings.HasPrefix(stderr, `{"trap":{"kind":"unreachable",`) || strings.Count(stderr, "\n") != 1 {
					t.Errorf("standard error is %q, want one trap record of kind unreachable", stderr)
				}
			})
		}
	}
}

// A program that binds a UDP socket to the scoped address it is given and
// sends itself a datagram.
const scopedDatagram = `module t
import "std/net"
import "std/io"
func say(s: view[]u8) { print_str(s); println() }
func run(text: view[]u8) -> Result[(), io.IoErr] {
    match net.parse_addr(text) {
        Some(a) => {
            u := net.UdpSocket.bind(a)?
            at := u.local_addr()?
            print_str("bound with scope "); print_u64(u64(at.scope)); println()
            n := u.send_to("over the link", at)?
            var buf: [32]u8 = zero()
            d := u.recv_from(buf)?
            print_str("received "); print_u64(u64(d.len)); print_str(" bytes from scope "); print_u64(u64(d.from.scope)); println()
            // without its scope the address names no link
            match u.send_to("nowhere", net.addr(at.ip, at.port)) {
                Ok(k) => say("sent without a scope")
                Err(e) => say("refused without a scope")
            }
        }
        None => say("not an address")
    }
    return Ok(())
}
func main() -> i32 {
    a := io.args()
    if a.len() < 2usize { return 2i32 }
    with text := a.at(1usize) {
        match run(text.as_view()) {
            Ok(u) => { }
            Err(e) => {
                match io.kind(e) {
                    AddrNotAvailable => say("the address is not available")
                    _ => say("failed")
                }
            }
        }
    }
    return 0i32
}
`

// linkLocal finds an IPv6 link-local address of this machine and the
// interface it is on.
func linkLocal() (net.IP, *net.Interface) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, nil
	}
	for i := range ifs {
		if ifs[i].Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := ifs[i].Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() == nil && n.IP.IsLinkLocalUnicast() {
				return n.IP, &ifs[i]
			}
		}
	}
	return nil, nil
}

// A link-local address is reached through its scope (specs/runtime RM2):
// a socket bound to one reports the scope, a datagram's sender carries
// it, and the scope may be written as the interface's name or its index.
func TestScopedAddressOnALink(t *testing.T) {
	ip, ifi := linkLocal()
	if ip == nil {
		t.Skip("this machine has no IPv6 link-local address")
	}
	index := strconv.Itoa(ifi.Index)
	want := "bound with scope " + index + "\nreceived 13 bytes from scope " + index + "\n"
	for _, zone := range []string{ifi.Name, index} {
		for _, engine := range []string{"native", "interpreter"} {
			t.Run(zone+"/"+engine, func(t *testing.T) {
				// an address the system has just given the interface is not
				// available until the system has checked that no neighbour
				// has it, which takes a second or two
				var stdout, stderr string
				var err error
				for try := 0; try < 20; try++ {
					stdout, stderr, err = ranEngine(t, scopedDatagram, engine == "interpreter", "["+ip.String()+"%"+zone+"]:0")
					if stdout != "the address is not available\n" {
						break
					}
					time.Sleep(250 * time.Millisecond)
				}
				if err != nil || stderr != "" {
					t.Fatalf("the program ended with %v and wrote %q to standard error", err, stderr)
				}
				// what the system does with a link-local address that has
				// no scope is its own choice; the program reports it, and
				// the two engines must agree, which the comparison below
				// of everything before that line leaves open
				if !strings.HasPrefix(stdout, want) {
					t.Errorf("the program wrote %q, want it to begin %q", stdout, want)
				}
			})
		}
	}
}
