# kern — References

Primary specifications and papers for every reference direction named in spec §14 and in
`ARCHITECTURE_EVALUATION.md` §11. ADRs under `docs/decisions/` and the workload notes under
`docs/workloads/` cite entries from this file by the bracketed key; a lint test fails on a
citation whose key is not listed here (PLAN.md 0.1a).

Conventions: one entry per key; URLs given in `ARCHITECTURE_EVALUATION.md` §11 are reproduced
as cited there. An entry marked "(verify)" carries a URL, DOI, author list, or edition that
has not been checked against the primary source and must be confirmed before the research
report (PLAN.md 4.3) cites it. No DOI below is guessed; where a DOI is not known with
confidence it is omitted and the URL stands alone.

## Primary (spec §14)

### Hylo / mutable value semantics

- **[hylo-mvs]** *Implementation Strategies for Mutable Value Semantics.*
  Dimitri Racordon, Denys Shabalin, Daniel Zheng, Dave Abrahams, Brent A. Yorgey.
  The Journal of Object Technology, vol. 21, no. 2, 2022 (verify: journal volume and issue);
  preprint arXiv:2106.12678, 2021.
  https://arxiv.org/abs/2106.12678 (PDF as cited by the evaluation: https://arxiv.org/pdf/2106.12678).
  Note: `ARCHITECTURE_EVALUATION.md` §11 lists the authors as "Racordon, Shirako, Abrahams,
  et al."; the author list above is the one on the arXiv record (verify).

- **[hylo-tour]** *The Hylo Programming Language — Language Tour.*
  The Hylo project (formerly Val). Online documentation, current edition (undated).
  https://docs.hylo-lang.org/language-tour/functions-and-methods ;
  https://docs.hylo-lang.org/language-tour/bindings .
  Covers the `let` / `inout` / `sink` / `set` parameter conventions, projections, and
  exclusive access without lifetime annotations.

### Second-class values

- **[osvald-2016]** *Gentrification Gone Too Far? Affordable 2nd-Class Values for Fun and (Co-)Effect.*
  Leo Osvald, Grégory Essertel, Xilun Wu, Lilliam I. González-Alayón, Tiark Rompf.
  Proceedings of OOPSLA 2016 (ACM SIGPLAN International Conference on Object-Oriented
  Programming, Systems, Languages, and Applications), Amsterdam, 2016.
  https://www.cs.purdue.edu/homes/rompf/papers/osvald-oopsla16.pdf .
  Second-class values with stack-bounded lifetimes; Coq soundness proof.

- **[osvald-2017]** *Rust-Like Borrowing with 2nd-Class Values (Short Paper).*
  Leo Osvald, Tiark Rompf.
  Proceedings of the 8th ACM SIGPLAN International Symposium on Scala (SCALA 2017),
  Vancouver, 2017.
  https://www.cs.purdue.edu/homes/rompf/papers/osvald-scala17.pdf (verify).

### Swift nonescapable types and lifetime dependence

- **[swift-se-0446]** *SE-0446: Nonescapable Types.*
  Swift Evolution proposal; authors Andrew Trick and Tim Kientzle (verify: author list).
  Swift Evolution, accepted with modifications, 2024.
  Review thread: https://forums.swift.org/t/se-0446-nonescapable-types/74666 ;
  acceptance note: https://forums.swift.org/t/accepted-with-modifications-se-0446-nonescapable-types/75504 ;
  proposal text: https://github.com/swiftlang/swift-evolution/blob/main/proposals/0446-non-escapable.md (verify).

- **[swift-lifetime-pitch]** *Compile-time Lifetime Dependency Annotations* (pitch 3) and
  *Experimental Support for Lifetime Dependencies in Swift 6.2 and Beyond.*
  Swift Evolution pitches on the Swift Forums; authors Andrew Trick, Meghana Gupta and
  Tim Kientzle (verify: author list), 2024–2025.
  https://forums.swift.org/t/pitch-3-compile-time-lifetime-dependency-annotations/84968 ;
  https://forums.swift.org/t/experimental-support-for-lifetime-dependencies-in-swift-6-2-and-beyond/78638 .
  Evidence that nonescapable values needed a downward-only dependency first, then more.

### Mojo origins

- **[mojo-origins]** *Lifetimes, Origins, and References* (Mojo Manual, "Value ownership" chapter).
  Modular Inc. Online documentation, current edition.
  https://docs.modular.com/mojo/manual/values/lifetimes .

- **[mojo-24-6]** *Hands-on with Mojo 24.6* (rename of lifetimes to origins).
  Modular Inc. Blog post, 2024.
  https://modular.com/blog/hands-on-with-mojo-24-6 .

- **[mojo-1.0]** *Mojo 1.0 Release Notes* (interior origins for container element references).
  Modular Inc. Release notes, version 1.0.0.
  https://mojolang.org/releases/v1.0.0/ (as cited by the evaluation; verify that this is the
  canonical host for the Mojo changelog).

### Rust (expressiveness baseline)

- **[rust-reference]** *The Rust Reference.*
  The Rust Project Developers. Online, tracks the current stable Rust release.
  https://doc.rust-lang.org/reference/ .
  Ownership, borrowing, lifetime elision, `Send`/`Sync`, `unsafe`.

