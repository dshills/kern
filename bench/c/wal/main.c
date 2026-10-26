/* wal: N=2,000,000 records appended to an in-memory log (growable byte
 * buffer, doubling). Record = 16-byte header + 64-byte payload. Header:
 * lsn u64 LE, len u32 LE (=64), crc u32 LE = CRC-32/IEEE (poly 0xEDB88320,
 * reflected, init and final xor 0xFFFFFFFF, table driven) of the payload.
 * Payload = 8 LE u64 words, word j = (lsn+1)*0x9E3779B97F4A7C15 + j
 * (wrapping). lsn starts at 0. Then replay: walk the log, parse each header,
 * verify the payload CRC (abort on mismatch or short record), and chain
 *   cs = (cs XOR ((lsn << 32) | crc)) * 0x100000001b3 (wrapping), from 0.
 * ops = N appends; sampled operation: one append; replay is timed but not
 * counted. checksum = cs after replaying exactly N records. */
#include "../bench.h"

#define N 2000000ull
#define PAYLOAD 64
#define HDR 16

static uint32_t crc_table[256];

static void crc_init(void) {
    for (uint32_t i = 0; i < 256; i++) {
        uint32_t c = i;
        for (int k = 0; k < 8; k++) c = (c & 1) ? 0xEDB88320u ^ (c >> 1) : c >> 1;
        crc_table[i] = c;
    }
}

static inline uint32_t crc32(const uint8_t *p, size_t n) {
    uint32_t c = 0xFFFFFFFFu;
    for (size_t i = 0; i < n; i++) c = crc_table[(c ^ p[i]) & 0xFF] ^ (c >> 8);
    return c ^ 0xFFFFFFFFu;
}

typedef struct {
    uint8_t *buf;
    size_t len, cap;
    uint64_t next_lsn;
} wal;

static inline void put_u64(uint8_t *p, uint64_t v) {
    for (int i = 0; i < 8; i++) p[i] = (uint8_t)(v >> (8 * i));
}
static inline void put_u32(uint8_t *p, uint32_t v) {
    for (int i = 0; i < 4; i++) p[i] = (uint8_t)(v >> (8 * i));
}
static inline uint64_t get_u64(const uint8_t *p) {
    uint64_t v = 0;
    for (int i = 0; i < 8; i++) v |= (uint64_t)p[i] << (8 * i);
    return v;
}
static inline uint32_t get_u32(const uint8_t *p) {
    uint32_t v = 0;
    for (int i = 0; i < 4; i++) v |= (uint32_t)p[i] << (8 * i);
    return v;
}

static inline void wal_extend(wal *w, const uint8_t *p, size_t n) {
    if (w->len + n > w->cap) {
        size_t cap = w->cap ? w->cap : 1024;
        while (w->len + n > cap) cap *= 2;
        w->buf = xrealloc(w->buf, cap);
        w->cap = cap;
    }
    memcpy(w->buf + w->len, p, n);
    w->len += n;
}

static uint64_t wal_append(wal *w, const uint8_t *payload, uint32_t n) {
    uint8_t hdr[HDR];
    uint64_t lsn = w->next_lsn;
    put_u64(hdr, lsn);
    put_u32(hdr + 8, n);
    put_u32(hdr + 12, crc32(payload, n));
    wal_extend(w, hdr, HDR);
    wal_extend(w, payload, n);
    w->next_lsn = lsn + 1;
    return lsn;
}

static uint64_t replay(const uint8_t *data, size_t len, uint64_t *count) {
    uint64_t cs = 0;
    size_t off = 0;
    while (off + HDR <= len) {
        uint64_t lsn = get_u64(data + off);
        uint32_t n = get_u32(data + off + 8), crc = get_u32(data + off + 12);
        size_t end = off + HDR + n;
        if (end > len) abort();
        if (crc32(data + off + HDR, n) != crc) abort();
        cs = (cs ^ ((lsn << 32) | crc)) * 0x100000001b3ull;
        (*count)++;
        off = end;
    }
    return cs;
}

int main(void) {
    crc_init();
    wal w = {0, 0, 0, 0};
    sampler s;
    sampler_init(&s, N);
    uint8_t payload[PAYLOAD];
    uint64_t count = 0;

    uint64_t start = now_ns();
    for (uint64_t i = 0; i < N; i++) {
        for (uint64_t j = 0; j < PAYLOAD / 8; j++)
            put_u64(payload + 8 * j, (i + 1) * 0x9E3779B97F4A7C15ull + j);
        int ws = sampler_want(&s, i);
        if (ws) sampler_begin(&s);
        uint64_t lsn = wal_append(&w, payload, PAYLOAD);
        if (ws) sampler_end(&s);
        if (lsn != i) abort();
    }
    uint64_t checksum = replay(w.buf, w.len, &count);
    uint64_t ns = now_ns() - start;

    if (count != N) abort();
    bench_report(N, ns, &s, checksum);
    free(w.buf);
    return 0;
}
