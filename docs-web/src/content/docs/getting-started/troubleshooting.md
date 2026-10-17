---
title: Troubleshooting
description: Resolve source-version errors, missing module dependencies, unsupported features, and target runner problems.
sidebar:
  order: 5
---

## The source language version is too new

The reference compiler uses Go 1.27.1, while source being transpiled must declare Go 1.25 or earlier. A newly created module can inherit the installed toolchain's newer language version. Set the source baseline explicitly:

```sh
go mod edit -go=1.25
goalchemy check .
```

Changing the directive does not make newer language features portable. Check the [language specification](/language/specification/) and remove unsupported constructs.

## Goalchemy packages cannot be resolved

The source module needs a Goalchemy dependency even when your first program uses only builtins:

```sh
go get github.com/eugenioenko/goalchemy@latest
```

Use your pinned release in place of `@latest` for an existing project. `go mod tidy` can remove the dependency when no source file imports it; restore it before running the compiler. A source package importing `std/` or `lib/` retains the dependency normally.

## Valid Go is rejected

Go type checking establishes only that the program is Go. Goalchemy also checks a semantic subset, its imported source, and the selected feature gate. Run `goalchemy check -json .` and use the diagnostic's location and remedy to find the rejected construct.

Tasks, channels, `select`, and suspending operations need `-gate cooperative`. Generics, source reflection, `unsafe`, and arbitrary standard-library imports are outside the accepted profile. See [limitations](/language/limitations/) and the [feature matrix](/language/features/).

## Emission succeeds but execution fails

Install the selected [target's tools and native prerequisites](/targets/). Inspect the generated `README.md` and run its native build command directly to see target diagnostics. Use `goalchemy run -keep -target python .`, substituting your target, to retain temporary output.

The public library boundary can reject exported types that are valid inside source code. Consult the target's library documentation and test a minimal native consumer before expanding the API.

## A program prints nothing to standard output

Go's `print` and `println` builtins write to standard error. Check both streams when running examples or collecting results. An unrecovered source panic also writes a panic report to standard error.

## Report a reproducible problem

Open an [issue](https://github.com/eugenioenko/goalchemy/issues) with the compiler version, source module language version, target tools, gate, complete command, diagnostic, and smallest source reproducer. Include the generated manifest when a failure depends on target output. Keep credentials and private data out of the reproducer.
