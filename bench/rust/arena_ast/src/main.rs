//! arena_ast: an arena (Vec) of exactly N=1,000,000 expression nodes addressed
//! by u32 index. Slot 0 is a Num(0) sentinel (the null handle, never
//! referenced); slots 1..999,999 hold a balanced binary tree built post-order
//! by build(n) (n odd): n==1 -> Num(lcg>>40); else m=n-1, l=m/2 (l-=1 if l is
//! even), r=m-l, build(l), build(r), then x=lcg: Add if x>>63 else Mul.
//! LCG seed 42; one draw per node, in allocation order. Evaluation is a
//! recursive walk in arithmetic modulo the prime M = 4294967291 (2^32-5):
//! Num -> v (v < 2^24 < M), Add -> (l + r) % M, Mul -> (l * r) % M; operands
//! are below 2^32 so no intermediate exceeds 2^64 and nothing wraps.
//! ops = N allocations; sampled operation: one allocation; the evaluation is
//! timed but not counted.
//! checksum = eval(root) + N.
#[path = "../../bench.rs"]
mod bench;

use bench::{Lcg, Sampler};
use std::time::Instant;

const N: u64 = 1_000_000;
const M: u64 = 4_294_967_291;

#[derive(Clone, Copy)]
enum Expr {
    Num(u64),
    Add(u32, u32),
    Mul(u32, u32),
}

struct Arena {
    nodes: Vec<Expr>,
    samp: Sampler,
}

impl Arena {
    fn alloc(&mut self, e: Expr) -> u32 {
        let w = self.samp.want(self.nodes.len() as u64);
        if w {
            self.samp.begin();
        }
        self.nodes.push(e);
        let id = (self.nodes.len() - 1) as u32;
        if w {
            self.samp.end();
        }
        id
    }

    fn build(&mut self, lcg: &mut Lcg, n: u64) -> u32 {
        if n == 1 {
            let v = lcg.next() >> 40;
            return self.alloc(Expr::Num(v));
        }
        let m = n - 1;
        let mut l = m / 2;
        if l % 2 == 0 {
            l -= 1;
        }
        let left = self.build(lcg, l);
        let right = self.build(lcg, m - l);
        let e = if lcg.next() >> 63 == 1 { Expr::Add(left, right) } else { Expr::Mul(left, right) };
        self.alloc(e)
    }

    fn eval(&self, id: u32) -> u64 {
        match self.nodes[id as usize] {
            Expr::Num(v) => v,
            Expr::Add(l, r) => (self.eval(l) + self.eval(r)) % M,
            Expr::Mul(l, r) => (self.eval(l) * self.eval(r)) % M,
        }
    }
}

fn main() {
    let mut a = Arena { nodes: Vec::new(), samp: Sampler::new(N) };

    let start = Instant::now();
    let mut lcg = Lcg::new();
    a.alloc(Expr::Num(0));
    let root = a.build(&mut lcg, N - 1);
    let checksum = a.eval(root) + N;
    let ns = start.elapsed().as_nanos() as u64;

    assert_eq!(a.nodes.len() as u64, N);
    a.samp.report(N, ns, checksum);
}
