//! Timing, latency sampling, output and LCG helpers shared by the Rust
//! benchmark programs (bench/PROTOCOL.md). Every program prints exactly:
//!   ops=<N> ns=<elapsed> p50=<ns/op> p99=<ns/op> checksum=<u64>
//! Per-op latency is sampled every ops/4096 operations (at most 4096
//! samples); p50 = sorted[n/2], p99 = sorted[n*99/100].
#![allow(dead_code)]

use std::time::Instant;

pub const MAX_SAMPLES: usize = 4096;
pub const LCG_SEED: u64 = 42;

/// The 64-bit LCG shared by all implementations: x = x*a + c, seed 42.
pub struct Lcg(pub u64);

impl Lcg {
    pub fn new() -> Lcg {
        Lcg(LCG_SEED)
    }
    #[inline]
    pub fn next(&mut self) -> u64 {
        self.0 = self
            .0
            .wrapping_mul(6364136223846793005)
            .wrapping_add(1442695040888963407);
        self.0
    }
}

pub struct Sampler {
    samples: Vec<u64>,
    stride: u64,
    t0: Instant,
}

impl Sampler {
    pub fn new(ops: u64) -> Sampler {
        let stride = (ops / MAX_SAMPLES as u64).max(1);
        Sampler { samples: Vec::with_capacity(MAX_SAMPLES), stride, t0: Instant::now() }
    }
    /// want reports whether operation i is a sampled one.
    #[inline]
    pub fn want(&self, i: u64) -> bool {
        i % self.stride == 0 && self.samples.len() < MAX_SAMPLES
    }
    #[inline]
    pub fn begin(&mut self) {
        self.t0 = Instant::now();
    }
    #[inline]
    pub fn end(&mut self) {
        self.samples.push(self.t0.elapsed().as_nanos() as u64);
    }
    fn line(&mut self, ops: u64, ns: u64, checksum: u64) -> String {
        let (mut p50, mut p99) = (0, 0);
        if !self.samples.is_empty() {
            self.samples.sort_unstable();
            let n = self.samples.len();
            p50 = self.samples[n / 2];
            p99 = self.samples[n * 99 / 100];
        }
        format!("ops={} ns={} p50={} p99={} checksum={}", ops, ns, p50, p99, checksum)
    }
    pub fn report(&mut self, ops: u64, ns: u64, checksum: u64) {
        println!("{}", self.line(ops, ns, checksum));
    }
    /// report_failures prints the protocol line with the ` failures=<N>`
    /// field that failure-path runs (KERN_BENCH_FAIL=1) add.
    pub fn report_failures(&mut self, ops: u64, ns: u64, checksum: u64, failures: u64) {
        println!("{} failures={}", self.line(ops, ns, checksum), failures);
    }
}

/// fail_mode reports whether KERN_BENCH_FAIL=1 is set (PROTOCOL.md
/// failure-path run).
pub fn fail_mode() -> bool {
    std::env::var("KERN_BENCH_FAIL").map(|v| v == "1").unwrap_or(false)
}
