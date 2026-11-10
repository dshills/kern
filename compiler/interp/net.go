package interp

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"net"
	"os"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// std/net (specs/runtime area H): sockets, as the hosted runtime's
// rt_net.c does them. The runtime keeps every socket non-blocking and
// loops on poll with a deadline; here Go's network poller does the same
// work behind a read, write or accept deadline. A descriptor is a number
// of the interpreter's own, above every file's.
//
// An address crosses as a record of 23 bytes: the family (4 or 6), 16
// bytes of address (IPv4 in the first four), the port high byte first,
// an IPv6 address's scope (an interface's index) high byte first.

// sock is one open socket: a listener, a stream or a datagram socket.
// tcp is set beside conn for a TCP stream, which alone has TCP's options.
type sock struct {
	ln   listener
	conn stream
	tcp  *net.TCPConn
	udp  *net.UDPConn
	ud   *net.UnixConn // a Unix-domain datagram socket
	poll *poller       // a readiness queue
	// raw is a descriptor another program sent, not yet made one of the
	// above (std/net's Descriptor); it is the system's own number
	raw    int
	hasRaw bool
}

// poller is a readiness queue: the system's (kqueue or epoll), and the
// token of each descriptor watched, which the system's event does not
// carry here.
type poller struct {
	fd     int
	mu     sync.Mutex
	tokens map[int]uint64
}

// readyFD is one ready descriptor of the system's, with the runtime's
// flags: 1 readable, 2 writable, 4 the peer closed, 8 an error pending.
type readyFD struct {
	fd    int
	flags uint64
}

// osFD is the system's descriptor of a socket. It stays the socket's
// while the socket is open.
func (s *sock) osFD() (int, error) {
	if s.hasRaw {
		return s.raw, nil
	}
	fd := -1
	err := s.control(func(d int) error { fd = d; return nil })
	return fd, err
}

// control runs f on the socket's descriptor, for the options Go's types
// do not carry.
func (s *sock) control(f func(fd int) error) error {
	var c any
	switch {
	case s.ln != nil:
		c = s.ln
	case s.conn != nil:
		c = s.conn
	case s.udp != nil:
		c = s.udp
	case s.ud != nil:
		c = s.ud
	}
	sc, ok := c.(syscall.Conn)
	if !ok {
		return syscall.EBADF
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return err
	}
	var ferr error
	if err := raw.Control(func(fd uintptr) { ferr = f(int(fd)) }); err != nil {
		return err
	}
	return ferr
}

// listener is a TCP or a Unix-domain listener, and stream a connection
// of either: what the two families share is all std/net asks of them.
type listener interface {
	Accept() (net.Conn, error)
	SetDeadline(time.Time) error
	Addr() net.Addr
	Close() error
}

type stream interface {
	net.Conn
	CloseRead() error
	CloseWrite() error
}

// streamSock is the socket of a connection.
func streamSock(c net.Conn) *sock {
	if t, ok := c.(*net.TCPConn); ok {
		_ = t.SetNoDelay(false) // Go turns TCP_NODELAY on for every connection, the system leaves it off
		return &sock{conn: t, tcp: t}
	}
	return &sock{conn: c.(stream)}
}

const (
	addrBytes = 23
	sockBase  = 1 << 20 // socket descriptors start here, clear of the files'
)

func init() {
	for k, v := range map[string]func(*Interp, []Value, callCtx) []Value{
		"net_tcp_listen":     netTCPListen,
		"net_udp_bind":       netUDPBind,
		"net_accept":         netAccept,
		"net_connect":        netConnect,
		"net_unix_listen":    netUnixListen,
		"net_unix_connect":   netUnixConnect,
		"net_unix_pair":      netUnixPair,
		"net_recv":           netRecv,
		"net_send":           netSend,
		"net_shutdown":       netShutdown,
		"net_set_nodelay":    netSetNodelay,
		"net_set_opt":        netSetOpt,
		"net_get_opt":        netGetOpt,
		"net_if_index":       netIfIndex,
		"net_if_name":        netIfName,
		"net_unix_dgram":     netUnixDgram,
		"net_unix_send_to":   netUnixSendTo,
		"net_unix_recv_from": netUnixRecvFrom,
		"net_poller_new":     netPollerNew,
		"net_poller_set":     netPollerSet,
		"net_poller_wait":    netPollerWait,
		"net_send_fd":        netSendFD,
		"net_recv_fd":        netRecvFD,
		"net_fd_kind":        netFDKind,
		"net_fd_adopt":       netFDAdopt,
		"net_peer_cred":      netPeerCred,
		"net_addr":           netAddr,
		"net_send_to":        netSendTo,
		"net_recv_from":      netRecvFrom,
		"net_close":          netClose,
		"net_resolve":        netResolve,
	} {
		intrinsics[k] = v
	}
}

// addSock records a socket and returns its descriptor.
func (it *Interp) addSock(s *sock) int64 {
	it.sh.mu.Lock()
	defer it.sh.mu.Unlock()
	if it.sh.socks == nil {
		it.sh.socks = map[int64]*sock{}
	}
	fd := sockBase + it.sh.nextSock
	it.sh.nextSock++
	it.sh.socks[fd] = s
	return fd
}

