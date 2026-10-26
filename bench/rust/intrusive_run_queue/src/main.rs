//! intrusive_run_queue: 1024 tasks in an array; each task embeds next/prev
//! links (raw pointers), a u64 prio and a u32 id. At the start task i has id
//! i, prio i and the queue links them in index order (head = task 0), so the
//! queue is non-decreasing in prio from head to tail. N=10,000,000 ops; op i
//! pops the head, adds 1024 + (x >> 58) to its prio (x from the LCG), and
//! re-inserts it by walking prev from the tail while the node's prio is
//! greater than the new prio, inserting after the node found (at the head
//! when none). Per op cs += id XOR new prio (wrapping). After the loop the
//! queue is walked from the head with position k = 0, 1, ...:
//! cs += (k+1) * (id+1).
//!
//! The links are raw pointers into a boxed slice that is never moved or
//! dropped while the queue is in use; every dereference is inside one of the
//! four keyword blocks below (pop, insert, the priority bump, the final
//! walk), the intrusive form of docs/workloads.
#[path = "../../bench.rs"]
mod bench;

use bench::{Lcg, Sampler};
use std::ptr;
use std::time::Instant;

const N: u64 = 10_000_000;
const TASKS: usize = 1024;

struct Task {
    next: *mut Task,
    prev: *mut Task,
    prio: u64,
    id: u32,
}

struct RunQueue {
    head: *mut Task,
    tail: *mut Task,
    len: usize,
}

impl RunQueue {
    /// pop unlinks and returns the head, or null when the queue is empty.
    #[inline]
    fn pop(&mut self) -> *mut Task {
        let t = self.head;
        if t.is_null() {
            return t;
        }
        // SAFETY: t and its successor are live tasks of the one slice the
        // queue links (see the module comment); no other reference is live.
        unsafe {
            self.head = (*t).next;
            if self.head.is_null() {
                self.tail = ptr::null_mut();
            } else {
                (*self.head).prev = ptr::null_mut();
            }
            (*t).next = ptr::null_mut();
            (*t).prev = ptr::null_mut();
        }
        self.len -= 1;
        t
    }

    /// insert places t after the last node (from the tail) whose prio is not
    /// greater than t's.
    #[inline]
    fn insert(&mut self, t: *mut Task) {
        // SAFETY: as in pop; t is unlinked and distinct from every node.
        unsafe {
            let mut p = self.tail;
            while !p.is_null() && (*p).prio > (*t).prio {
                p = (*p).prev;
            }
            (*t).prev = p;
            if p.is_null() {
                (*t).next = self.head;
                self.head = t;
            } else {
                (*t).next = (*p).next;
                (*p).next = t;
            }
            if (*t).next.is_null() {
                self.tail = t;
            } else {
                (*(*t).next).prev = t;
            }
        }
        self.len += 1;
    }
}

fn main() {
    let mut tasks: Box<[Task]> = (0..TASKS as u32)
        .map(|i| Task { next: ptr::null_mut(), prev: ptr::null_mut(), prio: i as u64, id: i })
        .collect();
    let mut q = RunQueue { head: ptr::null_mut(), tail: ptr::null_mut(), len: 0 };
    for t in tasks.iter_mut() {
        q.insert(t);
    }
    let mut s = Sampler::new(N);
    let mut checksum: u64 = 0;
    let mut lcg = Lcg::new();

    let start = Instant::now();
    for i in 0..N {
        let x = lcg.next();
        let delta = 1024 + (x >> 58);
        let w = s.want(i);
        if w {
            s.begin();
        }
        let t = q.pop();
        assert!(!t.is_null(), "empty");
        // SAFETY: t was just popped from the queue and is a live task.
        let (id, prio) = unsafe {
            (*t).prio += delta;
            ((*t).id, (*t).prio)
        };
        q.insert(t);
        if w {
            s.end();
        }
        checksum = checksum.wrapping_add(id as u64 ^ prio);
    }
    let ns = start.elapsed().as_nanos() as u64;

    assert_eq!(q.len, TASKS);
    let mut k: u64 = 0;
    let mut t = q.head;
    while !t.is_null() {
        // SAFETY: walking the live linked tasks; no mutation during the walk.
        let (id, next) = unsafe { ((*t).id, (*t).next) };
        checksum = checksum.wrapping_add((k + 1).wrapping_mul(id as u64 + 1));
        k += 1;
        t = next;
    }
    assert_eq!(k as usize, TASKS);
    drop(tasks);
    s.report(N, ns, checksum);
}
