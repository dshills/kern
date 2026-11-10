package interp

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"syscall"
	"time"
)

// std/net (specs/runtime area H): sockets, as the hosted runtime's
// rt_net.c does them. The runtime keeps every socket non-blocking and
// loops on poll with a deadline; here Go's network poller does the same
// work behind a read, write or accept deadline. A descriptor is a number
// of the interpreter's own, above every file's.
//
// An address crosses as a record of 19 bytes: the family (4 or 6), 16
// bytes of address (IPv4 in the first four), the port high byte first.

// sock is one open socket: exactly one of its fields is set.
type sock struct {
	ln  *net.TCPListener
	tcp *net.TCPConn
	udp *net.UDPConn
}

const (
	addrBytes = 19
	sockBase  = 1 << 20 // socket descriptors start here, clear of the files'
)

func init() {
	for k, v := range map[string]func(*Interp, []Value, callCtx) []Value{
		"net_tcp_listen":  netTCPListen,
		"net_udp_bind":    netUDPBind,
		"net_accept":      netAccept,
		"net_connect":     netConnect,
		"net_recv":        netRecv,
		"net_send":        netSend,
		"net_shutdown":    netShutdown,
		"net_set_nodelay": netSetNodelay,
		"net_addr":        netAddr,
		"net_send_to":     netSendTo,
		"net_recv_from":   netRecvFrom,
		"net_close":       netClose,
		"net_resolve":     netResolve,
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

// recAddr reads a record: the address, the port and the family (4 or 6),
// or false for bytes that are not one.
func recAddr(rec []byte) (net.IP, int, byte, bool) {
	if len(rec) < addrBytes {
		return nil, 0, 0, false
	}
	port := int(rec[17])<<8 | int(rec[18])
	switch rec[0] {
	case 4:
		return net.IP(append([]byte(nil), rec[1:5]...)), port, 4, true
	case 6:
		return net.IP(append([]byte(nil), rec[1:17]...)), port, 6, true
	}
	return nil, 0, 0, false
}

// addrRec is the record of an address: an IPv4 one whatever length Go
// holds it in (an IPv6 socket here is IPv6 only, so it never carries an
// IPv4 address in IPv6 form).
func addrRec(ip net.IP, port int) []byte {
	rec := make([]byte, addrBytes)
	if four := ip.To4(); four != nil {
		rec[0] = 4
		copy(rec[1:], four)
	} else {
		rec[0] = 6
		copy(rec[1:], ip.To16())
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
	ip, port, family, ok := recAddr(seqBytes(acc(a[0])))
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
	l, err := lc.Listen(context.Background(), network("tcp", family), (&net.TCPAddr{IP: ip, Port: port}).String())
	if err != nil {
		return i32Val(-int32(errnoOf(err)))
	}
	ln := l.(*net.TCPListener)
	if raw, rerr := ln.SyscallConn(); rerr == nil {
		_ = raw.Control(func(fd uintptr) { _ = syscall.Listen(int(fd), backlog) })
	}
	return i32Val(int32(it.addSock(&sock{ln: ln})))
}

func netUDPBind(it *Interp, a []Value, _ callCtx) []Value {
	ip, port, family, ok := recAddr(seqBytes(acc(a[0])))
	if !ok {
		return i32Val(-int32(syscall.EINVAL))
	}
	u, err := net.ListenUDP(network("udp", family), &net.UDPAddr{IP: ip, Port: port})
	if err != nil {
		return i32Val(-int32(errnoOf(err)))
	}
	return i32Val(int32(it.addSock(&sock{udp: u})))
}

// plain makes a connection behave as the runtime's does: Go turns
// TCP_NODELAY on for every connection, the system leaves it off.
func plain(c *net.TCPConn) *net.TCPConn {
	_ = c.SetNoDelay(false)
	return c
}

func netAccept(it *Interp, a []Value, _ callCtx) []Value {
	s := it.sockOf(a[0])
	if s == nil || s.ln == nil {
		return i64Val(-int64(syscall.EBADF))
	}
	_ = s.ln.SetDeadline(deadline(a[1], a[2]))
	c, err := s.ln.AcceptTCP()
	if err != nil {
		return i64Val(netErr(err))
	}
	return i64Val(it.addSock(&sock{tcp: plain(c)}))
}

func netConnect(it *Interp, a []Value, _ callCtx) []Value {
	ip, port, family, ok := recAddr(seqBytes(acc(a[0])))
	if !ok {
		return i64Val(-int64(syscall.EINVAL))
	}
	d := net.Dialer{KeepAlive: -1}
	if bool(a[2].(Bool)) {
		// a duration of zero gives up at once, as the runtime's attempt
		// that did not complete in its one call does
		d.Deadline = time.Now().Add(time.Duration(min(a[1].(Int).Bits, uint64(1<<62))))
	}
	c, err := d.Dial(network("tcp", family), (&net.TCPAddr{IP: ip, Port: port}).String())
	if err != nil {
		return i64Val(netErr(err))
	}
	return i64Val(it.addSock(&sock{tcp: plain(c.(*net.TCPConn))}))
}

func netRecv(it *Interp, a []Value, _ callCtx) []Value {
	s := it.sockOf(a[0])
	if s == nil || s.tcp == nil {
		return i64Val(-int64(syscall.EBADF))
	}
	buf := make([]byte, byteSeq(acc(a[1])).length())
	if len(buf) == 0 {
		return i64Val(0)
	}
	_ = s.tcp.SetReadDeadline(deadline(a[2], a[3]))
	n, err := s.tcp.Read(buf)
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
	if s == nil || s.tcp == nil {
		return i64Val(-int64(syscall.EBADF))
	}
	data := seqBytes(acc(a[1]))
	if len(data) == 0 {
		return i64Val(0)
	}
	_ = s.tcp.SetWriteDeadline(time.Time{})
	n, err := s.tcp.Write(data)
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
	if s == nil || s.tcp == nil {
		return i32Val(-int32(syscall.EBADF))
	}
	var err error
	switch int32(a[1].(Int).Bits) {
	case 0:
		err = s.tcp.CloseRead()
	case 1:
		err = s.tcp.CloseWrite()
	case 2:
		if err = s.tcp.CloseRead(); err == nil {
			err = s.tcp.CloseWrite()
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
	case s.tcp != nil && peer:
		addr = s.tcp.RemoteAddr()
	case s.tcp != nil:
		addr = s.tcp.LocalAddr()
	case s.udp != nil && !peer:
		addr = s.udp.LocalAddr()
	}
	switch x := addr.(type) {
	case *net.TCPAddr:
		putBytes(a[2], addrRec(x.IP, x.Port))
	case *net.UDPAddr:
		putBytes(a[2], addrRec(x.IP, x.Port))
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
	ip, port, _, ok := recAddr(seqBytes(acc(a[2])))
	if !ok {
		return i64Val(-int64(syscall.EINVAL))
	}
	_ = s.udp.SetWriteDeadline(time.Time{})
	n, err := s.udp.WriteToUDP(seqBytes(acc(a[1])), &net.UDPAddr{IP: ip, Port: port})
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
	putBytes(a[2], addrRec(from.IP, from.Port))
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
	case s.tcp != nil:
		err = s.tcp.Close()
	case s.udp != nil:
		err = s.udp.Close()
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
		recs = append(recs, addrRec(ia.IP, port)...)
	}
	if len(recs) <= byteSeq(acc(a[2])).length() {
		putBytes(a[2], recs)
	}
	return i64Val(int64(len(recs)))
}
