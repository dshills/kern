// kern hosted runtime: std/net, sockets on the loopback and beyond
// (specs/runtime area H).
// Included by kern_rt.c; see kern_rt.h for the entries.
//
// Every socket is non-blocking inside the runtime, and every operation
// that can wait is a deadline loop: make the call; when it would block,
// poll for readiness with the time that is left; when a signal interrupts
// either, go round again with the time recomputed; when the time has
// passed, return KERN_TIMED_OUT. A call without a duration has no
// deadline. A call therefore never blocks past its duration on readiness
// that went stale, and no socket has a mode to restore.
//
// Sockets are close-on-exec and numbered above 2, as files are. A write
// to a connection the peer closed is an error, never a SIGPIPE:
// MSG_NOSIGNAL on Linux, SO_NOSIGPIPE on macOS.
//
// An address crosses the interface as 23 bytes: the family (4 or 6), 16
// bytes of address (an IPv4 address in the first four), the port, high
// byte first, and an IPv6 address's scope (the index of the interface a
// link-local address belongs to, 0 for none), high byte first.

#define KERN_ADDR_BYTES 23

// kern_net_to_sa fills a socket address from a record: its length, or 0
// for a record that is not one.
static socklen_t kern_net_to_sa(const uint8_t *rec, int64_t n, struct sockaddr_storage *ss) {
    if (n < KERN_ADDR_BYTES) return 0;
    memset(ss, 0, sizeof *ss);
    uint16_t port = (uint16_t)((uint16_t)rec[17] << 8 | rec[18]);
    if (rec[0] == 4) {
        struct sockaddr_in *a = (struct sockaddr_in *)ss;
        a->sin_family = AF_INET;
        a->sin_port = htons(port);
        memcpy(&a->sin_addr, rec + 1, 4);
        return sizeof *a;
    }
    if (rec[0] == 6) {
        struct sockaddr_in6 *a = (struct sockaddr_in6 *)ss;
        a->sin6_family = AF_INET6;
        a->sin6_port = htons(port);
        memcpy(&a->sin6_addr, rec + 1, 16);
        a->sin6_scope_id = (uint32_t)rec[19] << 24 | (uint32_t)rec[20] << 16 | (uint32_t)rec[21] << 8 | rec[22];
        return sizeof *a;
    }
    return 0;
}
// kern_net_from_sa writes the record of a socket address: 0, or
// -EAFNOSUPPORT for a family that is neither IPv4 nor IPv6.
static int kern_net_from_sa(const struct sockaddr_storage *ss, uint8_t *rec) {
    memset(rec, 0, KERN_ADDR_BYTES);
    if (ss->ss_family == AF_INET) {
        const struct sockaddr_in *a = (const struct sockaddr_in *)ss;
        uint16_t port = ntohs(a->sin_port);
        rec[0] = 4;
        memcpy(rec + 1, &a->sin_addr, 4);
        rec[17] = (uint8_t)(port >> 8);
        rec[18] = (uint8_t)port;
        return 0;
    }
    if (ss->ss_family == AF_INET6) {
        const struct sockaddr_in6 *a = (const struct sockaddr_in6 *)ss;
        uint16_t port = ntohs(a->sin6_port);
        rec[0] = 6;
        memcpy(rec + 1, &a->sin6_addr, 16);
        rec[17] = (uint8_t)(port >> 8);
        rec[18] = (uint8_t)port;
        rec[19] = (uint8_t)(a->sin6_scope_id >> 24);
        rec[20] = (uint8_t)(a->sin6_scope_id >> 16);
        rec[21] = (uint8_t)(a->sin6_scope_id >> 8);
        rec[22] = (uint8_t)a->sin6_scope_id;
        return 0;
    }
    return -EAFNOSUPPORT;
}
// kern_net_ready makes a new socket the runtime's kind: non-blocking,
// close-on-exec, above 2, and silent about a closed peer. It returns the
// descriptor (a new number when the socket was below 3) or -errno, having
// closed the socket.
static int kern_net_ready(int fd) {
    if (fd < 3) {
        int d = fcntl(fd, F_DUPFD_CLOEXEC, 3);
        int e = errno;
        close(fd);
        if (d < 0) return -e;
        fd = d;
    }
    int fl = fcntl(fd, F_GETFL);
    if (fl < 0 || fcntl(fd, F_SETFL, fl | O_NONBLOCK) < 0 || fcntl(fd, F_SETFD, FD_CLOEXEC) < 0) {
        int e = errno;
        close(fd);
        return -e;
    }
#if defined(SO_NOSIGPIPE)
    int one = 1;
    (void)setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &one, sizeof one);
