//! page_frame_allocator: a bitmap over 65,536 frames (1024 u64 words, set bit
//! = free, all free at start, hint = 0) and a FIFO ring of at most 4096 held
//! frames. N=10,000,000 ops; op i draws x from the LCG and allocates when
//! held == 0 or (x>>63 == 0 and held < 4096), else frees the oldest held
//! frame. alloc scans words from hint (wrapping) for the first non-zero word,
//! takes its lowest set bit, sets hint to that word; free checks the bit is
//! clear (double free aborts), sets it, and lowers hint to the word if
//! smaller. cs = (cs XOR f) * 0x100000001b3 after an alloc of frame f,
//! cs = (cs XOR (f | 1<<32)) * 0x100000001b3 after a free; at the end
//! cs += free frame count. KERN_BENCH_FAIL=1: frames 1024..65535 are marked
//! allocated in setup (never freed), so allocation fails once 1024 frames are
//! held (failures += 1, no checksum contribution).
#[path = "../../bench.rs"]
mod bench;

use bench::{fail_mode, Lcg, Sampler};
use std::time::Instant;

const N: u64 = 10_000_000;
const NFRAMES: u32 = 65536;
const WORDS: usize = NFRAMES as usize / 64;
const MAX_HELD: usize = 4096;
const FNV: u64 = 0x100000001b3;

struct FrameAlloc {
    bits: Box<[u64; WORDS]>,
    free_count: usize,
    hint: usize,
}

#[derive(Debug)]
struct OutOfFrames;

#[derive(Debug)]
enum FreeErr {
    OutOfRange,
    DoubleFree,
}

impl FrameAlloc {
    fn new() -> FrameAlloc {
        FrameAlloc { bits: Box::new([u64::MAX; WORDS]), free_count: NFRAMES as usize, hint: 0 }
    }

    #[inline]
    fn alloc(&mut self) -> Result<u32, OutOfFrames> {
        if self.free_count == 0 {
            return Err(OutOfFrames);
        }
        let mut w = self.hint;
        for _ in 0..WORDS {
            let word = self.bits[w];
            if word != 0 {
                let bit = word.trailing_zeros();
                self.bits[w] = word & (word - 1);
                self.free_count -= 1;
                self.hint = w;
                return Ok(w as u32 * 64 + bit);
            }
            w = (w + 1) & (WORDS - 1);
        }
        unreachable!("free_count > 0 but no set bit");
    }

    #[inline]
    fn free(&mut self, f: u32) -> Result<(), FreeErr> {
        if f >= NFRAMES {
            return Err(FreeErr::OutOfRange);
        }
        let w = (f / 64) as usize;
        let m = 1u64 << (f % 64);
        if self.bits[w] & m != 0 {
            return Err(FreeErr::DoubleFree);
        }
        self.bits[w] |= m;
        self.free_count += 1;
        if w < self.hint {
            self.hint = w;
        }
        Ok(())
    }
}

fn main() {
    let fail = fail_mode();
    let mut a = FrameAlloc::new();
    if fail {
        for w in 1024 / 64..WORDS {
            a.bits[w] = 0;
        }
        a.free_count = 1024;
    }
    let mut held = [0u32; MAX_HELD];
    let (mut hhead, mut hlen) = (0usize, 0usize);
    let mut s = Sampler::new(N);
    let mut checksum: u64 = 0;
    let mut failures: u64 = 0;
    let mut lcg = Lcg::new();

    let start = Instant::now();
    for i in 0..N {
        let x = lcg.next();
        let do_alloc = hlen == 0 || ((x >> 63) == 0 && hlen < MAX_HELD);
        let w = s.want(i);
        if w {
            s.begin();
        }
        if do_alloc {
            match a.alloc() {
                Ok(f) => {
                    held[(hhead + hlen) & (MAX_HELD - 1)] = f;
                    hlen += 1;
                    checksum = (checksum ^ f as u64).wrapping_mul(FNV);
                }
                Err(OutOfFrames) => failures += 1,
            }
        } else {
            let f = held[hhead];
            hhead = (hhead + 1) & (MAX_HELD - 1);
            hlen -= 1;
            a.free(f).expect("free");
            checksum = (checksum ^ (f as u64 | (1 << 32))).wrapping_mul(FNV);
        }
        if w {
            s.end();
        }
    }
    let ns = start.elapsed().as_nanos() as u64;

    checksum = checksum.wrapping_add(a.free_count as u64);
    assert!(fail || failures == 0);
    if fail {
        s.report_failures(N, ns, checksum, failures);
    } else {
        s.report(N, ns, checksum);
    }
}
