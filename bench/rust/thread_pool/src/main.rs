//! thread_pool: 4 worker threads (std::thread::scope) pull jobs from a
//! mutex-protected FIFO ring of 2048 job indices with two condition variables
//! (ready, space). The main thread feeds N=1,000,000 jobs in batches of 1024
//! (the last batch has 576): it waits until the ring holds at most 1024 jobs,
//! pushes the batch, notifies ready. Job b sums the 64 words of block
//! b & 4095 of a 4096-block input (word k of the whole input =
//! k * 0x9E3779B97F4A7C15, wrapping) and yields sum XOR b. A worker adds its
//! previous job's result to the shared total under the queue mutex when it
//! takes its next job (or before exiting); when a pop leaves exactly 1024
//! jobs it signals space. ops = N jobs; the sampled operation is worker 0's
//! i-th job (lock, pop, compute) with i its own counter. Timed: feeding
//! through joining; threads = 4 workers + main.
//! checksum = total (wrapping sum of the job results) + N.
#[path = "../../bench.rs"]
mod bench;

use bench::Sampler;
use std::sync::{Condvar, Mutex};
use std::thread;
use std::time::Instant;

const N: u64 = 1_000_000;
const WORKERS: usize = 4;
const BATCH: usize = 1024;
const QCAP: usize = 2048;
const BLOCKS: usize = 4096;
const WORDS: usize = 64;

struct State {
    ring: Vec<u64>,
    head: usize,
    len: usize,
    total: u64,
    done_jobs: u64,
    done: bool,
}

struct Queue {
    mu: Mutex<State>,
    ready: Condvar,
    space: Condvar,
}

#[inline]
fn run_job(words: &[u64], b: u64) -> u64 {
    let blk = &words[(b as usize & (BLOCKS - 1)) * WORDS..][..WORDS];
    blk.iter().fold(0u64, |s, &w| s.wrapping_add(w)) ^ b
}

fn worker(q: &Queue, words: &[u64], mut s: Option<Sampler>) -> Option<Sampler> {
    let mut pending = None;
    let mut i: u64 = 0;
    loop {
        let w = s.as_ref().is_some_and(|s| s.want(i));
        if w {
            s.as_mut().unwrap().begin();
        }
        let mut st = q.mu.lock().unwrap();
        if let Some(r) = pending.take() {
            st.total = st.total.wrapping_add(r);
            st.done_jobs += 1;
        }
        while st.len == 0 && !st.done {
            st = q.ready.wait(st).unwrap();
        }
        if st.len == 0 {
            return s;
        }
        let b = st.ring[st.head];
        st.head = (st.head + 1) & (QCAP - 1);
        st.len -= 1;
        if st.len == BATCH {
            q.space.notify_one();
        }
        drop(st);
        pending = Some(run_job(words, b));
        if w {
            s.as_mut().unwrap().end();
        }
        i += 1;
    }
}

fn main() {
    let words: Vec<u64> = (0..(BLOCKS * WORDS) as u64).map(|k| k.wrapping_mul(0x9E3779B97F4A7C15)).collect();
    let q = Queue {
        mu: Mutex::new(State { ring: vec![0; QCAP], head: 0, len: 0, total: 0, done_jobs: 0, done: false }),
        ready: Condvar::new(),
        space: Condvar::new(),
    };

    let start = Instant::now();
    let sampler = thread::scope(|sc| {
        let handles: Vec<_> = (0..WORKERS)
            .map(|t| {
                let s = if t == 0 { Some(Sampler::new(N)) } else { None };
                let (q, words) = (&q, &words);
                sc.spawn(move || worker(q, words, s))
            })
            .collect();
        let mut b: u64 = 0;
        while b < N {
            let n = (N - b).min(BATCH as u64);
            let mut st = q.mu.lock().unwrap();
            while st.len > BATCH {
                st = q.space.wait(st).unwrap();
            }
            for k in 0..n {
                let slot = (st.head + st.len) & (QCAP - 1);
                st.ring[slot] = b + k;
                st.len += 1;
            }
            q.ready.notify_all();
            drop(st);
            b += n;
        }
        q.mu.lock().unwrap().done = true;
        q.ready.notify_all();
        let mut sampler = None;
        for h in handles {
            if let Some(s) = h.join().unwrap() {
                sampler = Some(s);
            }
        }
        sampler
    });
    let ns = start.elapsed().as_nanos() as u64;

    let st = q.mu.lock().unwrap();
    assert_eq!(st.done_jobs, N);
    let checksum = st.total.wrapping_add(N);
    sampler.expect("worker 0 sampler").report(N, ns, checksum);
}