#endif
    return fd;
}
static int kern_net_socket(int family, int type) {
#if defined(SOCK_CLOEXEC) && defined(SOCK_NONBLOCK)
    int fd = socket(family, type | SOCK_CLOEXEC | SOCK_NONBLOCK, 0); // atomically: no child of another thread inherits it
#else
    int fd = socket(family, type, 0); // a child is spawned with only the descriptors it is given (rt_proc.c)
#endif
    if (fd < 0) return -errno;
    return kern_net_ready(fd);
}
// kern_net_wait waits until fd is ready for events (or has an error or a
// hang-up to report) or the deadline passes: 1, 0 when the time passed,
// or -errno.
static int kern_net_wait(int fd, short events, int timed, uint64_t start, uint64_t ns) {
    for (;;) {
        int ms = -1;
        if (timed) {
            uint64_t spent = kern_now_ns() - start;
            if (spent >= ns) return 0;
            uint64_t left = ns - spent;
            uint64_t whole = left / 1000000u + (left % 1000000u != 0); // poll waits whole milliseconds: rounded up
            ms = whole > (uint64_t)INT_MAX ? INT_MAX : (int)whole;
        }
        struct pollfd p = {fd, events, 0};
        int r = poll(&p, 1, ms);
        if (r > 0) return 1;
        if (r < 0 && errno != EINTR) return -errno;
        // interrupted, or the wait ended: look at the clock again
    }
}
static int kern_net_would_block(int e) { return e == EAGAIN || e == EWOULDBLOCK; }
#if defined(MSG_NOSIGNAL)
#define KERN_SEND_FLAGS MSG_NOSIGNAL
#else
#define KERN_SEND_FLAGS 0 // SO_NOSIGPIPE is on the socket
#endif

// kern_net_bound makes a socket of the record's family and binds it.
static int kern_net_bound(const uint8_t *addr, int64_t n, int type, int reuse) {
    struct sockaddr_storage ss;
    socklen_t len = kern_net_to_sa(addr, n, &ss);
    if (len == 0) return -EINVAL;
    int fd = kern_net_socket(ss.ss_family, type);
    if (fd < 0) return fd;
    int one = 1;
    if (reuse) (void)setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &one, sizeof one);
    // an IPv6 socket is IPv6 only on every host: the same program binds
    // the same addresses everywhere
    if (ss.ss_family == AF_INET6) (void)setsockopt(fd, IPPROTO_IPV6, IPV6_V6ONLY, &one, sizeof one);
    if (bind(fd, (struct sockaddr *)&ss, len) < 0) {
        int e = errno;
        close(fd);
        return -e;
    }
    return fd;
}
int32_t kern_net_tcp_listen(const uint8_t *addr, int64_t n, int32_t backlog) {
    int fd = kern_net_bound(addr, n, SOCK_STREAM, 1);
    if (fd < 0) return fd;
    if (listen(fd, backlog < 0 ? 0 : backlog) < 0) {
        int e = errno;
        close(fd);
        return -e;
    }
    return fd;
}
int32_t kern_net_udp_bind(const uint8_t *addr, int64_t n) { return kern_net_bound(addr, n, SOCK_DGRAM, 0); }

