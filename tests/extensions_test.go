package tests

import (
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// A program that connects to a Unix-domain listener, sends it an open
// file, and reads the file it is sent back.
const descriptorExchange = `module t
import "std/net"
import "std/io"
func say(s: view[]u8) { print_str(s); println() }
func exchange(sock: view[]u8, file: view[]u8) -> Result[(), io.IoErr] {
    s := net.UnixStream.connect(sock)?
    c := s.peer_credentials()?
    print_str("peer "); print_u64(u64(c.user)); print_str(" "); print_u64(u64(c.group)); print_str(" "); print_u64(u64(c.process)); println()
    f := io.open_read(file)?
    n := s.send_file("k", f)?
    var buf: [64]u8 = zero()
    match s.recv_with(buf)? {
        WithDescriptor(k, d) => {
            g := d.into_file()?
            m := g.read(buf)?
            print_str("received a file holding ["); print_str(buf[0usize : m]); say("]")
        }
        Bytes(k) => say("no descriptor came")
    }
    return Ok(())
}
func main() -> i32 {
    a := io.args()
    if a.len() < 3usize { return 2i32 }
    sock := a.get(1usize).clone()
    file := a.get(2usize).clone()
    match exchange(sock.as_view(), file.as_view()) {
        Ok(u) => { }
        Err(e) => say("failed")
    }
    return 0i32
}
`

// Descriptors cross between processes (specs/runtime RN2), and a peer's
// credentials are the other process's (RN3): the test is the peer. It
// receives the file the program sends and reads it, sends one back for
// the program to read, and the program reports the test's user, group
// and process.
func TestDescriptorsBetweenProcesses(t *testing.T) {
	for _, engine := range []string{"native", "interpreter"} {
		t.Run(engine, func(t *testing.T) {
			dir, err := os.MkdirTemp("", "kfd")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = os.RemoveAll(dir) }()
			sockPath := filepath.Join(dir, "s")
			fromKern, fromTest := filepath.Join(dir, "k"), filepath.Join(dir, "t")
			if err := os.WriteFile(fromKern, []byte("from the program"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(fromTest, []byte("from the test"), 0o644); err != nil {
				t.Fatal(err)
			}
			l, err := net.ListenUnix("unix", &net.UnixAddr{Name: sockPath, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = l.Close() }()
			got := make(chan string, 1)
			go func() {
				c, aerr := l.AcceptUnix()
				if aerr != nil {
					got <- "accept: " + aerr.Error()
					return
				}
				defer func() { _ = c.Close() }()
				_ = c.SetDeadline(time.Now().Add(20 * time.Second))
				buf, oob := make([]byte, 8), make([]byte, syscall.CmsgSpace(4))
				_, oobn, _, _, rerr := c.ReadMsgUnix(buf, oob)
				if rerr != nil {
					got <- "read: " + rerr.Error()
					return
				}
				msgs, perr := syscall.ParseSocketControlMessage(oob[:oobn])
				if perr != nil || len(msgs) != 1 {
					got <- "no control message"
					return
				}
				fds, perr := syscall.ParseUnixRights(&msgs[0])
				if perr != nil || len(fds) != 1 {
					got <- "no descriptor"
					return
				}
				f := os.NewFile(uintptr(fds[0]), "received")
				content, _ := io.ReadAll(f)
				_ = f.Close()
				back, oerr := os.Open(fromTest)
				if oerr != nil {
					got <- "open: " + oerr.Error()
					return
				}
				defer func() { _ = back.Close() }()
				if _, _, werr := c.WriteMsgUnix([]byte("t"), syscall.UnixRights(int(back.Fd())), nil); werr != nil {
					got <- "write: " + werr.Error()
					return
				}
				got <- string(content)
			}()
			stdout, stderr, err := ranEngine(t, descriptorExchange, engine == "interpreter", sockPath, fromKern)
			if err != nil || stderr != "" {
				t.Fatalf("the program ended with %v and wrote %q to standard error", err, stderr)
			}
			select {
			case content := <-got:
				if content != "from the program" {
					t.Errorf("the test received a file holding %q, want the program's file", content)
				}
			case <-time.After(20 * time.Second):
				t.Fatal("the test's end never finished")
			}
			want := "peer " + strconv.Itoa(os.Getuid()) + " " + strconv.Itoa(os.Getgid()) + " " + strconv.Itoa(os.Getpid()) + "\nreceived a file holding [from the test]\n"
			if stdout != want {
				t.Errorf("the program wrote %q, want %q", stdout, want)
			}
		})
	}
}

// A program that uses names in the abstract namespace: a listener and a
// connection, and two datagram sockets.
const abstractNames = `module t
import "std/net"
import "std/io"
func say(s: view[]u8) { print_str(s); println() }
func named(tag: u8, base: view[]u8) -> Vec[u8] {
    name := Vec[u8].new()
    name.push(0u8)
    name.push(tag)
    name.extend(base)
    return name
}
func run(base: view[]u8) -> Result[(), io.IoErr] {
    ln := named(108u8, base)
    l := net.UnixListener.bind(ln.as_view())?
    c := net.UnixStream.connect(ln.as_view())?
    s := l.accept()?
    c.write_all("abstract")?
    var buf: [32]u8 = zero()
    n := s.read(buf)?
    print_str("a connection by an abstract name carries ["); print_str(buf[0usize : n]); say("]")
    match net.UnixListener.bind(ln.as_view()) {
        Ok(x) => say("the name was bound twice")
        Err(e) => {
            match io.kind(e) {
                AddrInUse => say("the name is in use while its socket is open")
                _ => say("another error")
            }
        }
    }
    drop(s)
    drop(l)
    again := net.UnixListener.bind(ln.as_view())?
    say("and free once it is closed")
    an := named(97u8, base)
    bn := named(98u8, base)
    a := net.UnixDatagram.bind(an.as_view())?
    b := net.UnixDatagram.bind(bn.as_view())?
    k := a.send_to("datagram", bn.as_view())?
    m := b.recv_from(buf)?
    var same: bool = m.from.len() == an.len()
    if same {
        for i in 0usize..an.len() {
            if m.from.get(i) != an.get(i) { same = false }
        }
    }
    print_str("a datagram's sender is its abstract name: "); print_bool(same); println()
    return Ok(())
}
func refused(r: own Result[net.UnixListener, io.IoErr]) {
    match r {
        Ok(l) => say("accepted")
        Err(e) => {
            match io.kind(e) {
                InvalidInput => say("InvalidInput")
                Unsupported => say("Unsupported")
                _ => say("another error")
            }
        }
    }
}
func main() -> i32 {
    a := io.args()
    if a.len() < 2usize { return 2i32 }
    with base := a.at(1usize) {
        match run(base.as_view()) {
            Ok(u) => { }
            Err(e) => {
                match io.kind(e) {
                    Unsupported => say("abstract names are not supported here")
                    _ => say("failed")
                }
            }
        }
    }
    print_str("a name of no bytes: "); refused(net.UnixListener.bind("\x00"))
    print_str("a name holding a zero byte: "); refused(net.UnixListener.bind("\x00a\x00b"))
    return 0i32
}
`

// Names in Linux's abstract namespace (specs/runtime RN4): on Linux they
// work in both engines, for streams and datagrams, and a name is free
// again when its socket is closed; on macOS every such call is
// Unsupported in both engines. A malformed name is InvalidInput on both.
func TestAbstractSocketNames(t *testing.T) {
	want := "abstract names are not supported here\n"
	if runtime.GOOS == "linux" {
		want = "a connection by an abstract name carries [abstract]\n" +
			"the name is in use while its socket is open\n" +
			"and free once it is closed\n" +
			"a datagram's sender is its abstract name: true\n"
	}
	want += "a name of no bytes: InvalidInput\na name holding a zero byte: InvalidInput\n"
	for _, engine := range []string{"native", "interpreter"} {
		t.Run(engine, func(t *testing.T) {
			base := "kern-test-" + strconv.Itoa(os.Getpid()) + "-" + engine
			stdout, stderr, err := ranEngine(t, abstractNames, engine == "interpreter", base)
			if err != nil || stderr != "" {
				t.Fatalf("the program ended with %v and wrote %q to standard error", err, stderr)
			}
			if stdout != want {
				t.Errorf("the program wrote %q, want %q", stdout, want)
			}
		})
	}
}
