//! lock_protected_map: the hash_table map (open addressing, linear probing,
//! Fibonacci hash, states/keys/vals, initial capacity 16, grow at 3/4) behind
//! one std Mutex, shared by 4 scoped threads. Untimed setup inserts keys
//! 0..K-1 (K = 262,144) with value key * 0x9E3779B97F4A7C15 (wrapping).
//! Thread t (0..3) runs its own LCG with seed 42 + t for 1,000,000 ops:
//! x = next(); x >> 62 == 0 (25%): insert key K + (x >> 46) with the same
//! value rule, cs += key; otherwise look up key x >> 46 (always present;
//! abort if not), cs += value XOR key. Each op holds the lock once.
//! ops = 4,000,000; the sampled operation is thread 0's i-th op (lock, op,
//! unlock). Timed: spawning through joining; threads = 4.
//! checksum = wrapping sum of the four per-thread cs, plus the final count.
#[path = "../../bench.rs"]
mod bench;

use bench::{Sampler, LCG_SEED, Lcg};
use std::sync::Mutex;
use std::thread;
use std::time::Instant;

const N: u64 = 4_000_000;
const THREADS: u64 = 4;
const PER_THREAD: u64 = N / THREADS;
const K: u64 = 262_144;
const GOLD: u64 = 0x9E3779B97F4A7C15;
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
        let mut i = (key.wrapping_mul(GOLD) >> (64 - self.bits)) as usize;
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

fn worker(m: &Mutex<Map>, id: u64, mut s: Option<Sampler>) -> (u64, Option<Sampler>) {
    let mut lcg = Lcg(LCG_SEED + id);
    let mut cs: u64 = 0;
    for i in 0..PER_THREAD {
        let x = lcg.next();
        let w = s.as_ref().is_some_and(|s| s.want(i));
        if w {
            s.as_mut().unwrap().begin();
        }
        if (x >> 62) == 0 {
            let key = K + (x >> 46);
            m.lock().unwrap().insert(key, key.wrapping_mul(GOLD));
            cs = cs.wrapping_add(key);
        } else {
            let key = x >> 46;
            let val = m.lock().unwrap().find(key).expect("key missing");
            cs = cs.wrapping_add(val ^ key);
        }
        if w {
            s.as_mut().unwrap().end();
        }
    }
    (cs, s)
}

fn main() {
    let mut map = Map::new(4);
    for k in 0..K {
        map.insert(k, k.wrapping_mul(GOLD));
    }
    let m = Mutex::new(map);

    let start = Instant::now();
    let (mut checksum, mut sampler) = (0u64, None);
    thread::scope(|sc| {
        let handles: Vec<_> = (0..THREADS)
            .map(|t| {
                let s = if t == 0 { Some(Sampler::new(N)) } else { None };
                let m = &m;
                sc.spawn(move || worker(m, t, s))
            })
            .collect();
        for h in handles {
            let (cs, s) = h.join().unwrap();
            checksum = checksum.wrapping_add(cs);
            if s.is_some() {
                sampler = s;
            }
        }
    });
    let ns = start.elapsed().as_nanos() as u64;

    checksum = checksum.wrapping_add(m.lock().unwrap().count as u64);
    sampler.expect("thread 0 sampler").report(N, ns, checksum);
}
