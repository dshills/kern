//! packet_parser: P=4096 packets are generated (untimed, LCG seed 42) back to
//! back into one byte buffer. Packet = 32-byte big-endian header + payload:
//!   0 magic u32 (0xCAFEF00D) | 4 version u8 (1) | 5 flags u8 | 6 hdr_len u16 (32)
//!   8 src u32 | 12 dst u32 | 16 src_port u16 | 18 dst_port u16 | 20 seq u32
//!   24 payload_len u16 | 26 csum u16 | 28 ts u32
//! Per packet three LCG draws x1,x2,x3: src=x1>>32, dst=x1 (low 32),
//! src_port=x2>>48, dst_port=x2>>32 (low 16), seq=x2 (low 32),
//! payload_len=16+(x3>>57), ts=x3 (low 32), flags=x3>>32 (low 8);
//! payload byte j = (seq + j) (low 8 bits). csum is the Internet checksum
//! (ones'-complement sum of the 16 header words with csum=0, complemented).
//! Then N=10,000,000 operations: operation i parses packet i mod P from the
//! buffer into a header struct (length, magic, hdr_len, header checksum and
//! payload bound are checked; failure aborts) and computes the folded
//! ones'-complement 16-bit sum of the payload view (odd trailing byte is the
//! high byte of a final word). ops = N; sampled operation: parse + payload sum.
//! checksum = wrapping sum over operations of (seq XOR (psum << 32)).
#[path = "../../bench.rs"]
mod bench;

use bench::{Lcg, Sampler};
use std::time::Instant;

const N: u64 = 10_000_000;
const P: usize = 4096;
const HDR: usize = 32;

#[allow(dead_code)]
#[derive(Clone, Copy)]
struct Header {
    magic: u32,
    version: u8,
    flags: u8,
    hdr_len: u16,
    src: u32,
    dst: u32,
    src_port: u16,
    dst_port: u16,
    seq: u32,
    payload_len: u16,
    csum: u16,
    ts: u32,
}

#[inline]
fn rd16(p: &[u8], at: usize) -> u16 {
    u16::from_be_bytes([p[at], p[at + 1]])
}
#[inline]
fn rd32(p: &[u8], at: usize) -> u32 {
    u32::from_be_bytes([p[at], p[at + 1], p[at + 2], p[at + 3]])
}

/// sum16 is the folded ones'-complement sum of big-endian 16-bit words.
#[inline]
fn sum16(p: &[u8]) -> u16 {
    let mut s: u32 = 0;
    let mut chunks = p.chunks_exact(2);
    for c in &mut chunks {
        s += u16::from_be_bytes([c[0], c[1]]) as u32;
    }
    if let [b] = chunks.remainder() {
        s += (*b as u32) << 8;
    }
    while s >> 16 != 0 {
        s = (s & 0xFFFF) + (s >> 16);
    }
    s as u16
}

/// parse decodes a header from pkt; None on any malformed input.
#[inline]
fn parse(pkt: &[u8]) -> Option<Header> {
    let p = pkt.get(..HDR)?;
    let h = Header {
        magic: rd32(p, 0),
        version: p[4],
        flags: p[5],
        hdr_len: rd16(p, 6),
        src: rd32(p, 8),
        dst: rd32(p, 12),
        src_port: rd16(p, 16),
        dst_port: rd16(p, 18),
        seq: rd32(p, 20),
        payload_len: rd16(p, 24),
        csum: rd16(p, 26),
        ts: rd32(p, 28),
    };
    if h.magic != 0xCAFEF00D || h.hdr_len as usize != HDR {
        return None;
    }
    if sum16(p) != 0xFFFF {
        return None;
    }
    pkt.get(HDR..HDR + h.payload_len as usize)?;
    Some(h)
}

fn gen(buf: &mut Vec<u8>, lcg: &mut Lcg) {
    let (x1, x2, x3) = (lcg.next(), lcg.next(), lcg.next());
    let plen = (16 + (x3 >> 57)) as u16;
    let seq = x2 as u32;
    let mut h = [0u8; HDR];
    h[0..4].copy_from_slice(&0xCAFEF00Du32.to_be_bytes());
    h[4] = 1;
    h[5] = (x3 >> 32) as u8;
    h[6..8].copy_from_slice(&(HDR as u16).to_be_bytes());
    h[8..12].copy_from_slice(&((x1 >> 32) as u32).to_be_bytes());
    h[12..16].copy_from_slice(&(x1 as u32).to_be_bytes());
    h[16..18].copy_from_slice(&((x2 >> 48) as u16).to_be_bytes());
    h[18..20].copy_from_slice(&((x2 >> 32) as u16).to_be_bytes());
    h[20..24].copy_from_slice(&seq.to_be_bytes());
    h[24..26].copy_from_slice(&plen.to_be_bytes());
    h[28..32].copy_from_slice(&(x3 as u32).to_be_bytes());
    let c = !sum16(&h);
    h[26..28].copy_from_slice(&c.to_be_bytes());
    buf.extend_from_slice(&h);
    for j in 0..plen as u32 {
        buf.push(seq.wrapping_add(j) as u8);
    }
}

fn main() {
    let mut buf: Vec<u8> = Vec::with_capacity(P * (HDR + 144));
    let mut offsets = [0usize; P];
    let mut lcg = Lcg::new();
    for off in offsets.iter_mut() {
        *off = buf.len();
        gen(&mut buf, &mut lcg);
    }
    let mut s = Sampler::new(N);
    let mut checksum: u64 = 0;

    let start = Instant::now();
    for i in 0..N {
        let off = offsets[(i as usize) & (P - 1)];
        let w = s.want(i);
        if w {
            s.begin();
        }
        let pkt = &buf[off..];
        let h = parse(pkt).expect("malformed packet");
        let psum = sum16(&pkt[HDR..HDR + h.payload_len as usize]);
        if w {
            s.end();
        }
        checksum = checksum.wrapping_add(h.seq as u64 ^ ((psum as u64) << 32));
    }
    let ns = start.elapsed().as_nanos() as u64;

    s.report(N, ns, checksum);
}
