# ADR 0001: Go for the native component manager

* Status: accepted
* Context: issue #12 (native port plan) and the #21 handover
* Decision: implement `4d-components` in **Go**, standard library only.

## Context

The dependency manager is network-, filesystem- and orchestration-bound. The reference is a 1,400-line Python
script whose behavior is defined by observed 4D semantics, not by an algorithm to optimise. The port must
reproduce it exactly, ship as one binary per platform, and be auditable.

## Candidates scored (1 = poor, 5 = excellent)

| Criterion | Go | Rust |
| --- | --- | --- |
| Implementation complexity | 5 | 3 |
| Cross-platform single binary | 5 | 4 |
| HTTP and ZIP reliability (stdlib) | 5 | 3 (crates needed) |
| Resolver complexity | 4 | 4 |
| Security and error handling | 4 | 5 |
| Testability (mock HTTP server in the stdlib) | 5 | 4 |
| Long-term maintenance for this repository | 5 | 3 |
| Reproducing Python behavior precisely | 4 | 4 |

The default preference in #12 is Go; nothing measured here argues against it.

## Decision and consequences

* **Zero third-party modules.** `net/http`, `archive/zip`, `crypto/sha256`, `encoding/json` and `flag` suffice.
  The supply-chain surface is the Go toolchain and nothing else; `go build` needs no network, so the GitHub
  Action builds the executable from the reviewed revision of its own source.
* **No SemVer library.** The reference's range grammar and the observed 4D tag/anchor rules are ported directly,
  because a generic SemVer solver would differ from real 4D (R tags are not SemVer; combined-declaration
  semantics are specific). The differential tests are the proof.
* **Ordered JSON.** Declaration order is part of the behavior (breadth-first resolution, combined-declaration
  order), so `internal/ojson` keeps object key order and number text, and writes JSON the way Python does so
  that locks and cache metadata are byte-comparable.
* **Rust remains a valid alternative** if a later requirement (for example memory-safety guarantees for a
  much larger archive parser) outweighs the extra build and dependency cost. Reversing this ADR requires
  re-running the differential suite, which is language-independent.
* The command-line option `--github-token` proposed in #12 is **not** provided: a process argument is visible to every user of the machine. Credentials come from the environment or a token file.