int64_t kern_net_accept(int32_t fd, uint64_t ns, uint8_t timed) {
    uint64_t start = timed ? kern_now_ns() : 0;
    for (;;) {
#if defined(SOCK_CLOEXEC) && defined(SOCK_NONBLOCK)
        int c = accept4(fd, NULL, NULL, SOCK_CLOEXEC | SOCK_NONBLOCK);
#else
        int c = accept(fd, NULL, NULL);
#endif
        if (c >= 0) return kern_net_ready(c);
        // a connection that went away before it was accepted is not this
        // call's failure: take the next
        if (errno == EINTR || errno == ECONNABORTED) continue;
        if (!kern_net_would_block(errno)) return -errno;
        int w = kern_net_wait(fd, POLLIN, timed, start, ns);
        if (w == 0) return KERN_TIMED_OUT;
        if (w < 0) return w;
    }
}
// kern_net_connect_to makes one connect call on a new stream socket of
// the family; when the connection is still being made it waits until the
// socket is writable, and SO_ERROR is then the result. A Unix-domain
// socket whose listener's queue is full answers EAGAIN and makes no
// connection in the background: the call is made again, a millisecond
// later, until the time has passed. A descriptor the caller never saw is
// closed on every path that does not return it.
static int64_t kern_net_connect_to(int family, const struct sockaddr *sa, socklen_t len, uint64_t ns, uint8_t timed) {
    uint64_t start = timed ? kern_now_ns() : 0;
    int fd = kern_net_socket(family, SOCK_STREAM);
    if (fd < 0) return fd;
    for (;;) {
        if (connect(fd, sa, len) == 0) return fd;
        if (errno != EAGAIN || family != AF_UNIX) break;
        if (timed && kern_now_ns() - start >= ns) {
            close(fd);
            return KERN_TIMED_OUT;
        }
        struct timespec nap = {0, 1000000};
        (void)nanosleep(&nap, NULL);
    }
    // an interrupted connect goes on being made, as one in progress does
    if (errno != EINPROGRESS && errno != EINTR) {
        int e = errno;
        close(fd);
        return -e;
    }
    int w = kern_net_wait(fd, POLLOUT, timed, start, ns);
    if (w <= 0) {
        close(fd);
        return w == 0 ? KERN_TIMED_OUT : w;
    }
    int err = 0;
    socklen_t el = sizeof err;
    if (getsockopt(fd, SOL_SOCKET, SO_ERROR, &err, &el) < 0) err = errno;
    if (err != 0) {
        close(fd);
        return -err;
    }
    return fd;
}
int64_t kern_net_connect(const uint8_t *addr, int64_t n, uint64_t ns, uint8_t timed) {
    struct sockaddr_storage ss;
    socklen_t len = kern_net_to_sa(addr, n, &ss);
    if (len == 0) return -EINVAL;
    return kern_net_connect_to(ss.ss_family, (struct sockaddr *)&ss, len, ns, timed);
}

// ---- Unix-domain sockets (specs/runtime RL1) ----