func (it *Interp) sockOf(v Value) *sock {
	it.sh.mu.Lock()
	defer it.sh.mu.Unlock()
	return it.sh.socks[signedOf(v.(Int))]
}

// recAddr reads a record: the address, the port, the scope as Go names
// one (an interface's index in decimal, empty for none) and the family (4
// or 6), or false for bytes that are not one.
func recAddr(rec []byte) (net.IP, int, string, byte, bool) {
	if len(rec) < addrBytes {
		return nil, 0, "", 0, false
	}
	port := int(rec[17])<<8 | int(rec[18])
	switch rec[0] {
	case 4:
		return net.IP(append([]byte(nil), rec[1:5]...)), port, "", 4, true
	case 6:
		zone := ""
		if scope := binary.BigEndian.Uint32(rec[19:23]); scope != 0 {
			zone = strconv.FormatUint(uint64(scope), 10)
		}
		return net.IP(append([]byte(nil), rec[1:17]...)), port, zone, 6, true
	}
	return nil, 0, "", 0, false
}

// scopeOf is the index of the interface a zone names: Go gives a zone as
// the interface's name when it knows one, else as the index in decimal.
func scopeOf(zone string) uint32 {
	if zone == "" {
		return 0
	}
	if ifi, err := net.InterfaceByName(zone); err == nil {
		return uint32(ifi.Index)
	}
	n, _ := strconv.ParseUint(zone, 10, 32)
	return uint32(n)
}

// addrRec is the record of an address: an IPv4 one whatever length Go
// holds it in (an IPv6 socket here is IPv6 only, so it never carries an
// IPv4 address in IPv6 form).
func addrRec(ip net.IP, port int, zone string) []byte {
	rec := make([]byte, addrBytes)
	if four := ip.To4(); four != nil {
		rec[0] = 4
		copy(rec[1:], four)
	} else {
		rec[0] = 6
		copy(rec[1:], ip.To16())
		binary.BigEndian.PutUint32(rec[19:], scopeOf(zone))
	}
	rec[17], rec[18] = byte(port>>8), byte(port)
	return rec
}

func putBytes(dst Value, b []byte) {
	seq := byteSeq(acc(dst))
	for i := 0; i < len(b) && i < seq.length(); i++ {
		seq.at(i).set(mkInt(tyU8, uint64(b[i])))
	}
}

// deadline is the time a timed call gives up, or no time at all. A
// duration under a millisecond is a millisecond: the poller refuses a
// call whose deadline has already passed without trying it, where the
// runtime tries once before it looks at the clock.
func deadline(ns Value, timed Value) time.Time {
	if !bool(timed.(Bool)) {
		return time.Time{}
	}
	d := time.Duration(min(ns.(Int).Bits, uint64(1<<62)))
	if d < time.Millisecond {
		d = time.Millisecond
	}
	return time.Now().Add(d)
}

// netErr is the value a failed call returns: the timed-out value for a
// deadline that passed, else -errno.
func netErr(err error) int64 {
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return timedOut
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return timedOut
	}
	return -int64(errnoOf(err))
}

func network(base string, family byte) string {
	if family == 6 {
		return base + "6"
	}
	return base + "4"
}

func netTCPListen(it *Interp, a []Value, _ callCtx) []Value {
	ip, port, zone, family, ok := recAddr(seqBytes(acc(a[0])))
	if !ok {
		return i32Val(-int32(syscall.EINVAL))
	}
	backlog := int(int32(a[1].(Int).Bits))
	if backlog < 0 {
		backlog = 0
	}
	// Go listens with the system's largest queue and with address reuse
	// on; the queue the program asked for is set by listening again
	lc := net.ListenConfig{KeepAlive: -1}
	l, err := lc.Listen(context.Background(), network("tcp", family), (&net.TCPAddr{IP: ip, Port: port, Zone: zone}).String())
	if err != nil {
		return i32Val(-int32(errnoOf(err)))
	}
	ln := l.(*net.TCPListener)
	if raw, rerr := ln.SyscallConn(); rerr == nil {
		_ = raw.Control(func(fd uintptr) { _ = syscall.Listen(int(fd), backlog) })
	}
	return i32Val(int32(it.addSock(&sock{ln: tcpListener{ln}})))
}

func netUDPBind(it *Interp, a []Value, _ callCtx) []Value {
	ip, port, zone, family, ok := recAddr(seqBytes(acc(a[0])))
	if !ok {
		return i32Val(-int32(syscall.EINVAL))
	}
	u, err := net.ListenUDP(network("udp", family), &net.UDPAddr{IP: ip, Port: port, Zone: zone})
	if err != nil {
		return i32Val(-int32(errnoOf(err)))
	}
	s := &sock{udp: u}
	// Go permits broadcast on every datagram socket, the system on none
	_ = s.control(func(fd int) error { return syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_BROADCAST, 0) })
	return i32Val(int32(it.addSock(s)))
}

func netAccept(it *Interp, a []Value, _ callCtx) []Value {
	s := it.sockOf(a[0])
	if s == nil || s.ln == nil {
		return i64Val(-int64(syscall.EBADF))
	}
	_ = s.ln.SetDeadline(deadline(a[1], a[2]))
	c, err := s.ln.Accept()
	if err != nil {
		return i64Val(netErr(err))
	}
	return i64Val(it.addSock(streamSock(c)))
}

