---
title: Target languages
description: Compare the eight native targets, required tools, generated executables, and library support.
---

Goalchemy emits source for eight target languages. Each target preserves the accepted source semantics through its runtime. Install the target tools to build and run generated output.

| Target | Tools to run generated programs | Entry or build command |
| --- | --- | --- |
| [Go](/targets/go/) | Go 1.25+ | `go run .` |
| [TypeScript](/targets/typescript/) | Node.js 22.6+ | `node main.ts` |
| [Python](/targets/python/) | Python 3.10+ | `python3 main.py` |
| [Java](/targets/java/) | JDK 21+ | `sh run.sh` |
| [C#](/targets/csharp/) | .NET SDK 8 | `sh run.sh` or `dotnet run` |
| [Rust](/targets/rust/) | Stable Rust, edition 2021 | `sh run.sh` or `cargo run --release` |
| [C](/targets/c/) | C17 compiler, threaded bdwgc 8.x | `sh run.sh` |
| [Swift (experimental)](/targets/swift/) | Swift 6.4, C compiler, pkg-config, OpenSSL 3, zlib, libcurl | `sh run.sh` |

Commands in the last column run from the generated output directory. Native capabilities can add dependencies beyond the base tools. Swift includes its native prerequisites even for pure-logic programs; Linux x86_64 is its verified baseline.

## Executables and libraries

A `main` root package emits an executable. A non-`main` root emits a library with a target-specific public value boundary. Generated output also includes a `README.md`, diagnostic mappings, and a manifest of its source packages, contracts, and file inventory.

The compiler's output is a foundation for SDK packaging. Your delivery pipeline owns ecosystem-specific release metadata, dependency installation, and native consumer tests. Start with [building an SDK](/guides/building-an-sdk/).

## Feature and environment support

The [feature matrix](/language/features/) records source feature coverage. It does not imply every host platform, dependency combination, or public API shape has been validated. Consult the individual target guide and [limitations](/language/limitations/).

For exact file inventories and entry behavior, read [commands and output](/guides/usage/#output). Deeper design and verification notes live under each target's implementation details.