// kern_net_unix_sa fills a Unix-domain address from a path: its length,
// or a negative error for a path that cannot be one (empty or holding a
// NUL: -EINVAL; 104 bytes or more, the smallest limit of the supported
// hosts: -ENAMETOOLONG).
static int kern_net_unix_sa(const char *p, int64_t n, struct sockaddr_un *sa) {
    if (n <= 0 || memchr(p, 0, (size_t)n) != NULL) return -EINVAL;
    if (n >= 104 || (size_t)n >= sizeof sa->sun_path) return -ENAMETOOLONG;
    memset(sa, 0, sizeof *sa);
    sa->sun_family = AF_UNIX;
    memcpy(sa->sun_path, p, (size_t)n);
    return (int)(offsetof(struct sockaddr_un, sun_path) + (size_t)n + 1);
}
int32_t kern_net_unix_listen(const char *p, int64_t n, int32_t backlog) {
    struct sockaddr_un sa;
    int len = kern_net_unix_sa(p, n, &sa);
    if (len < 0) return len;
    int fd = kern_net_socket(AF_UNIX, SOCK_STREAM);
    if (fd < 0) return fd;
    if (bind(fd, (struct sockaddr *)&sa, (socklen_t)len) < 0 || listen(fd, backlog < 0 ? 0 : backlog) < 0) {
        int e = errno;
        close(fd);
        return -e;
    }
    return fd;
}
int64_t kern_net_unix_connect(const char *p, int64_t n, uint64_t ns, uint8_t timed) {
    struct sockaddr_un sa;
    int len = kern_net_unix_sa(p, n, &sa);
    if (len < 0) return len;
    return kern_net_connect_to(AF_UNIX, (struct sockaddr *)&sa, (socklen_t)len, ns, timed);
}
// kern_net_unix_dgram makes a Unix-domain datagram socket, bound to the
// path when there is one (n > 0): a descriptor or -errno.
int32_t kern_net_unix_dgram(const char *p, int64_t n) {
    struct sockaddr_un sa;
    int len = 0;
    if (n != 0 && (len = kern_net_unix_sa(p, n, &sa)) < 0) return len;
    int fd = kern_net_socket(AF_UNIX, SOCK_DGRAM);
    if (fd < 0) return fd;
    if (n != 0 && bind(fd, (struct sockaddr *)&sa, (socklen_t)len) < 0) {
        int e = errno;
        close(fd);
        return -e;
    }
    return fd;
}
int64_t kern_net_unix_send_to(int32_t fd, const char *p, int64_t n, const char *path, int64_t pn) {
    struct sockaddr_un sa;
    int len = kern_net_unix_sa(path, pn, &sa);
    if (len < 0) return len;
    if (n < 0) return -EINVAL;
    for (;;) {
        ssize_t r = sendto(fd, p, (size_t)n, KERN_SEND_FLAGS, (struct sockaddr *)&sa, (socklen_t)len);
        if (r >= 0) return (int64_t)r;
        if (errno == EINTR) continue;
        // a receiver whose queue is full (which this socket's own
        // readiness does not show): the send is made again each millisecond
        if (!kern_net_would_block(errno) && errno != ENOBUFS) return -errno;
        struct timespec nap = {0, 1000000};
        (void)nanosleep(&nap, NULL);
    }
}
// kern_net_unix_recv_from receives one datagram, cut to n bytes, and
// writes its sender's path into from, NUL-terminated: an empty path for a
// sender that is not bound.
int64_t kern_net_unix_recv_from(int32_t fd, char *buf, int64_t n, char *from, int64_t fcap, uint64_t ns, uint8_t timed) {
    if (n < 0 || fcap < 105) return -EINVAL;
    uint64_t start = timed ? kern_now_ns() : 0;
    for (;;) {
        struct sockaddr_un sa;
        socklen_t len = sizeof sa;
        memset(&sa, 0, sizeof sa);
        ssize_t r = recvfrom(fd, buf, (size_t)n, 0, (struct sockaddr *)&sa, &len);
        if (r >= 0) {
            size_t pl = len > offsetof(struct sockaddr_un, sun_path) ? strnlen(sa.sun_path, len - offsetof(struct sockaddr_un, sun_path)) : 0;
            if (pl > 104) pl = 104;
            memcpy(from, sa.sun_path, pl);
            from[pl] = 0;
            return (int64_t)r;
        }
        if (errno == EINTR) continue;
        if (!kern_net_would_block(errno)) return -errno;
        int w = kern_net_wait(fd, POLLIN, timed, start, ns);
        if (w == 0) return KERN_TIMED_OUT;
        if (w < 0) return w;
    }
}
int32_t kern_net_unix_pair(int32_t *fds, int64_t cap) {
    if (cap < 2) return -EINVAL;
    int sv[2];
    if (socketpair(AF_UNIX, SOCK_STREAM, 0, sv) < 0) return -errno;
    int a = kern_net_ready(sv[0]);
    if (a < 0) { close(sv[1]); return a; }
    int b = kern_net_ready(sv[1]);
    if (b < 0) { close(a); return b; }
    fds[0] = a;
    fds[1] = b;
    return 0;
}