- **[rustonomicon]** *The Rustonomicon: The Dark Arts of Unsafe Rust.*
  The Rust Project Developers. Online, tracks the current stable Rust release.
  https://doc.rust-lang.org/nomicon/ .
  Drop flags and drop check, `MaybeUninit`, unwinding and exception safety, `Send`/`Sync`
  obligations.

## Secondary (spec §14)

### Zig

- **[zig-ref]** *Zig Language Reference.*
  Zig Software Foundation. Version 0.14.1 (verify: pin to the version the benchmark
  toolchain lock uses).
  https://ziglang.org/documentation/0.14.1/ (verify) ; current: https://ziglang.org/documentation/master/ .
  Explicit allocators, build modes and safety checks, freestanding targets, no closures.

### Austral

- **[austral-spec]** *The Austral Language Specification.*
  Fernando Borretti. Online specification, current edition.
  https://austral-lang.org/spec/spec.html .
  Linear types, `borrow` blocks with named regions, typeclasses, capability-based security.

### Hare

- **[hare-spec]** *Hare Language Specification.*
  Drew DeVault and the Hare contributors. Online specification, current edition.
  https://harelang.org/specification/ (verify).
  Minimal systems language: no closures, no generics, explicit error unions, freestanding use.

### Vale

- **[vale-genrefs]** *Generational References* and *Vale's Memory Safety*.
  Evan Ovadia. Vale language documentation and design articles, 2021–2023 (verify: dates).
  https://verdagon.dev/blog/generational-references (verify) ;
  https://vale.dev/memory-safe (verify).
  Per-object generation words checked on every dereference; adopted in kern only as the
  `Pool` handle design (spec §5.6).

### Cyclone

- **[cyclone-2002]** *Cyclone: A Safe Dialect of C.*
  Trevor Jim, Greg Morrisett, Dan Grossman, Michael Hicks, James Cheney, Yanling Wang.
  Proceedings of the USENIX Annual Technical Conference, 2002.
  https://www.usenix.org/legacy/event/usenix02/jim.html (verify) ;
  PDF: https://www.cs.umd.edu/~mwh/papers/cyclone-cuj.pdf (verify; the C/C++ Users Journal
  version of the same material).

- **[cyclone-regions-2002]** *Region-Based Memory Management in Cyclone.*
  Dan Grossman, Greg Morrisett, Trevor Jim, Michael Hicks, Yanling Wang, James Cheney.
  Proceedings of PLDI 2002 (ACM SIGPLAN Conference on Programming Language Design and
  Implementation), Berlin, 2002.
  https://www.cs.umd.edu/~mwh/papers/cyclone-regions.pdf (verify).
  Region type parameters on functions and structs; the annotation burden ADR-002 rejects.

### Tofte–Talpin region inference

- **[tofte-talpin-1997]** *Region-Based Memory Management.*
  Mads Tofte, Jean-Pierre Talpin.
  Information and Computation, vol. 132, no. 2, pp. 109–176, 1997.
  https://doi.org/10.1006/inco.1996.2613 (verify).
  Region inference without annotations; extents are computed, not written, and the known
  leak pathology is the predictability objection in ADR-002.

### Pony

- **[pony-caps-2015]** *Deny Capabilities for Safe, Fast Actors.*
  Sylvan Clebsch, Sophia Drossopoulou, Sebastian Blessing, Andy McNeil.
  Proceedings of the 5th International Workshop on Programming based on Actors, Agents, and
  Decentralized Control (AGERE! 2015), co-located with SPLASH 2015, Pittsburgh, 2015.
  https://www.ponylang.io/media/papers/fast-cheap-with-proof.pdf (verify).
  Reference capabilities (`iso`, `trn`, `ref`, `val`, `box`, `tag`); `iso` and `val` inform
  kern's `Send` and `Share` markers (ADR-007).

### Ada / SPARK

- **[ada-rm]** *Ada Reference Manual, ISO/IEC 8652:2023 (Ada 2022).*
  ISO/IEC JTC 1/SC 22/WG 9; edited by the Ada Rapporteur Group (AdaCore / Ada-Europe
  editions).
  http://www.ada-auth.org/standards/22rm/html/RM-TTL.html (verify).
  Contracts (`Pre`, `Post`, `Type_Invariant`), run-time checks and `pragma Suppress`,
  runtime profiles.

- **[spark-rm]** *SPARK 2014 Reference Manual.*
  AdaCore and Altran/Capgemini Engineering. Online, tracks the current SPARK release.
  https://docs.adacore.com/spark2014-docs/html/lrm/ (verify).
  The formally verifiable Ada subset; proof of absence of run-time errors.

### C++20 memory model

- **[iso-cpp20]** *ISO/IEC 14882:2020 — Programming languages — C++.*
  ISO/IEC JTC 1/SC 22/WG 21. International Standard, 2020.
  https://www.iso.org/standard/79358.html (verify).
  Clause 6.9.2 [intro.multithread] and clause 33 [atomics]: the happens-before relation,
  release sequences, and the `memory_order` values that spec §5.5 adopts by reference.
  (The final working draft N4861 is the freely available text closest to the published
  standard; verify before citing it in place of the standard.)
