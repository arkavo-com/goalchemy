---
title: Introduction
description: What Goalchemy compiles, when to use it, and how one source program becomes native packages.
sidebar:
  order: 1
---

Goalchemy is a transpiler for a restricted, Go-compatible source language. Write ordinary `.go` files, check them with the Go toolchain, and generate Go, TypeScript, Python, Java, C#, Rust, C, and experimental Swift.

Its focus is SDKs that must ship the same logic to several language ecosystems. A shared implementation keeps validation, formats, and protocol decisions together; native capability adapters handle the host.

## What you write

Source programs use Go syntax and a module declaring Go 1.25 or earlier. The compiler loads and type-checks packages, then checks them against an explicit language gate. Successful Go compilation is only the first check: Goalchemy rejects source outside its accepted subset.

The [language overview](/language/overview/) introduces the portable semantics. The [feature matrix](/language/features/) records tested support.

## What you ship

An executable package emits a runnable target directory. A non-`main` root package emits an importable library with a target-specific public boundary. Outputs include generated source, selected runtime files, diagnostic maps, and a manifest recording compiler and contract information.

Generated programs run with their target's tools. They do not invoke a Go interpreter. Native dependencies depend on the target and linked capabilities; consult the [target guides](/targets/).

## How compilation works

1. **Load and validate.** Go packages are parsed, type-checked, and checked against the subset.
2. **Lower.** A typed intermediate representation makes sequencing, copying, aliasing, and runtime operations explicit.
3. **Emit and link.** A target emitter writes native source and links the runtime operations it needs.
4. **Verify behavior.** Contract and differential tests compare results across targets and native Go.

See [compiler architecture](/internals/architecture/) to work on the compiler itself.

## Choose a starting point

- [Install Goalchemy](/getting-started/installation/) and [run your first program](/getting-started/quick-start/).
- [Build an SDK](/guides/building-an-sdk/) to generate reusable libraries.
- [Explore examples](/getting-started/examples/) for errors, concurrency, HTTP, and crypto.

For AI assistants and coding agents, [llms.txt](/llms.txt) provides a concise reading guide with links to canonical Markdown references.
