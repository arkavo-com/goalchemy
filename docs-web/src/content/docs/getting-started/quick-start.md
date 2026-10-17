---
title: Quick start
description: Check a Go-subset program, execute it in multiple languages, and build target output directories.
sidebar:
  order: 3
---

[Install Goalchemy](/getting-started/installation/) first. This walkthrough uses Python 3.10+ and Node.js 22.6+ for the corresponding run commands.

## Create a Go program

```sh
mkdir hello && cd hello
go mod init example.com/hello
go mod edit -go=1.25
go get github.com/eugenioenko/goalchemy@latest
```

Create `main.go`:

```go
package main

func main() {
    println("Hello from Goalchemy")
}
```

The source runs with ordinary Go:

```sh
go run .
```

## Check and run across languages

```sh
goalchemy check .
goalchemy run -target python .
goalchemy run -target typescript .
```

Each run prints `Hello from Goalchemy`. Go's `print` and `println` builtins write to standard error. `run` compiles into a temporary directory, invokes the target runner, and removes the directory afterward. Add `-keep` to retain it for inspection.

## Generate source you can inspect

```sh
goalchemy compile -target python -out out/python .
python3 out/python/main.py
```

The output includes generated source, runtime files, a `README.md`, line mappings, and `goalchemy.manifest.json`. Private generated names are readable by default. The [commands and output guide](/guides/usage/#output) explains each target's layout.

## Build several targets together

Create `goalchemy.yaml` beside `main.go`:

```yaml
schema_version: 1
packages: [.]
gate: sequential
targets:
  go: {out: out/go}
  typescript: {out: out/typescript}
  python: {out: out/python}
```

```sh
goalchemy build
```

`build` emits all configured targets; it does not execute them. Run TypeScript with `node out/typescript/main.ts`, Python with `python3 out/python/main.py`, or Go from its output directory with `go run .`.

Continue with [project configuration](/guides/project-configuration/), [portable source](/guides/portable-source/), or [building an SDK](/guides/building-an-sdk/).