func netConnect(it *Interp, a []Value, _ callCtx) []Value {
	ip, port, zone, family, ok := recAddr(seqBytes(acc(a[0])))
	if !ok {
		return i64Val(-int64(syscall.EINVAL))
	}
	d := net.Dialer{KeepAlive: -1}
	if bool(a[2].(Bool)) {
		// a duration of zero gives up at once, as the runtime's attempt
		// that did not complete in its one call does
		d.Deadline = time.Now().Add(time.Duration(min(a[1].(Int).Bits, uint64(1<<62))))
	}
	c, err := d.Dial(network("tcp", family), (&net.TCPAddr{IP: ip, Port: port, Zone: zone}).String())
	if err != nil {
		return i64Val(netErr(err))
	}
	return i64Val(it.addSock(streamSock(c)))
}

// unixPath checks a Unix-domain path as the runtime's kern_net_unix_sa
// does and returns it as the network package names it: 0, or the error
// number. A path whose first byte is zero is a name in the abstract
// namespace, where the system has one.
func unixPath(b []byte) (string, syscall.Errno) {
	if len(b) == 0 {
		return "", syscall.EINVAL
	}
	rest := b
	if b[0] == 0 {
		if len(b) < 2 {
			return "", syscall.EINVAL
		}
		rest = b[1:]
	}
	for _, c := range rest {
		if c == 0 {
			return "", syscall.EINVAL
		}
	}
	if len(b) >= 104 {
		return "", syscall.ENAMETOOLONG
	}
	if b[0] == 0 {
		return abstractName(string(rest))
	}
	return fileName(string(b)), 0
}

func netUnixListen(it *Interp, a []Value, _ callCtx) []Value {
	path, e := unixPath(seqBytes(acc(a[0])))
	if e != 0 {
		return i32Val(-int32(e))
	}
	backlog := int(int32(a[1].(Int).Bits))
	if backlog < 0 {
		backlog = 0
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return i32Val(-int32(errnoOf(err)))
	}
	l.SetUnlinkOnClose(false) // the path is the program's to remove, as natively
	if raw, rerr := l.SyscallConn(); rerr == nil {
		_ = raw.Control(func(fd uintptr) { _ = syscall.Listen(int(fd), backlog) })
	}
	return i32Val(int32(it.addSock(&sock{ln: unixListener{l}})))
}

// unixListener gives a Unix-domain listener the listener interface's
// Accept.
type unixListener struct{ *net.UnixListener }

func (u unixListener) Accept() (net.Conn, error) { return u.AcceptUnix() }

// tcpListener does the same for TCP.
type tcpListener struct{ *net.TCPListener }

func (t tcpListener) Accept() (net.Conn, error) { return t.AcceptTCP() }

func netUnixConnect(it *Interp, a []Value, _ callCtx) []Value {
	path, e := unixPath(seqBytes(acc(a[0])))
	if e != 0 {
		return i64Val(-int64(e))
	}
	d := net.Dialer{}
	timed := bool(a[2].(Bool))
	end := time.Now().Add(time.Duration(min(a[1].(Int).Bits, uint64(1<<62))))
	for {
		c, err := d.Dial("unix", path)
		if err == nil {
			return i64Val(it.addSock(streamSock(c)))
		}
		// a listener whose queue is full: the call is made again, as the
		// runtime makes it, until the time has passed
		if errnoOf(err) != int(syscall.EAGAIN) {
			return i64Val(netErr(err))
		}
		if timed && !time.Now().Before(end) {
			return i64Val(timedOut)
		}
		time.Sleep(time.Millisecond)
	}
}

func netUnixPair(it *Interp, a []Value, _ callCtx) []Value {
	seq := byteSeq(acc(a[0]))
	if seq.length() < 2 {
		return i32Val(-int32(syscall.EINVAL))
	}
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return i32Val(-int32(errnoOf(err)))
	}
	for i, fd := range fds {
		syscall.CloseOnExec(fd)
		f := os.NewFile(uintptr(fd), "socketpair")
		c, cerr := net.FileConn(f) // a duplicate that the poller owns
		_ = f.Close()
		if cerr != nil {
			if i == 0 {
				_ = syscall.Close(fds[1])
			}
			return i32Val(-int32(errnoOf(cerr)))
		}
		seq.at(i).set(mkInt(tyI32, uint64(uint32(it.addSock(streamSock(c))))))
	}
	return i32Val(0)
}

func netRecv(it *Interp, a []Value, _ callCtx) []Value {
	s := it.sockOf(a[0])
	if s == nil || s.conn == nil {
		return i64Val(-int64(syscall.EBADF))
	}
	buf := make([]byte, byteSeq(acc(a[1])).length())
	if len(buf) == 0 {
		return i64Val(0)
	}
	_ = s.conn.SetReadDeadline(deadline(a[2], a[3]))
	n, err := s.conn.Read(buf)
	if n > 0 {
		putBytes(a[1], buf[:n])
		return i64Val(int64(n))
	}
	if err == nil || errors.Is(err, io.EOF) {
		return i64Val(0)
	}
	return i64Val(netErr(err))
}

