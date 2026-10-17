---
title: C
description: C target setup, generated output, native library boundaries, and implementation details.
---

## Prerequisites

A C17 compiler and Boehm-Demers-Weiser GC 8.x built with threads. Crypto and HTTP add OpenSSL 3 and, for HTTP, libcurl discovered through pkg-config.

## Compile and run

From a prepared source module containing a `main` package:

```sh
goalchemy compile -target c -out out/c .
cd out/c
sh run.sh
```

Or use `goalchemy run -target c .` to compile and execute in a temporary directory. Add `-gate cooperative` for source tasks or suspending operations.

## Generated output

The C target emits `main.c`, package-owned `pkg_*.c` translation units, `shared.c`, declaration-only `goalchemy_internal.h`, selected `rt/` files, and `run.sh`. The build compiles explicit native source units.

Every output includes its own `README.md`, selected runtime files, source mappings, and `goalchemy.manifest.json`. See the [full output reference](/guides/usage/#output) for the inventory and entry behavior.

## Native libraries

Non-`main` packages produce `goalchemy.h`, `build.sh`, and `libgoalchemy.a`. The owned `gxc_value` API supports value trees, cooperative calls, and error mapping. Older scalar entry points have narrower types and do not support suspending exports. Read the [library output reference](/guides/usage/#libraries) before choosing an API.

## Runtime and diagnostics

`GOALCHEMY_BDWGC` can identify a collector install prefix; otherwise the runner uses pkg-config or `-lgc`. `CC`, `CFLAGS`, and `LDLIBS` are honored. `.c.lines` sidecars map generated lines. Native byte storage remains collector-owned and preserves source aliasing.

## Implementation details

[Host operations](/targets/details/c-host-operations/) · [Byte storage](/targets/details/c-byte-storage/) · [CRC32 mappings](/reference/checksums/)
