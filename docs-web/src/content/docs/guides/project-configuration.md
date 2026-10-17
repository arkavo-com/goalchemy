---
title: Project configuration
description: Configure package patterns, feature gates, build tags, generated names, and multi-target builds.
sidebar:
  order: 1
---

`goalchemy build` reads `goalchemy.yaml` from the current directory. Package patterns and output paths resolve relative to that configuration file.

```yaml
schema_version: 1
packages: [./cmd/app]
tags: [prod]
gate: sequential
compact_names: false
targets:
  go: {out: out/go}
  typescript: {out: out/typescript}
  python: {out: out/python}
  java: {out: out/java}
  csharp: {out: out/csharp}
  rust: {out: out/rust}
  c: {out: out/c}
  swift: {out: out/swift}
```

Only list the targets you intend to generate. Swift is experimental. Choose a non-`main` root package to emit a library, or a `main` package for an executable.

| Field | Meaning |
| --- | --- |
| `schema_version` | Configuration schema version; currently `1`. |
| `packages` | Go package patterns to load. |
| `tags` | Optional source build tags. |
| `gate` | `sequential` by default, or `cooperative` for tasks and suspending operations. |
| `compact_names` | Use short private generated identifiers; defaults to `false`. |
| `targets` | Target IDs mapped to output directories. |

The configuration uses restricted YAML: anchors, aliases, floating-point values, and unknown keys are rejected.

## Select another configuration

```sh
goalchemy build -config config/release.yaml
```

## Work on one target

`check`, `compile`, and `run` accept package patterns directly. They do not read `goalchemy.yaml`; pass their flags explicitly:

```sh
goalchemy check -gate cooperative -tags prod ./cmd/app
goalchemy compile -gate cooperative -target rust -out out/rust ./cmd/app
goalchemy run -gate cooperative -target python ./cmd/app
```

Flags precede package arguments. Use `-C` to select the directory from which packages are loaded.

## Control generated names

```sh
goalchemy compile -target rust -out out/rust --compact-names .
goalchemy build --compact-names=false
```

An explicit `build` flag overrides `compact_names` in the configuration. Public API names retain their contracts in either mode. See [generated names](/guides/usage/#generated-names) for collision handling and target details.

## Keep generated output reproducible

Pin the compiler, source module dependencies, and target dependencies in your own delivery pipeline. Generated manifests identify linked contracts and emitted files. A successful emission into a reused directory removes obsolete files from its prior inventory and preserves unlisted caller files; use dedicated output directories for generated code.
