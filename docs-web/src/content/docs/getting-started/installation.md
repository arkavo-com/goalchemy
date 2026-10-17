---
title: Installation
description: Install Goalchemy, understand the source toolchain, and prepare target runtimes.
sidebar:
  order: 2
---

## Install the compiler

The repository selects Go 1.27.1 as its reference toolchain. Use that toolchain, or a Go installation permitted to download the toolchain selected by the module.

```sh
go install github.com/eugenioenko/goalchemy/cmd/goalchemy@latest
goalchemy version
```

The executable is installed in `GOBIN`, or `$(go env GOPATH)/bin` when `GOBIN` is unset. Put that directory on your `PATH` if your shell cannot find `goalchemy`.

For a repeatable installation, replace `@latest` with a release tag and pin the Goalchemy module dependency in your source project to the same release.

## Build a checkout

```sh
git clone https://github.com/eugenioenko/goalchemy.git
cd goalchemy
go build -o bin/goalchemy ./cmd/goalchemy
./bin/goalchemy version
```

Commands in these docs use `goalchemy` on `PATH`. From a checkout, `go run ./cmd/goalchemy` also runs the local compiler.

## Prepare a source module

The compiler's toolchain and the source language version serve different purposes. Goalchemy source modules must declare Go 1.25 or earlier, even when you use the newer reference compiler.

```sh
mkdir hello && cd hello
go mod init example.com/hello
go mod edit -go=1.25
go get github.com/eugenioenko/goalchemy@latest
```

Add the module dependency even for a program using only builtins, so the compiler can resolve Goalchemy's packages when loading source. Source is checked for Linux amd64 with cgo disabled. The [language specification](/language/specification/) defines the complete source profile.

## Install the tools for your target

Generating source does not install the target runtime or its native dependencies. To execute the result, install the tools listed in the [target overview](/targets/). A Python or TypeScript installation is enough to follow the quick start for its corresponding target.

Compiler contributors can use the repository's pinned Linux x86_64 toolchain and CI bootstrap scripts. See [compiler CI](/internals/ci/) for the full prerequisites; these are broader than the tools needed for one generated application.
