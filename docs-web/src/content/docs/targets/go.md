---
title: Go
description: Go target setup, generated output, native library boundaries, and implementation details.
---

## Prerequisites

Go 1.25 or later. The compiler itself uses the Go 1.27.1 reference toolchain.

## Compile and run

From a prepared source module containing a `main` package:

```sh
goalchemy compile -target go -out out/go .
cd out/go
go run .
```

Or use `goalchemy run -target go .` to compile and execute in a temporary directory. Add `-gate cooperative` for source tasks or suspending operations.

## Generated output

Generated Go preserves the lowered semantics and provides a useful comparison with the original source. The output is a native Go package with `main.go`, `shared.go`, package-owned `pkg_*.go` files, `go.mod`, and selected `rt/` files.

Every output includes its own `README.md`, selected runtime files, source mappings, and `goalchemy.manifest.json`. See the [full output reference](/guides/usage/#output) for the inventory and entry behavior.

## Native libraries

Non-`main` packages expose copied values and cancellable, serialized calls. Library state has explicit ownership; ordinary value calls start from fresh source initialization. Read the [Go library boundary](/targets/details/go-library-boundary/) and [stateful handles](/guides/library-handles/).

## Runtime and diagnostics

Go source line directives connect diagnostics with original source positions. Host I/O runs through a mailbox and scheduler owner; native worker completion never executes source code directly.

## Implementation details

[Library boundary](/targets/details/go-library-boundary/) · [Host-operation lifecycle](/guides/host-operations/)
