/* packet_parser: P=4096 packets are generated (untimed, LCG seed 42) back to
 * back into one byte buffer. Packet = 32-byte big-endian header + payload:
 *   0 magic u32 (0xCAFEF00D) | 4 version u8 (1) | 5 flags u8 | 6 hdr_len u16 (32)
 *   8 src u32 | 12 dst u32 | 16 src_port u16 | 18 dst_port u16 | 20 seq u32
 *   24 payload_len u16 | 26 csum u16 | 28 ts u32
 * Per packet three LCG draws x1,x2,x3: src=x1>>32, dst=x1 (low 32),
 * src_port=x2>>48, dst_port=x2>>32 (low 16), seq=x2 (low 32),
 * payload_len=16+(x3>>57), ts=x3 (low 32), flags=x3>>32 (low 8);
 * payload byte j = (seq + j) (low 8 bits). csum is the Internet checksum
 * (ones'-complement sum of the 16 header words with csum=0, complemented).
 * Then N=10,000,000 operations: operation i parses packet i mod P from the
 * buffer into a header struct (length, magic, hdr_len, header checksum and
 * payload bound are checked; failure aborts) and computes the folded
 * ones'-complement 16-bit sum of the payload view (odd trailing byte is the
 * high byte of a final word). ops = N; sampled operation: parse + payload sum.
 * checksum = wrapping sum over operations of (seq XOR (psum << 32)). */
#include "../bench.h"

#define N 10000000ull
#define P 4096
#define HDR 32

typedef struct {
    uint32_t magic, src, dst, seq, ts;
    uint16_t hdr_len, src_port, dst_port, payload_len, csum;
    uint8_t version, flags;
} header;

static inline uint16_t rd16(const uint8_t *p) { return (uint16_t)((p[0] << 8) | p[1]); }
static inline uint32_t rd32(const uint8_t *p) {
    return ((uint32_t)p[0] << 24) | ((uint32_t)p[1] << 16) | ((uint32_t)p[2] << 8) | p[3];
}
static inline void wr16(uint8_t *p, uint16_t v) { p[0] = (uint8_t)(v >> 8); p[1] = (uint8_t)v; }
static inline void wr32(uint8_t *p, uint32_t v) { wr16(p, (uint16_t)(v >> 16)); wr16(p + 2, (uint16_t)v); }

/* sum16 is the folded ones'-complement sum of big-endian 16-bit words. */
static inline uint16_t sum16(const uint8_t *p, size_t n) {
    uint32_t s = 0;
    size_t i = 0;
    for (; i + 1 < n; i += 2) s += rd16(p + i);
    if (i < n) s += (uint32_t)p[i] << 8;
    while (s >> 16) s = (s & 0xFFFF) + (s >> 16);
    return (uint16_t)s;
}

/* parse decodes a header from pkt (len bytes available); returns 0 on any
 * malformed input. */
static inline int parse(const uint8_t *pkt, size_t len, header *h) {
    if (len < HDR) return 0;
    h->magic = rd32(pkt);
    h->version = pkt[4];
    h->flags = pkt[5];
    h->hdr_len = rd16(pkt + 6);
    h->src = rd32(pkt + 8);
    h->dst = rd32(pkt + 12);
    h->src_port = rd16(pkt + 16);
    h->dst_port = rd16(pkt + 18);
    h->seq = rd32(pkt + 20);
    h->payload_len = rd16(pkt + 24);
    h->csum = rd16(pkt + 26);
    h->ts = rd32(pkt + 28);
    if (h->magic != 0xCAFEF00Du || h->hdr_len != HDR) return 0;
    if (sum16(pkt, HDR) != 0xFFFF) return 0;
    if ((size_t)HDR + h->payload_len > len) return 0;
    return 1;
}

static size_t gen(uint8_t *buf, size_t off, uint64_t *x) {
    uint64_t x1 = lcg_next(x), x2 = lcg_next(x), x3 = lcg_next(x);
    uint8_t *p = buf + off;
    uint16_t plen = (uint16_t)(16 + (x3 >> 57));
    uint32_t seq = (uint32_t)x2;
    wr32(p, 0xCAFEF00Du);
    p[4] = 1;
    p[5] = (uint8_t)(x3 >> 32);
    wr16(p + 6, HDR);
    wr32(p + 8, (uint32_t)(x1 >> 32));
    wr32(p + 12, (uint32_t)x1);
    wr16(p + 16, (uint16_t)(x2 >> 48));
    wr16(p + 18, (uint16_t)(x2 >> 32));
    wr32(p + 20, seq);
    wr16(p + 24, plen);
    wr16(p + 26, 0);
    wr32(p + 28, (uint32_t)x3);
    wr16(p + 26, (uint16_t)~sum16(p, HDR));
    for (uint32_t j = 0; j < plen; j++) p[HDR + j] = (uint8_t)(seq + j);
    return off + HDR + plen;
}

int main(void) {
    static size_t offsets[P];
    uint8_t *buf = xmalloc((size_t)P * (HDR + 144));
    uint64_t x = LCG_SEED;
    size_t len = 0;
    for (size_t i = 0; i < P; i++) {
        offsets[i] = len;
        len = gen(buf, len, &x);
    }
    sampler s;
    sampler_init(&s, N);
    uint64_t checksum = 0;

    uint64_t start = now_ns();
    for (uint64_t i = 0; i < N; i++) {
        size_t off = offsets[i & (P - 1)];
        header h;
        int w = sampler_want(&s, i);
        if (w) sampler_begin(&s);
        if (!parse(buf + off, len - off, &h)) abort();
        uint16_t psum = sum16(buf + off + HDR, h.payload_len);
        if (w) sampler_end(&s);
        checksum += (uint64_t)h.seq ^ ((uint64_t)psum << 32);
    }
    uint64_t ns = now_ns() - start;

    bench_report(N, ns, &s, checksum);
    free(buf);
    return 0;
}
