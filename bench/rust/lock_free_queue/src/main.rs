//! lock_free_queue: a bounded single-producer single-consumer ring of 4096
//! u64 slots. head (consumer) and tail (producer) are monotonically
//! increasing u64 atomics on separate cache lines; each side keeps its own
//! index in a private counter (it is the sole writer) plus a cached copy of
//! the other side's index, and publishes its own index with a Release store
//! every B = 64 elements and before waiting (B-Queue style batching: without
//! it the two threads fall into a lock-step regime where every release store
//! stalls on the line the other thread is polling, 20-40x slower on Apple
//! silicon). push: if tail - head_cache == 4096, re-read head with Acquire;
//! still full -> return false; write slots[tail & 4095]; tail += 1; publish
//! when tail % 64 == 0. pop mirrors it with head/tail swapped, re-reading
//! tail with Acquire only when the cached copy says empty. The main thread
//! is the producer: element i is the LCG's i-th value; N=8,000,000 elements,
//! each pushed until push succeeds (publish, spin, retry). The consumer
//! thread pops every element (publish, spin, retry while empty) and chains
//! cs = (cs XOR v) * 0x100000001b3 (wrapping) from 0. ops = N; the sampled
//! operation is the consumer's i-th pop (including any wait). Timed: spawn
//! through join; threads = 2. checksum = cs + number of popped elements.
//! KERN_BENCH_FAIL=1: the consumer is held at a gate while the producer
//! attempts elements 0..5119 once each; the 1024 pushes on the full ring
//! return false (failures = 1024; those elements are dropped), then tail is
//! published, the gate opens and elements 5120..N-1 follow as in the normal
//! run; the consumer pops N - 1024 elements.
//!
//! The slots live in an UnsafeCell shared by both threads; the two
//! dereferences and the Sync assertion are the only lines that need the
//! keyword (three in all).
#[path = "../../bench.rs"]
mod bench;

use bench::{fail_mode, Lcg, Sampler};
use std::cell::UnsafeCell;
use std::hint::spin_loop;
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::thread;
use std::time::Instant;

const N: u64 = 8_000_000;
const CAP: u64 = 4096;
const B: u64 = 64;
const FNV: u64 = 0x100000001b3;
const FAIL_PREFIX: u64 = 5120;

#[repr(align(128))]
struct Padded<T>(T);

struct Spsc {
    slots: UnsafeCell<[u64; CAP as usize]>,
    head: Padded<AtomicU64>,
    tail: Padded<AtomicU64>,
    go: Padded<AtomicBool>,
    expected: AtomicU64,
}

// SAFETY: slot i is written only by the producer before it publishes tail
// past i (Release) and read only by the consumer after it observed that tail
// (Acquire), and a slot is never reused until the consumer published head
// past it; so no slot is accessed by both threads at once.
unsafe impl Sync for Spsc {}

/// Side is one thread's private index and its cached copy of the other
/// thread's published index.
#[derive(Default)]
struct Side {
    pos: u64,
    cache: u64,
}

impl Spsc {
    /// push returns false when the ring is full.
    #[inline]
    fn push(&self, p: &mut Side, v: u64) -> bool {
        if p.pos - p.cache == CAP {
            p.cache = self.head.0.load(Ordering::Acquire);
            if p.pos - p.cache == CAP {
                return false;
            }
        }
        // SAFETY: see the Sync impl; this slot is free and ours until the
        // Release store that publishes it.
        unsafe { (*self.slots.get())[(p.pos & (CAP - 1)) as usize] = v };
        p.pos += 1;
        if p.pos & (B - 1) == 0 {
            self.tail.0.store(p.pos, Ordering::Release);
        }
        true
    }

    #[inline]
    fn publish_tail(&self, p: &Side) {
        self.tail.0.store(p.pos, Ordering::Release);
    }

    /// pop returns None when the ring is empty.
    #[inline]
    fn pop(&self, c: &mut Side) -> Option<u64> {
        if c.pos == c.cache {
            c.cache = self.tail.0.load(Ordering::Acquire);
            if c.pos == c.cache {
                return None;
            }
        }
        // SAFETY: see the Sync impl; the producer published this slot.
        let v = unsafe { (*self.slots.get())[(c.pos & (CAP - 1)) as usize] };
        c.pos += 1;
        if c.pos & (B - 1) == 0 {
            self.head.0.store(c.pos, Ordering::Release);
        }
        Some(v)
    }

    #[inline]
    fn publish_head(&self, c: &Side) {
        self.head.0.store(c.pos, Ordering::Release);
    }
}

fn consumer(q: &Spsc, mut s: Sampler) -> (u64, u64, Sampler) {
    while !q.go.0.load(Ordering::Acquire) {
        spin_loop();
    }
    let expected = q.expected.load(Ordering::Relaxed);
    let mut cs = 0u64;
    let mut c = Side::default();
    for i in 0..expected {
        let w = s.want(i);
        if w {
            s.begin();
        }
        let v = loop {
            if let Some(v) = q.pop(&mut c) {
                break v;
            }
            q.publish_head(&c);
            spin_loop();
        };
        if w {
            s.end();
        }
        cs = (cs ^ v).wrapping_mul(FNV);
    }
    q.publish_head(&c);
    (cs, expected, s)
}

fn main() {
    let fail = fail_mode();
    let q = Spsc {
        slots: UnsafeCell::new([0; CAP as usize]),
        head: Padded(AtomicU64::new(0)),
        tail: Padded(AtomicU64::new(0)),
        go: Padded(AtomicBool::new(false)),
        expected: AtomicU64::new(0),
    };
    let mut lcg = Lcg::new();
    let mut p = Side::default();
    let (mut failures, mut first) = (0u64, 0u64);

    let start = Instant::now();
    let (cs, popped, mut s) = thread::scope(|sc| {
        let h = sc.spawn(|| consumer(&q, Sampler::new(N)));
        if fail {
            for _ in 0..FAIL_PREFIX {
                if !q.push(&mut p, lcg.next()) {
                    failures += 1;
                }
            }
            q.publish_tail(&p);
            first = FAIL_PREFIX;
        }
        q.expected.store(N - failures, Ordering::Relaxed);
        q.go.0.store(true, Ordering::Release);
        for _ in first..N {
            let v = lcg.next();
            while !q.push(&mut p, v) {
                q.publish_tail(&p);
                spin_loop();
            }
        }
        q.publish_tail(&p);
        h.join().unwrap()
    });
    let ns = start.elapsed().as_nanos() as u64;

    assert_eq!(popped, N - failures);
    assert!(!fail || failures == FAIL_PREFIX - CAP);
    let checksum = cs.wrapping_add(popped);
    if fail {
        s.report_failures(N, ns, checksum, failures);
    } else {
        s.report(N, ns, checksum);
    }
}