func netSend(it *Interp, a []Value, _ callCtx) []Value {
	s := it.sockOf(a[0])
	if s == nil || s.conn == nil {
		return i64Val(-int64(syscall.EBADF))
	}
	data := seqBytes(acc(a[1]))
	if len(data) == 0 {
		return i64Val(0)
	}
	_ = s.conn.SetWriteDeadline(time.Time{})
	n, err := s.conn.Write(data)
	if n > 0 {
		return i64Val(int64(n))
	}
	if err != nil {
		return i64Val(netErr(err))
	}
	return i64Val(0)
}

func netShutdown(it *Interp, a []Value, _ callCtx) []Value {
	s := it.sockOf(a[0])
	if s == nil || s.conn == nil {
		return i32Val(-int32(syscall.EBADF))
	}
	var err error
	switch int32(a[1].(Int).Bits) {
	case 0:
		err = s.conn.CloseRead()
	case 1:
		err = s.conn.CloseWrite()
	case 2:
		if err = s.conn.CloseRead(); err == nil {
			err = s.conn.CloseWrite()
		}
	default:
		return i32Val(-int32(syscall.EINVAL))
	}
	if err != nil {
		return i32Val(-int32(errnoOf(err)))
	}
	return i32Val(0)
}

func netSetNodelay(it *Interp, a []Value, _ callCtx) []Value {
	s := it.sockOf(a[0])
	if s == nil || s.tcp == nil {
		return i32Val(-int32(syscall.EBADF))
	}
	if err := s.tcp.SetNoDelay(bool(a[1].(Bool))); err != nil {
		return i32Val(-int32(errnoOf(err)))
	}
	return i32Val(0)
}

func netAddr(it *Interp, a []Value, _ callCtx) []Value {
	s := it.sockOf(a[0])
	if s == nil {
		return i32Val(-int32(syscall.EBADF))
	}
	if byteSeq(acc(a[2])).length() < addrBytes {
		return i32Val(-int32(syscall.EINVAL))
	}
	peer := bool(a[1].(Bool))
	var addr net.Addr
	switch {
	case s.ln != nil && !peer:
		addr = s.ln.Addr()
	case s.conn != nil && peer:
		addr = s.conn.RemoteAddr()
	case s.conn != nil:
		addr = s.conn.LocalAddr()
	case s.udp != nil && !peer:
		addr = s.udp.LocalAddr()
	}
	switch x := addr.(type) {
	case *net.TCPAddr:
		putBytes(a[2], addrRec(x.IP, x.Port, x.Zone))
	case *net.UDPAddr:
		putBytes(a[2], addrRec(x.IP, x.Port, x.Zone))
	default:
		return i32Val(-int32(syscall.ENOTCONN)) // a listener or an unconnected socket has no peer
	}
	return i32Val(0)
}

func netSendTo(it *Interp, a []Value, _ callCtx) []Value {
	s := it.sockOf(a[0])
	if s == nil || s.udp == nil {
		return i64Val(-int64(syscall.EBADF))
	}
	ip, port, zone, _, ok := recAddr(seqBytes(acc(a[2])))
	if !ok {
		return i64Val(-int64(syscall.EINVAL))
	}
	_ = s.udp.SetWriteDeadline(time.Time{})
	n, err := s.udp.WriteToUDP(seqBytes(acc(a[1])), &net.UDPAddr{IP: ip, Port: port, Zone: zone})
	if err != nil {
		return i64Val(netErr(err))
	}
	return i64Val(int64(n))
}

func netRecvFrom(it *Interp, a []Value, _ callCtx) []Value {
	s := it.sockOf(a[0])
	if s == nil || s.udp == nil {
		return i64Val(-int64(syscall.EBADF))
	}
	if byteSeq(acc(a[2])).length() < addrBytes {
		return i64Val(-int64(syscall.EINVAL))
	}
	buf := make([]byte, byteSeq(acc(a[1])).length())
	_ = s.udp.SetReadDeadline(deadline(a[3], a[4]))
	n, from, err := s.udp.ReadFromUDP(buf)
	if err != nil && from == nil {
		return i64Val(netErr(err))
	}
	putBytes(a[1], buf[:n])
	putBytes(a[2], addrRec(from.IP, from.Port, from.Zone))
	return i64Val(int64(n))
}

func netClose(it *Interp, a []Value, _ callCtx) []Value {
	fd := signedOf(a[0].(Int))
	it.sh.mu.Lock()
	s := it.sh.socks[fd]
	delete(it.sh.socks, fd)
	it.sh.mu.Unlock()
	var err error
	switch {
	case s == nil:
		return i32Val(-int32(syscall.EBADF))
	case s.ln != nil:
		err = s.ln.Close()
	case s.conn != nil:
		err = s.conn.Close()
	case s.udp != nil:
		err = s.udp.Close()
	case s.ud != nil:
		err = s.ud.Close()
	case s.poll != nil:
		err = syscall.Close(s.poll.fd)
	case s.hasRaw:
		err = syscall.Close(s.raw)
	}
	if err != nil {
		return i32Val(-int32(errnoOf(err)))
	}
	return i32Val(0)
}

