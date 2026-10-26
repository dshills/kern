//! ring_buffer: a 1024-slot ring of u64 (inline array, head/tail/len, index
//! masked with 1023). Untimed prefill pushes the values 0..511. Then
//! N=50,000,000 operations; operation i pushes 512+i and pops one value
//! (which is i). ops = N; sampled operation: one push+pop pair.
//! checksum = FNV-style chain over popped values p:
//!   cs = (cs XOR p) * 0x100000001b3 (wrapping), starting from 0.
#[path = "../../bench.rs"]
mod bench;

use bench::Sampler;
use std::time::Instant;

const N: u64 = 50_000_000;
const CAP: usize = 1024;

struct Ring {
    slots: [u64; CAP],
    head: usize,
    tail: usize,
    len: usize,
}

impl Ring {
    /// push hands the value back when the ring is full.
    #[inline]
    fn push(&mut self, v: u64) -> Result<(), u64> {
        if self.len == CAP {
            return Err(v);
        }
        self.slots[self.tail] = v;
        self.tail = (self.tail + 1) & (CAP - 1);
        self.len += 1;
        Ok(())
    }

    #[inline]
    fn pop(&mut self) -> Option<u64> {
        if self.len == 0 {
            return None;
        }
        let v = self.slots[self.head];
        self.head = (self.head + 1) & (CAP - 1);
        self.len -= 1;
        Some(v)
    }
}

fn main() {
    let mut r = Ring { slots: [0; CAP], head: 0, tail: 0, len: 0 };
    for i in 0..512u64 {
        r.push(i).expect("prefill");
    }
    let mut s = Sampler::new(N);
    let mut checksum: u64 = 0;

    let start = Instant::now();
    for i in 0..N {
        let w = s.want(i);
        if w {
            s.begin();
        }
        r.push(512 + i).expect("full");
        let p = r.pop().expect("empty");
        if w {
            s.end();
        }
        checksum = (checksum ^ p).wrapping_mul(0x100000001b3);
    }
    let ns = start.elapsed().as_nanos() as u64;

    assert_eq!(r.len, 512);
    s.report(N, ns, checksum);
}