int64_t kern_net_recv(int32_t fd, char *buf, int64_t n, uint64_t ns, uint8_t timed) {
    if (n <= 0) return 0;
    uint64_t start = timed ? kern_now_ns() : 0;
    for (;;) {
        ssize_t r = recv(fd, buf, (size_t)n, 0);
        if (r >= 0) return (int64_t)r;
        if (errno == EINTR) continue;
        if (!kern_net_would_block(errno)) return -errno;
        int w = kern_net_wait(fd, POLLIN, timed, start, ns);
        if (w == 0) return KERN_TIMED_OUT;
        if (w < 0) return w;
    }
}
int64_t kern_net_send(int32_t fd, const char *p, int64_t n) {
    if (n <= 0) return 0;
    for (;;) {
        ssize_t r = send(fd, p, (size_t)n, KERN_SEND_FLAGS);
        if (r >= 0) return (int64_t)r;
        if (errno == EINTR) continue;
        if (!kern_net_would_block(errno)) return -errno;
        int w = kern_net_wait(fd, POLLOUT, 0, 0, 0);
        if (w < 0) return w;
    }
}
int32_t kern_net_shutdown(int32_t fd, int32_t how) {
    if (how < 0 || how > 2) return -EINVAL;
    return shutdown(fd, how == 0 ? SHUT_RD : how == 1 ? SHUT_WR : SHUT_RDWR) < 0 ? -errno : 0;
}
int32_t kern_net_set_nodelay(int32_t fd, uint8_t on) {
    int v = on ? 1 : 0;
    return setsockopt(fd, IPPROTO_TCP, TCP_NODELAY, &v, sizeof v) < 0 ? -errno : 0;
}
// The socket options std/net names (specs/runtime RM4), by its numbers: 1
// keep-alive probes, 2 the receive buffer's size, 3 the send buffer's, 4
// broadcast datagrams, 5 the hop limit of what is sent (the TTL of IPv4),
// 6 linger on close in seconds (-1: do not), 7 TCP_NODELAY.
static int kern_net_opt(int32_t fd, int32_t opt, int *level, int *name) {
    *level = SOL_SOCKET;
    switch (opt) {
    case 1: *name = SO_KEEPALIVE; return 0;
    case 2: *name = SO_RCVBUF; return 0;
    case 3: *name = SO_SNDBUF; return 0;
    case 4: *name = SO_BROADCAST; return 0;
#if defined(SO_LINGER_SEC)
    case 6: *name = SO_LINGER_SEC; return 0; // Darwin's SO_LINGER counts in ticks
#else
    case 6: *name = SO_LINGER; return 0;
#endif
    case 7: *level = IPPROTO_TCP; *name = TCP_NODELAY; return 0;
    case 5: {
        struct sockaddr_storage ss;
        socklen_t len = sizeof ss;
        if (getsockname(fd, (struct sockaddr *)&ss, &len) < 0) return -errno;
        if (ss.ss_family == AF_INET6) { *level = IPPROTO_IPV6; *name = IPV6_UNICAST_HOPS; }
        else { *level = IPPROTO_IP; *name = IP_TTL; }
        return 0;
    }
    }
    return -EINVAL;
}
int32_t kern_net_set_opt(int32_t fd, int32_t opt, int64_t value) {
    int level, name;
    int e = kern_net_opt(fd, opt, &level, &name);
    if (e < 0) return e;
    if (opt == 6) {
        struct linger l = { value >= 0, value >= 0 ? (int)(value > INT_MAX ? INT_MAX : value) : 0 };
        return setsockopt(fd, level, name, &l, sizeof l) < 0 ? -errno : 0;
    }
    if (value < INT_MIN || value > INT_MAX) return -EINVAL;
    int v = (int)value;
    return setsockopt(fd, level, name, &v, sizeof v) < 0 ? -errno : 0;
}
// kern_net_get_opt is an option's value, or -errno - 1: linger that is
// off is the value -1, so a failure is below it.
int64_t kern_net_get_opt(int32_t fd, int32_t opt) {
    int level, name;
    int e = kern_net_opt(fd, opt, &level, &name);
    if (e < 0) return (int64_t)e - 1;
    if (opt == 6) {
        struct linger l;
        socklen_t len = sizeof l;
        if (getsockopt(fd, level, name, &l, &len) < 0) return -(int64_t)errno - 1;
        return l.l_onoff ? (int64_t)l.l_linger : -1;
    }
    int v = 0;
    socklen_t len = sizeof v;
    if (getsockopt(fd, level, name, &v, &len) < 0) return -(int64_t)errno - 1;
    return (int64_t)v;
}
// kern_net_if_name writes the name of the interface with that index into
// buf: its length, or 0 when there is none or it does not fit.
int64_t kern_net_if_name(uint32_t index, char *buf, int64_t cap) {
    char name[IF_NAMESIZE];
    if (if_indextoname(index, name) == NULL) return 0;
    size_t n = strlen(name);
    if (cap < 0 || n > (size_t)cap) return 0;
    memcpy(buf, name, n);
    return (int64_t)n;
}
// kern_net_if_index is the index of the interface of that name, or 0.
uint32_t kern_net_if_index(const char *p, int64_t n) {
    char name[IF_NAMESIZE];
    if (n <= 0 || (size_t)n >= sizeof name || memchr(p, 0, (size_t)n) != NULL) return 0;
    memcpy(name, p, (size_t)n);
    name[n] = 0;
    return if_nametoindex(name);
}
int32_t kern_net_addr(int32_t fd, uint8_t peer, uint8_t *rec, int64_t cap) {
    if (cap < KERN_ADDR_BYTES) return -EINVAL;
    struct sockaddr_storage ss;
    socklen_t len = sizeof ss;
    memset(&ss, 0, sizeof ss);
    if ((peer ? getpeername(fd, (struct sockaddr *)&ss, &len) : getsockname(fd, (struct sockaddr *)&ss, &len)) < 0) return -errno;
    return kern_net_from_sa(&ss, rec);
}
int64_t kern_net_send_to(int32_t fd, const char *p, int64_t n, const uint8_t *addr, int64_t an) {
    struct sockaddr_storage ss;
    socklen_t len = kern_net_to_sa(addr, an, &ss);
    if (len == 0 || n < 0) return -EINVAL;
    for (;;) {
        ssize_t r = sendto(fd, p, (size_t)n, KERN_SEND_FLAGS, (struct sockaddr *)&ss, len);
        if (r >= 0) return (int64_t)r;
        if (errno == EINTR) continue;
        if (!kern_net_would_block(errno)) return -errno;
        int w = kern_net_wait(fd, POLLOUT, 0, 0, 0);
        if (w < 0) return w;
    }
}
// kern_net_recv_from receives one datagram: its bytes go into buf, cut to
// n when it is longer, and its sender's record into from.
int64_t kern_net_recv_from(int32_t fd, char *buf, int64_t n, uint8_t *from, int64_t fcap, uint64_t ns, uint8_t timed) {
    if (n < 0 || fcap < KERN_ADDR_BYTES) return -EINVAL;
    uint64_t start = timed ? kern_now_ns() : 0;
    for (;;) {
        struct sockaddr_storage ss;
        socklen_t len = sizeof ss;
        memset(&ss, 0, sizeof ss);
        ssize_t r = recvfrom(fd, buf, (size_t)n, 0, (struct sockaddr *)&ss, &len);
        if (r >= 0) {
            int e = kern_net_from_sa(&ss, from);
            return e < 0 ? e : (int64_t)r;
        }
        if (errno == EINTR) continue;
        if (!kern_net_would_block(errno)) return -errno;
        int w = kern_net_wait(fd, POLLIN, timed, start, ns);
        if (w == 0) return KERN_TIMED_OUT;
        if (w < 0) return w;
    }
}
int32_t kern_net_close(int32_t fd) { if (fd < 0) return -EBADF; return close(fd) < 0 ? -errno : 0; }