// netResolve asks the system resolver, as getaddrinfo does natively. Its
// failures have no errno: an unknown name is -ENOENT, a failure that may
// pass -EAGAIN, anything else -EIO.
func netResolve(_ *Interp, a []Value, _ callCtx) []Value {
	host := seqBytes(acc(a[0]))
	if len(host) == 0 || len(host) >= 256 {
		return i64Val(-int64(syscall.EINVAL))
	}
	for _, c := range host {
		if c == 0 {
			return i64Val(-int64(syscall.EINVAL))
		}
	}
	port := int(a[1].(Int).Bits)
	addrs, err := net.DefaultResolver.LookupIPAddr(context.Background(), string(host))
	if err != nil {
		var de *net.DNSError
		switch {
		case errors.As(err, &de) && de.IsNotFound:
			return i64Val(-int64(syscall.ENOENT))
		case errors.As(err, &de) && (de.IsTimeout || de.IsTemporary):
			return i64Val(-int64(syscall.EAGAIN))
		}
		return i64Val(-int64(syscall.EIO))
	}
	var recs []byte
	for _, ia := range addrs {
		recs = append(recs, addrRec(ia.IP, port, ia.Zone)...)
	}
	if len(recs) <= byteSeq(acc(a[2])).length() {
		putBytes(a[2], recs)
	}
	return i64Val(int64(len(recs)))
}

// sockOpt is the system's level and name of an option std/net numbers,
// as the runtime's kern_net_opt: 1 keep-alive, 2 the receive buffer, 3
// the send buffer, 4 broadcast, 5 the hop limit (of the socket's
// family), 6 linger, 7 no-delay.
func sockOpt(fd int, opt int32) (level, name int, err error) {
	switch opt {
	case 1:
		return syscall.SOL_SOCKET, syscall.SO_KEEPALIVE, nil
	case 2:
		return syscall.SOL_SOCKET, syscall.SO_RCVBUF, nil
	case 3:
		return syscall.SOL_SOCKET, syscall.SO_SNDBUF, nil
	case 4:
		return syscall.SOL_SOCKET, syscall.SO_BROADCAST, nil
	case 5:
		sa, serr := syscall.Getsockname(fd)
		if serr != nil {
			return 0, 0, serr
		}
		if _, six := sa.(*syscall.SockaddrInet6); six {
			return syscall.IPPROTO_IPV6, syscall.IPV6_UNICAST_HOPS, nil
		}
		return syscall.IPPROTO_IP, syscall.IP_TTL, nil
	case 6:
		return syscall.SOL_SOCKET, soLinger, nil
	case 7:
		return syscall.IPPROTO_TCP, syscall.TCP_NODELAY, nil
	}
	return 0, 0, syscall.EINVAL
}

func netSetOpt(it *Interp, a []Value, _ callCtx) []Value {
	s := it.sockOf(a[0])
	if s == nil {
		return i32Val(-int32(syscall.EBADF))
	}
	opt, value := int32(a[1].(Int).Bits), signedOf(a[2].(Int))
	err := s.control(func(fd int) error {
		level, name, err := sockOpt(fd, opt)
		if err != nil {
			return err
		}
		if opt == 6 {
			l := syscall.Linger{}
			if value >= 0 {
				l.Onoff, l.Linger = 1, int32(min(value, math.MaxInt32))
			}
			return syscall.SetsockoptLinger(fd, level, name, &l)
		}
		if value < math.MinInt32 || value > math.MaxInt32 {
			return syscall.EINVAL
		}
		return syscall.SetsockoptInt(fd, level, name, int(value))
	})
	if err != nil {
		return i32Val(-int32(errnoOf(err)))
	}
	return i32Val(0)
}

// netGetOpt is an option's value, or -errno - 1: linger that is off is
// the value -1.
func netGetOpt(it *Interp, a []Value, _ callCtx) []Value {
	s := it.sockOf(a[0])
	if s == nil {
		return i64Val(-int64(syscall.EBADF) - 1)
	}
	opt := int32(a[1].(Int).Bits)
	var value int64
	err := s.control(func(fd int) error {
		level, name, err := sockOpt(fd, opt)
		if err != nil {
			return err
		}
		if opt == 6 {
			// package syscall reads no linger structure; it reads a
			// longer one, whose first eight bytes the system fills with
			// linger's two numbers
			m, lerr := syscall.GetsockoptIPv6Mreq(fd, level, name)
			if lerr != nil {
				return lerr
			}
			value = -1
			if binary.NativeEndian.Uint32(m.Multiaddr[0:4]) != 0 {
				value = int64(int32(binary.NativeEndian.Uint32(m.Multiaddr[4:8])))
			}
			return nil
		}
		v, gerr := syscall.GetsockoptInt(fd, level, name)
		value = int64(v)
		return gerr
	})
	if err != nil {
		return i64Val(-int64(errnoOf(err)) - 1)
	}
	return i64Val(value)
}

func netIfIndex(_ *Interp, a []Value, _ callCtx) []Value {
	name := seqBytes(acc(a[0]))
	var index uint32
	if len(name) > 0 && len(name) < 16 {
		if ifi, err := net.InterfaceByName(string(name)); err == nil {
			index = uint32(ifi.Index)
		}
	}
	return []Value{mkInt(tyU32, uint64(index))}
}

