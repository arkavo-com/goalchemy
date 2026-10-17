---
title: Swift
description: Swift target setup, generated output, native library boundaries, and implementation details.
---

## Prerequisites

Swift 6.4, a C compiler, pkg-config, OpenSSL 3, zlib, and libcurl. The verified baseline is Linux x86_64 in Swift 5 language mode.

## Compile and run

From a prepared source module containing a `main` package:

```sh
goalchemy compile -target swift -out out/swift .
cd out/swift
sh run.sh
```

Or use `goalchemy run -target swift .` to compile and execute in a temporary directory. Add `-gate cooperative` for source tasks or suspending operations.

## Generated output

Swift is experimental. It emits native Swift plus a small C module: `main.swift`, package-owned `pkg_*.swift` files, `shared.swift`, selected `rt/` files, and `run.sh`. All outputs currently include native prerequisites, even for pure logic.

Every output includes its own `README.md`, selected runtime files, source mappings, and `goalchemy.manifest.json`. See the [full output reference](/guides/usage/#output) for the inventory and entry behavior.

## Native libraries

Non-`main` output emits a SwiftPM product named `GoalchemyGenerated`. Public values are typed and calls expose synchronous or asynchronous operations. Source errors become `GoalchemyFailure`. Read [SwiftPM library API](/targets/details/swift-target/#swiftpm-library-api) for ownership, callbacks, cancellation, and stream examples.

## Runtime and diagnostics

macOS, iOS, and Apple SDK integration have not been verified. `SWIFTC` and `CC` select compiler binaries. `.swift.lines` sidecars retain source positions. ARC handles ordinary acyclic values and tracing breaks unreachable source cycles at scheduler safepoints.

## Implementation details

[Swift setup, semantics, and library API](/targets/details/swift-target/)