// kern_net_resolve asks the system resolver for a host's addresses and
// writes their records, each with the given port, into recs while they
// fit in cap bytes; it returns the bytes all of them need, or -errno. The
// resolver's own failures have no errno: an unknown name is -ENOENT, a
// failure that may pass -EAGAIN, no memory -ENOMEM, anything else -EIO.
int64_t kern_net_resolve(const char *host, int64_t n, uint16_t port, uint8_t *recs, int64_t cap) {
    char name[256];
    if (n <= 0 || (size_t)n >= sizeof name || memchr(host, 0, (size_t)n) != NULL) return -EINVAL;
    memcpy(name, host, (size_t)n);
    name[n] = 0;
    struct addrinfo hints, *list = NULL;
    memset(&hints, 0, sizeof hints);
    hints.ai_family = AF_UNSPEC;
    hints.ai_socktype = SOCK_STREAM; // one entry per address, not one per socket type
    int g = getaddrinfo(name, NULL, &hints, &list);
    if (g != 0) {
        if (g == EAI_SYSTEM) return -errno;
        if (g == EAI_NONAME) return -ENOENT;
#if defined(EAI_NODATA) && EAI_NODATA != EAI_NONAME
        if (g == EAI_NODATA) return -ENOENT;
#endif
        if (g == EAI_AGAIN) return -EAGAIN;
        if (g == EAI_MEMORY) return -ENOMEM;
        return -EIO;
    }
    int64_t total = 0;
    for (struct addrinfo *ai = list; ai != NULL; ai = ai->ai_next) {
        if (ai->ai_family != AF_INET && ai->ai_family != AF_INET6) continue;
        struct sockaddr_storage ss;
        memset(&ss, 0, sizeof ss);
        memcpy(&ss, ai->ai_addr, ai->ai_addrlen < sizeof ss ? ai->ai_addrlen : sizeof ss);
        uint8_t rec[KERN_ADDR_BYTES];
        if (kern_net_from_sa(&ss, rec) < 0) continue;
        rec[17] = (uint8_t)(port >> 8);
        rec[18] = (uint8_t)port;
        if (total + KERN_ADDR_BYTES <= cap) memcpy(recs + total, rec, KERN_ADDR_BYTES);
        total += KERN_ADDR_BYTES;
    }
    freeaddrinfo(list);
    return total;
}
