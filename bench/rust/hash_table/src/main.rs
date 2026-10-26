//! hash_table: N=5,000,000 keys from the LCG (seed 42; key = next value).
//! Insert (key, value=i) into an open-addressing table (linear probing,
//! Fibonacci hash h = (key*0x9E3779B97F4A7C15) >> (64-bits), structure of
//! arrays states/keys/vals, initial capacity 16, doubled before an insert
//! when count*4 >= cap*3), then look up every key in insertion order (the LCG
//! is re-run from the seed). ops = N; sampled operation: one insert; the
//! lookup pass is timed but not counted.
//! checksum = wrapping sum over lookups of (value XOR key), plus count.
#[path = "../../bench.rs"]
mod bench;

use bench::{Lcg, Sampler};
use std::time::Instant;

const N: u64 = 5_000_000;
const EMPTY: u8 = 0;
const FULL: u8 = 2;

struct Map {
    states: Vec<u8>,
    keys: Vec<u64>,
    vals: Vec<u64>,
    count: usize,
    bits: u32,
}

impl Map {
    fn new(bits: u32) -> Map {
        let cap = 1usize << bits;
        Map { states: vec![EMPTY; cap], keys: vec![0; cap], vals: vec![0; cap], count: 0, bits }
    }

    #[inline]
    fn probe(&self, key: u64) -> usize {
        let mask = self.keys.len() - 1;
        let mut i = (key.wrapping_mul(0x9E3779B97F4A7C15) >> (64 - self.bits)) as usize;
        while self.states[i] == FULL && self.keys[i] != key {
            i = (i + 1) & mask;
        }
        i
    }

    fn grow(&mut self) {
        let mut n = Map::new(self.bits + 1);
        for i in 0..self.keys.len() {
            if self.states[i] == FULL {
                n.insert(self.keys[i], self.vals[i]);
            }
        }
        *self = n;
    }

    fn insert(&mut self, key: u64, val: u64) {
        if self.count * 4 >= self.keys.len() * 3 {
            self.grow();
        }
        let i = self.probe(key);
        if self.states[i] == FULL {
            self.vals[i] = val;
            return;
        }
        self.states[i] = FULL;
        self.keys[i] = key;
        self.vals[i] = val;
        self.count += 1;
    }

    #[inline]
    fn find(&self, key: u64) -> Option<u64> {
        let i = self.probe(key);
        if self.states[i] != FULL {
            return None;
        }
        Some(self.vals[i])
    }
}

fn main() {
    let mut m = Map::new(4);
    let mut s = Sampler::new(N);
    let mut checksum: u64 = 0;

    let start = Instant::now();
    let mut lcg = Lcg::new();
    for i in 0..N {
        let key = lcg.next();
        let w = s.want(i);
        if w {
            s.begin();
        }
        m.insert(key, i);
        if w {
            s.end();
        }
    }
    let mut lcg = Lcg::new();
    for _ in 0..N {
        let key = lcg.next();
        let val = m.find(key).expect("key missing");
        checksum = checksum.wrapping_add(val ^ key);
    }
    checksum = checksum.wrapping_add(m.count as u64);
    let ns = start.elapsed().as_nanos() as u64;

    s.report(N, ns, checksum);
}
