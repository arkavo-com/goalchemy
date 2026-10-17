---
title: Rust
description: Rust target setup, generated output, native library boundaries, and implementation details.
---

## Prerequisites

A stable Rust toolchain supporting edition 2021. Native capabilities build through Cargo and use the maintained dependencies required by the output.

## Compile and run

From a prepared source module containing a `main` package:

```sh
goalchemy compile -target rust -out out/rust .
cd out/rust
sh run.sh
```

Or use `goalchemy run -target rust .` to compile and execute in a temporary directory. Add `-gate cooperative` for source tasks or suspending operations.

## Generated output

The Rust target emits `src/main.rs` for executables, package-owned `src/pkg_*.rs` modules, `src/shared.rs`, selected `src/rt/` files, `Cargo.toml`, and `run.sh`. Standard-only output can run through plain `rustc`; capability output uses Cargo.

Every output includes its own `README.md`, selected runtime files, source mappings, and `goalchemy.manifest.json`. See the [full output reference](/guides/usage/#output) for the inventory and entry behavior.

## Native libraries

Non-`main` packages emit `src/lib.rs` and an owned Cargo library boundary with `Result`/`Future` APIs and cancellation. Your SDK delivery pipeline supplies its dependency lock. Read the [Rust library boundary](/targets/details/rust-library-boundary/) for values, host entry, and lifecycle.

## Runtime and diagnostics

Values live in a traced heap collected at safepoints; byte leaves use native `Vec<u8>`. Set `GOALCHEMY_HEAP_STATS=1` to print heap statistics and `GOALCHEMY_GC_THRESHOLD` to control collection frequency. `.rs.lines` sidecars retain source positions.

## Implementation details

[Library boundary](/targets/details/rust-library-boundary/) · [Host operations](/targets/details/rust-host-operations/) · [Byte storage](/targets/details/rust-byte-storage/)
