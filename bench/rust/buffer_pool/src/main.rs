//! buffer_pool: 256 frames of 4 KiB over a 1024-page in-memory backing store.
//! Store page p holds p (LE u64) in bytes 0..8 and a counter (LE u64, 0) in
//! bytes 8..16. The page table is a direct-mapped array page -> frame.
//! N=5,000,000 ops; op i draws x from the LCG, page p = (x>>32)&127 when
//! x>>56 < 224, else (x>>32)&1023; pins p (a miss loads it into the unpinned
//! frame with the smallest last-unpin tick, lowest index on ties, writing the
//! victim back when dirty), verifies bytes 0..8 == p, increments the counter,
//! marks the frame dirty, unpins (last = ++tick). Per op cs += p | (f << 16).
//! After the loop every dirty frame is flushed; cs += sum over pages of
//! counter(p) * (p + 1), then cs += evictions. KERN_BENCH_FAIL=1: pages 0..255
//! are pinned in setup and never released, so a miss finds no victim and
//! returns an error (failures += 1, no checksum contribution).
#[path = "../../bench.rs"]
mod bench;

use bench::{fail_mode, Lcg, Sampler};
use std::time::Instant;

const N: u64 = 5_000_000;
const PAGES: usize = 1024;
const FRAMES: usize = 256;
const PAGE_SIZE: usize = 4096;
const NONE: u32 = u32::MAX;

struct Frame {
    data: [u8; PAGE_SIZE],
    page: u64, // u64::MAX when empty
    last: u64, // tick of the last unpin; 0 = never
    pins: u32,
    dirty: bool,
}

struct Pool {
    frames: Vec<Frame>,
    table: [u32; PAGES],
    tick: u64,
    evictions: u64,
    store: Vec<[u8; PAGE_SIZE]>,
}

#[derive(Debug)]
struct AllPinned;

#[inline]
fn get_u64(p: &[u8]) -> u64 {
    u64::from_le_bytes(p[..8].try_into().unwrap())
}

#[inline]
fn put_u64(p: &mut [u8], v: u64) {
    p[..8].copy_from_slice(&v.to_le_bytes());
}

impl Pool {
    fn new() -> Pool {
        let mut store = vec![[0u8; PAGE_SIZE]; PAGES];
        for (p, page) in store.iter_mut().enumerate() {
            put_u64(page, p as u64);
        }
        let frames = (0..FRAMES)
            .map(|_| Frame { data: [0; PAGE_SIZE], page: u64::MAX, last: 0, pins: 0, dirty: false })
            .collect();
        Pool { frames, table: [NONE; PAGES], tick: 0, evictions: 0, store }
    }

    /// pin returns the frame holding page p (loading it on a miss), or an
    /// error when every frame is pinned.
    fn pin(&mut self, p: u64) -> Result<usize, AllPinned> {
        let f = self.table[p as usize];
        if f != NONE {
            self.frames[f as usize].pins += 1;
            return Ok(f as usize);
        }
        let mut victim = None;
        let mut best = u64::MAX;
        for (i, fr) in self.frames.iter().enumerate() {
            if fr.pins == 0 && fr.last < best {
                best = fr.last;
                victim = Some(i);
            }
        }
        let v = victim.ok_or(AllPinned)?;
        let fr = &mut self.frames[v];
        if fr.page != u64::MAX {
            self.table[fr.page as usize] = NONE;
            if fr.dirty {
                self.store[fr.page as usize].copy_from_slice(&fr.data);
            }
            fr.dirty = false;
            self.evictions += 1;
        }
        fr.data.copy_from_slice(&self.store[p as usize]);
        fr.page = p;
        fr.pins = 1;
        self.table[p as usize] = v as u32;
        Ok(v)
    }

    #[inline]
    fn unpin(&mut self, f: usize) {
        self.tick += 1;
        self.frames[f].pins -= 1;
        self.frames[f].last = self.tick;
    }

    fn flush(&mut self) {
        for fr in self.frames.iter_mut() {
            if fr.page != u64::MAX && fr.dirty {
                self.store[fr.page as usize].copy_from_slice(&fr.data);
            }
        }
    }
}

fn main() {
    let fail = fail_mode();
    let mut pool = Pool::new();
    if fail {
        for p in 0..FRAMES as u64 {
            assert_eq!(pool.pin(p).expect("setup pin"), p as usize);
        }
    }
    let mut s = Sampler::new(N);
    let mut checksum: u64 = 0;
    let mut failures: u64 = 0;
    let mut lcg = Lcg::new();

    let start = Instant::now();
    for i in 0..N {
        let x = lcg.next();
        let p = if (x >> 56) < 224 { (x >> 32) & 127 } else { (x >> 32) & 1023 };
        let w = s.want(i);
        if w {
            s.begin();
        }
        let r = pool.pin(p);
        if let Ok(f) = r {
            let d = &mut pool.frames[f].data;
            assert_eq!(get_u64(d), p, "page number");
            let c = get_u64(&d[8..]) + 1;
            put_u64(&mut d[8..], c);
            pool.frames[f].dirty = true;
            pool.unpin(f);
        }
        if w {
            s.end();
        }
        match r {
            Ok(f) => checksum = checksum.wrapping_add(p | ((f as u64) << 16)),
            Err(_) => failures += 1,
        }
    }
    let ns = start.elapsed().as_nanos() as u64;

    pool.flush();
    for (p, page) in pool.store.iter().enumerate() {
        checksum = checksum.wrapping_add(get_u64(&page[8..]).wrapping_mul(p as u64 + 1));
    }
    checksum = checksum.wrapping_add(pool.evictions);
    assert!(fail || failures == 0);
    if fail {
        s.report_failures(N, ns, checksum, failures);
    } else {
        s.report(N, ns, checksum);
    }
}
