//! wal: N=2,000,000 records appended to an in-memory log (Vec<u8>, doubling).
//! Record = 16-byte header + 64-byte payload. Header: lsn u64 LE, len u32 LE
//! (=64), crc u32 LE = CRC-32/IEEE (poly 0xEDB88320, reflected, init and final
//! xor 0xFFFFFFFF, table driven) of the payload. Payload = 8 LE u64 words,
//! word j = (lsn+1)*0x9E3779B97F4A7C15 + j (wrapping). lsn starts at 0. Then
//! replay: walk the log, parse each header, verify the payload CRC (abort on
//! mismatch or short record), and chain
//!   cs = (cs XOR ((lsn << 32) | crc)) * 0x100000001b3 (wrapping), from 0.
//! ops = N appends; sampled operation: one append; replay is timed but not
//! counted. checksum = cs after replaying exactly N records.
#[path = "../../bench.rs"]
mod bench;

use bench::Sampler;
use std::time::Instant;

const N: u64 = 2_000_000;
const PAYLOAD: usize = 64;
const HDR: usize = 16;

struct Crc([u32; 256]);

impl Crc {
    fn new() -> Crc {
        let mut t = [0u32; 256];
        for (i, slot) in t.iter_mut().enumerate() {
            let mut c = i as u32;
            for _ in 0..8 {
                c = if c & 1 == 1 { 0xEDB88320 ^ (c >> 1) } else { c >> 1 };
            }
            *slot = c;
        }
        Crc(t)
    }
    #[inline]
    fn sum(&self, p: &[u8]) -> u32 {
        let mut c = 0xFFFF_FFFFu32;
        for &b in p {
            c = self.0[((c ^ b as u32) & 0xFF) as usize] ^ (c >> 8);
        }
        c ^ 0xFFFF_FFFF
    }
}

struct Wal {
    buf: Vec<u8>,
    next_lsn: u64,
}

impl Wal {
    fn append(&mut self, crc: &Crc, payload: &[u8]) -> u64 {
        let lsn = self.next_lsn;
        self.buf.extend_from_slice(&lsn.to_le_bytes());
        self.buf.extend_from_slice(&(payload.len() as u32).to_le_bytes());
        self.buf.extend_from_slice(&crc.sum(payload).to_le_bytes());
        self.buf.extend_from_slice(payload);
        self.next_lsn = lsn + 1;
        lsn
    }
}

fn replay(crc: &Crc, data: &[u8], count: &mut u64) -> u64 {
    let mut cs = 0u64;
    let mut off = 0usize;
    while off + HDR <= data.len() {
        let lsn = u64::from_le_bytes(data[off..off + 8].try_into().unwrap());
        let n = u32::from_le_bytes(data[off + 8..off + 12].try_into().unwrap()) as usize;
        let c = u32::from_le_bytes(data[off + 12..off + 16].try_into().unwrap());
        let end = off + HDR + n;
        let payload = data.get(off + HDR..end).expect("short record");
        assert_eq!(crc.sum(payload), c, "crc mismatch");
        cs = (cs ^ ((lsn << 32) | c as u64)).wrapping_mul(0x100000001b3);
        *count += 1;
        off = end;
    }
    cs
}

fn main() {
    let crc = Crc::new();
    let mut w = Wal { buf: Vec::new(), next_lsn: 0 };
    let mut s = Sampler::new(N);
    let mut payload = [0u8; PAYLOAD];
    let mut count = 0u64;

    let start = Instant::now();
    for i in 0..N {
        for j in 0..(PAYLOAD / 8) as u64 {
            let word = (i + 1).wrapping_mul(0x9E3779B97F4A7C15).wrapping_add(j);
            payload[(8 * j) as usize..(8 * j + 8) as usize].copy_from_slice(&word.to_le_bytes());
        }
        let ws = s.want(i);
        if ws {
            s.begin();
        }
        let lsn = w.append(&crc, &payload);
        if ws {
            s.end();
        }
        assert_eq!(lsn, i);
    }
    let checksum = replay(&crc, &w.buf, &mut count);
    let ns = start.elapsed().as_nanos() as u64;

    assert_eq!(count, N);
    s.report(N, ns, checksum);
}
