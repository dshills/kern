//! btree: N=2,000,000 distinct u64 keys from the LCG (seed 42), value = insert
//! index i, inserted into an order-16 B-tree (15 keys, 16 children per node,
//! min degree 8) whose nodes live in a growable pool (Vec) addressed by u32
//! index. Insert is the CLRS preemptive algorithm: a full root is split before
//! the descent and a full child is split (median key index 7 moves up, the
//! right node takes keys 8..14) before stepping into it. Then an in-order
//! traversal. ops = N; sampled operation: one insert; the traversal is timed
//! but not counted.
//! checksum = (wrapping sum over in-order position p of (key XOR p) + value)
//!            XOR number of pool nodes.
#[path = "../../bench.rs"]
mod bench;

use bench::{Lcg, Sampler};
use std::time::Instant;

const N: u64 = 2_000_000;
const MAXK: usize = 15;
const T: usize = 8;

#[derive(Clone, Copy)]
struct Node {
    keys: [u64; MAXK],
    vals: [u64; MAXK],
    children: [u32; MAXK + 1],
    count: usize,
    leaf: bool,
}

impl Node {
    fn empty(leaf: bool) -> Node {
        Node { keys: [0; MAXK], vals: [0; MAXK], children: [0; MAXK + 1], count: 0, leaf }
    }
}

struct BTree {
    nodes: Vec<Node>,
    root: u32,
}

impl BTree {
    fn alloc(&mut self, leaf: bool) -> u32 {
        self.nodes.push(Node::empty(leaf));
        (self.nodes.len() - 1) as u32
    }

    /// split_child splits the full child i of parent p.
    fn split_child(&mut self, p: u32, i: usize) {
        let l = self.nodes[p as usize].children[i] as usize;
        let r = self.alloc(self.nodes[l].leaf) as usize;
        let left = self.nodes[l];
        let right = &mut self.nodes[r];
        right.count = T - 1;
        right.keys[..T - 1].copy_from_slice(&left.keys[T..]);
        right.vals[..T - 1].copy_from_slice(&left.vals[T..]);
        if !left.leaf {
            right.children[..T].copy_from_slice(&left.children[T..]);
        }
        self.nodes[l].count = T - 1;
        let par = &mut self.nodes[p as usize];
        let c = par.count;
        par.children.copy_within(i + 1..c + 1, i + 2);
        par.children[i + 1] = r as u32;
        par.keys.copy_within(i..c, i + 1);
        par.vals.copy_within(i..c, i + 1);
        par.keys[i] = left.keys[T - 1];
        par.vals[i] = left.vals[T - 1];
        par.count += 1;
    }

    fn insert(&mut self, key: u64, val: u64) {
        if self.nodes[self.root as usize].count == MAXK {
            let s = self.alloc(false);
            self.nodes[s as usize].children[0] = self.root;
            self.root = s;
            self.split_child(s, 0);
        }
        let mut x = self.root;
        loop {
            let n = &mut self.nodes[x as usize];
            let mut i = n.count;
            while i > 0 && key < n.keys[i - 1] {
                i -= 1;
            }
            if i > 0 && n.keys[i - 1] == key {
                n.vals[i - 1] = val;
                return;
            }
            if n.leaf {
                let c = n.count;
                n.keys.copy_within(i..c, i + 1);
                n.vals.copy_within(i..c, i + 1);
                n.keys[i] = key;
                n.vals[i] = val;
                n.count += 1;
                return;
            }
            let child = n.children[i];
            if self.nodes[child as usize].count == MAXK {
                self.split_child(x, i);
                let n = &mut self.nodes[x as usize];
                if key == n.keys[i] {
                    n.vals[i] = val;
                    return;
                }
                if key > n.keys[i] {
                    i += 1;
                }
                x = n.children[i];
            } else {
                x = child;
            }
        }
    }

    fn traverse(&self, id: u32, checksum: &mut u64, pos: &mut u64) {
        let n = &self.nodes[id as usize];
        for i in 0..n.count {
            if !n.leaf {
                self.traverse(n.children[i], checksum, pos);
            }
            *checksum = checksum.wrapping_add(n.keys[i] ^ *pos).wrapping_add(n.vals[i]);
            *pos += 1;
        }
        if !n.leaf {
            self.traverse(n.children[n.count], checksum, pos);
        }
    }
}

fn main() {
    let mut t = BTree { nodes: Vec::new(), root: 0 };
    t.root = t.alloc(true);
    let mut s = Sampler::new(N);
    let (mut checksum, mut pos) = (0u64, 0u64);

    let start = Instant::now();
    let mut lcg = Lcg::new();
    for i in 0..N {
        let key = lcg.next();
        let w = s.want(i);
        if w {
            s.begin();
        }
        t.insert(key, i);
        if w {
            s.end();
        }
    }
    t.traverse(t.root, &mut checksum, &mut pos);
    checksum ^= t.nodes.len() as u64;
    let ns = start.elapsed().as_nanos() as u64;

    assert_eq!(pos, N);
    s.report(N, ns, checksum);
}
