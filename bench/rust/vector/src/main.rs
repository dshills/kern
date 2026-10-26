//! vector: 4 rounds; each round pushes PER=5,000,000 u64 values into a Vec
//! that is cleared (capacity kept) at the start of the round, then sums the
//! elements through a slice view. Pushed value for global push index
//! g = r*PER + k is g*3. ops = 20,000,000 pushes; the sum passes are timed but
//! not counted as ops. Sampled operation: one push.
//! checksum = wrapping sum of the four round sums.
#[path = "../../bench.rs"]
mod bench;

use bench::Sampler;
use std::time::Instant;

const ROUNDS: u64 = 4;
const PER: u64 = 5_000_000;
const OPS: u64 = ROUNDS * PER;

/// sum iterates a view (slice) of the vector.
fn sum(items: &[u64]) -> u64 {
    items.iter().fold(0u64, |acc, &x| acc.wrapping_add(x))
}

fn main() {
    let mut v: Vec<u64> = Vec::new();
    let mut s = Sampler::new(OPS);
    let mut checksum: u64 = 0;

    let start = Instant::now();
    for r in 0..ROUNDS {
        v.clear();
        for k in 0..PER {
            let g = r * PER + k;
            let w = s.want(g);
            if w {
                s.begin();
            }
            v.push(g * 3);
            if w {
                s.end();
            }
        }
        checksum = checksum.wrapping_add(sum(&v));
    }
    let ns = start.elapsed().as_nanos() as u64;

    assert_eq!(v[0], (ROUNDS - 1) * PER * 3);
    s.report(OPS, ns, checksum);
}