func netIfName(_ *Interp, a []Value, _ callCtx) []Value {
	index := a[0].(Int).Bits
	if index == 0 || index > math.MaxInt32 {
		return i64Val(0)
	}
	ifi, err := net.InterfaceByIndex(int(index))
	if err != nil || len(ifi.Name) > byteSeq(acc(a[1])).length() {
		return i64Val(0)
	}
	putBytes(a[1], []byte(ifi.Name))
	return i64Val(int64(len(ifi.Name)))
}

// netUnixDgram makes a Unix-domain datagram socket, bound to the path
// when there is one.
func netUnixDgram(it *Interp, a []Value, _ callCtx) []Value {
	b := seqBytes(acc(a[0]))
	if len(b) == 0 {
		// no address at all: Go's own way to an unbound socket would have
		// Linux bind it to a name of the system's choosing
		fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_DGRAM, 0)
		if err != nil {
			return i32Val(-int32(errnoOf(err)))
		}
		syscall.CloseOnExec(fd)
		f := os.NewFile(uintptr(fd), "unixgram")
		pc, perr := net.FilePacketConn(f) // a duplicate that the poller owns
		_ = f.Close()
		if perr != nil {
			return i32Val(-int32(errnoOf(perr)))
		}
		return i32Val(int32(it.addSock(&sock{ud: pc.(*net.UnixConn)})))
	}
	path, e := unixPath(b)
	if e != 0 {
		return i32Val(-int32(e))
	}
	c, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		return i32Val(-int32(errnoOf(err)))
	}
	return i32Val(int32(it.addSock(&sock{ud: c})))
}

func netUnixSendTo(it *Interp, a []Value, _ callCtx) []Value {
	s := it.sockOf(a[0])
	if s == nil || s.ud == nil {
		return i64Val(-int64(syscall.EBADF))
	}
	path, e := unixPath(seqBytes(acc(a[2])))
	if e != 0 {
		return i64Val(-int64(e))
	}
	data := seqBytes(acc(a[1]))
	for {
		// a receiver whose queue is full: the send is made again each
		// millisecond, as the runtime makes it
		_ = s.ud.SetWriteDeadline(time.Now().Add(20 * time.Millisecond))
		n, err := s.ud.WriteToUnix(data, &net.UnixAddr{Name: path, Net: "unixgram"})
		if err == nil {
			return i64Val(int64(n))
		}
		if netErr(err) != timedOut && errnoOf(err) != int(syscall.ENOBUFS) && errnoOf(err) != int(syscall.EAGAIN) {
			return i64Val(netErr(err))
		}
		time.Sleep(time.Millisecond)
	}
}

// netUnixRecvFrom receives one datagram and writes its sender's path
// into from: its length in the first byte, then its bytes.
func netUnixRecvFrom(it *Interp, a []Value, _ callCtx) []Value {
	s := it.sockOf(a[0])
	if s == nil || s.ud == nil {
		return i64Val(-int64(syscall.EBADF))
	}
	if byteSeq(acc(a[2])).length() < 105 {
		return i64Val(-int64(syscall.EINVAL))
	}
	want := byteSeq(acc(a[1])).length()
	buf := make([]byte, max(want, 1)) // a datagram is taken off the queue even into no bytes
	_ = s.ud.SetReadDeadline(deadline(a[3], a[4]))
	// a descriptor a sender attached is closed, as the runtime closes it
	oob := make([]byte, rightsRoom)
	n, oobn, _, from, err := s.ud.ReadMsgUnix(buf, oob)
	takeRights(oob[:max(oobn, 0)], false)
	if err != nil {
		return i64Val(netErr(err))
	}
	n = min(n, want)
	putBytes(a[1], buf[:n])
	var path []byte
	if from != nil {
		path = senderPath(from.Name)
	}
	if len(path) > 104 {
		path = path[:104]
	}
	putBytes(a[2], append([]byte{byte(len(path))}, path...))
	return i64Val(int64(n))
}

// ---- readiness (specs/runtime RN1) ----

func netPollerNew(it *Interp, _ []Value, _ callCtx) []Value {
	fd, err := pollerNew()
	if err != nil {
		return i32Val(-int32(errnoOf(err)))
	}
	return i32Val(int32(it.addSock(&sock{poll: &poller{fd: fd, tokens: map[int]uint64{}}})))
}

func netPollerSet(it *Interp, a []Value, _ callCtx) []Value {
	p, s := it.sockOf(a[0]), it.sockOf(a[1])
	interest := int(int32(a[3].(Int).Bits))
	if p == nil || p.poll == nil || s == nil {
		return i32Val(-int32(syscall.EBADF))
	}
	if interest < 0 || interest > 3 {
		return i32Val(-int32(syscall.EINVAL))
	}
	fd, err := s.osFD()
	if err == nil {
		err = pollerSet(p.poll.fd, fd, interest)
	}
	if err != nil {
		return i32Val(-int32(errnoOf(err)))
	}
	p.poll.mu.Lock()
	if interest == 0 {
		delete(p.poll.tokens, fd)
	} else {
		p.poll.tokens[fd] = a[2].(Int).Bits
	}
	p.poll.mu.Unlock()
	return i32Val(0)
}

