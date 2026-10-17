---
title: Limitations and support status
description: Source restrictions, target boundary limits, experimental Swift, and the scope of compatibility claims.
sidebar:
  order: 5
---

Goalchemy is designed for portable SDK logic in an explicit Go subset. Plan around the checked feature manifest and target boundary you will use.

## Source restrictions

The initial accepted profile excludes:

- Generic declarations and instantiations, generic aliases, and range-over-iterator functions.
- Complex numbers, `unsafe`, source reflection, cgo, and `go:linkname`.
- Arbitrary `goto`, pointers to slice/array elements, pointer arithmetic, and pointer/integer conversions.
- Source-visible finalizers, destructors, and parallel execution.
- Concurrency constructs under the sequential gate.

Some library operations support only specified operand types. A familiar package name does not imply the entire Go standard library is implemented. Check [runtime APIs](/runtime/overview/) and the [rejection details](/guides/usage/#what-is-rejected).

## Target and public API limits

Source feature coverage does not mean every type can cross a native public boundary. Value libraries, stateful handles, callbacks, and streams have their own shape and lifetime rules. Consult the relevant [target guide](/targets/) and test your native consumer.

The C target has both an owned value API and older scalar entry points. The scalar API has narrower types, thread rules, and no suspending exports; use the [library output reference](/guides/usage/#libraries) for the distinction.

## Swift is experimental

The verified baseline is Swift 6.4 on Linux x86_64, using Swift 5 language mode. Native prerequisites are required even for pure-logic Swift outputs. macOS, iOS, and Apple SDK integration have not been verified. See [Swift](/targets/swift/).

## Compatibility has a defined scope

Differential tests compare native Go with accepted source and target output. They account for choices Go intentionally leaves unspecified, including map iteration and incidental capacity growth. They do not establish compatibility for rejected source, arbitrary external packages, or untested host environments.

Compiler hardening results are bounded evidence with recorded toolchains, seeds, and cases. See [testing and hardening](/internals/testing/) for how to reproduce and interpret them. Current engineering follow-ups are tracked in [the repository](https://github.com/eugenioenko/goalchemy/blob/main/docs/followups.md).