func netPollerWait(it *Interp, a []Value, _ callCtx) []Value {
	p := it.sockOf(a[0])
	if p == nil || p.poll == nil {
		return i64Val(-int64(syscall.EBADF))
	}
	out := seqOf(acc(a[1]))
	if out.length() < 2 {
		return i64Val(-int64(syscall.EINVAL))
	}
	max := min(out.length()/2, 128)
	timed := bool(a[3].(Bool))
	total := time.Duration(min(a[2].(Int).Bits, uint64(1<<62)))
	start := time.Now()
	for {
		left := max64(total-time.Since(start), 0) // no time left: one look
		ready, err := pollerWait(p.poll.fd, max, left, timed)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return i64Val(-int64(errnoOf(err)))
		}
		if len(ready) == 0 {
			if timed && time.Since(start) >= total {
				return i64Val(0)
			}
			continue // the wait ended early: look at the clock again
		}
		p.poll.mu.Lock()
		for i, r := range ready {
			out.at(2 * i).set(mkInt(tyU64, p.poll.tokens[r.fd]))
			out.at(2*i + 1).set(mkInt(tyU64, r.flags))
		}
		p.poll.mu.Unlock()
		return i64Val(int64(len(ready)))
	}
}

func max64(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

// ---- descriptors and credentials over a Unix-domain stream (RN2, RN3) ----

// passFD is the system's descriptor of a file or socket of the program's.
func (it *Interp) passFD(d int64) (int, bool) {
	if d < sockBase {
		if f := it.openFile(d); f != nil {
			return int(f.Fd()), true
		}
		return -1, false
	}
	it.sh.mu.Lock()
	s := it.sh.socks[d]
	it.sh.mu.Unlock()
	if s == nil {
		return -1, false
	}
	fd, err := s.osFD()
	return fd, err == nil
}

func netSendFD(it *Interp, a []Value, _ callCtx) []Value {
	s := it.sockOf(a[0])
	data := seqBytes(acc(a[1]))
	if len(data) == 0 {
		return i64Val(-int64(syscall.EINVAL))
	}
	if s == nil || s.conn == nil {
		return i64Val(-int64(syscall.EBADF))
	}
	uc, ok := s.conn.(*net.UnixConn)
	if !ok {
		return i64Val(-int64(syscall.ENOTSOCK))
	}
	fd, ok := it.passFD(signedOf(a[2].(Int)))
	if !ok {
		return i64Val(-int64(syscall.EBADF))
	}
	_ = uc.SetWriteDeadline(time.Time{})
	n, _, err := uc.WriteMsgUnix(data, syscall.UnixRights(fd), nil)
	if err != nil && n == 0 {
		return i64Val(netErr(err))
	}
	return i64Val(int64(n))
}

// rightsRoom is room for the most descriptors one message can carry, as
// the runtime's receives give (rt_net.c says why: a system that opens
// every descriptor sent and reports only those that fit would otherwise
// leave some open with nothing to name them).
var rightsRoom = syscall.CmsgSpace(4 * 256)

// takeRights walks the control data of a received message: with keep, the
// first descriptor is returned, close-on-exec; every other is closed. It
// returns -1 when there is none to return.
func takeRights(oob []byte, keep bool) int {
	first := -1
	msgs, err := syscall.ParseSocketControlMessage(oob)
	if err != nil {
		return first
	}
	for i := range msgs {
		fds, rerr := syscall.ParseUnixRights(&msgs[i])
		if rerr != nil {
			continue
		}
		for _, fd := range fds {
			if keep && first < 0 {
				syscall.CloseOnExec(fd)
				first = fd
			} else {
				_ = syscall.Close(fd)
			}
		}
	}
	return first
}

func netRecvFD(it *Interp, a []Value, _ callCtx) []Value {
	s := it.sockOf(a[0])
	want := byteSeq(acc(a[1])).length()
	got := seqOf(acc(a[2]))
	if want == 0 || got.length() < 1 {
		return i64Val(-int64(syscall.EINVAL))
	}
	if s == nil || s.conn == nil {
		return i64Val(-int64(syscall.EBADF))
	}
	uc, ok := s.conn.(*net.UnixConn)
	if !ok {
		return i64Val(-int64(syscall.ENOTSOCK))
	}
	got.at(0).set(mkInt(tyI32, uint64(uint32(0xFFFFFFFF))))
	buf := make([]byte, want)
	oob := make([]byte, rightsRoom)
	_ = uc.SetReadDeadline(deadline(a[3], a[4]))
	n, oobn, _, _, err := uc.ReadMsgUnix(buf, oob)
	n, oobn = max(n, 0), max(oobn, 0) // a call that failed reports -1
	if err != nil && n == 0 && oobn == 0 {
		if errors.Is(err, io.EOF) {
			return i64Val(0)
		}
		return i64Val(netErr(err))
	}
	first := takeRights(oob[:oobn], true)
	if first >= 0 {
		d := it.addSock(&sock{raw: first, hasRaw: true})
		got.at(0).set(mkInt(tyI32, uint64(uint32(d))))
	}
	putBytes(a[1], buf[:n])
	return i64Val(int64(n))
}

// fdKind says what a descriptor of the system's is, as the runtime's
// kern_net_fd_kind.
func fdKind(fd int) (int32, error) {
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return 0, err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFSOCK {
		return 1, nil
	}
	typ, err := syscall.GetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_TYPE)
	if err != nil {
		return 0, err
	}
	listening := 0
	if typ == syscall.SOCK_STREAM && sockListening(fd) {
		listening = 1
	}
	sa, err := syscall.Getsockname(fd)
	if err != nil {
		return 0, err
	}
	inet, local := false, false
	switch sa.(type) {
	case *syscall.SockaddrInet4, *syscall.SockaddrInet6:
		inet = true
	case *syscall.SockaddrUnix:
		local = true
	}
	switch {
	case typ == syscall.SOCK_STREAM && inet && listening != 0:
		return 3, nil
	case typ == syscall.SOCK_STREAM && inet:
		return 2, nil
	case typ == syscall.SOCK_STREAM && local && listening != 0:
		return 6, nil
	case typ == syscall.SOCK_STREAM && local:
		return 5, nil
	case typ == syscall.SOCK_DGRAM && inet:
		return 4, nil
	case typ == syscall.SOCK_DGRAM && local:
		return 7, nil
	}
	return 0, nil
}

func netFDKind(it *Interp, a []Value, _ callCtx) []Value {
	s := it.sockOf(a[0])
	if s == nil || !s.hasRaw {
		return i32Val(-int32(syscall.EBADF))
	}
	k, err := fdKind(s.raw)
	if err != nil {
		return i32Val(-int32(errnoOf(err)))
	}
	return i32Val(k)
}

// netFDAdopt makes a received descriptor a file or a socket of the
// interpreter's and returns its descriptor there; on failure it stays
// what it was, for the program to close.
func netFDAdopt(it *Interp, a []Value, _ callCtx) []Value {
	d := signedOf(a[0].(Int))
	kind := int32(a[1].(Int).Bits)
	s := it.sockOf(a[0])
	if s == nil || !s.hasRaw {
		return i32Val(-int32(syscall.EBADF))
	}
	k, err := fdKind(s.raw)
	if err != nil {
		return i32Val(-int32(errnoOf(err)))
	}
	if k != kind || kind == 0 {
		return i32Val(-int32(syscall.EINVAL))
	}
	f := os.NewFile(uintptr(s.raw), "received")
	// should the network package refuse the descriptor, the file made of
	// it must not close it a second time behind the program's back: it is
	// closed now, and the entry is left with nothing to close
	failed := func(err error) []Value {
		_ = f.Close()
		it.sh.mu.Lock()
		s.hasRaw = false
		it.sh.mu.Unlock()
		return i32Val(-int32(errnoOf(err)))
	}
	if kind == 1 {
		it.sh.mu.Lock()
		delete(it.sh.socks, d)
		if it.sh.files == nil {
			it.sh.files = map[int64]*os.File{}
		}
		fd := int64(3)
		for it.sh.files[fd] != nil {
			fd++
		}
		it.sh.files[fd] = f
		it.sh.mu.Unlock()
		return i32Val(int32(fd))
	}
	// the network package takes a duplicate, which its poller owns
	var made *sock
	switch kind {
	case 2, 5:
		c, cerr := net.FileConn(f)
		if cerr != nil {
			return failed(cerr)
		}
		made = &sock{conn: c.(stream)}
		if t, ok := c.(*net.TCPConn); ok {
			made.tcp = t
		}
	case 3, 6:
		l, lerr := net.FileListener(f)
		if lerr != nil {
			return failed(lerr)
		}
		if t, ok := l.(*net.TCPListener); ok {
			made = &sock{ln: tcpListener{t}}
		} else {
			u := l.(*net.UnixListener)
			u.SetUnlinkOnClose(false)
			made = &sock{ln: unixListener{u}}
		}
	case 4, 7:
		pc, perr := net.FilePacketConn(f)
		if perr != nil {
			return failed(perr)
		}
		if u, ok := pc.(*net.UDPConn); ok {
			made = &sock{udp: u}
		} else {
			made = &sock{ud: pc.(*net.UnixConn)}
		}
	}
	_ = f.Close()
	it.sh.mu.Lock()
	it.sh.socks[d] = made
	it.sh.mu.Unlock()
	return i32Val(int32(d))
}

func netPeerCred(it *Interp, a []Value, _ callCtx) []Value {
	s := it.sockOf(a[0])
	out := seqOf(acc(a[1]))
	if out.length() < 3 {
		return i32Val(-int32(syscall.EINVAL))
	}
	if s == nil || s.conn == nil {
		return i32Val(-int32(syscall.EBADF))
	}
	var uid, gid, pid uint32
	err := s.control(func(fd int) error {
		var cerr error
		uid, gid, pid, cerr = peerCred(fd)
		return cerr
	})
	if err != nil {
		return i32Val(-int32(errnoOf(err)))
	}
	for i, v := range [...]uint32{uid, gid, pid} {
		out.at(i).set(mkInt(tyU32, uint64(v)))
	}
	return i32Val(0)
}
