---
title: CLI reference
description: Goalchemy commands, source flags, emission options, configuration selection, exit statuses, and contract tools.
sidebar:
  order: 1
---

Invoke `goalchemy <command> [flags] [packages]`. Flags precede package arguments. Package patterns default to `.` when omitted by source-loading commands.

## Commands

| Command | Purpose |
| --- | --- |
| `check` | Load, type-check, and validate source against a language gate. |
| `compile` | Emit one target to `-out`, or print IR for `-target ir`. |
| `run` | Emit to a temporary directory and invoke the target runner. |
| `build` | Emit every target configured in `goalchemy.yaml`. |
| `features` | Print the installed compiler's feature manifest. |
| `version` | Print the compiler version. |
| `spec validate` | Validate canonical contracts and target mappings. |
| `spec generate` | Regenerate target specifications and API documentation. |
| `test` | Execute runtime contract cases with target harnesses. |

## Source-loading flags

`check`, `compile`, and `run` share these options:

| Flag | Default | Meaning |
| --- | --- | --- |
| `-gate` | `sequential` | `sequential` or `cooperative`. |
| `-tags` | empty | Comma-separated source build tags. |
| `-C` | current directory | Directory from which to load source packages. |
| `-json` | `false` | Write structured diagnostics. |

```sh
goalchemy check -json -gate cooperative ./sdk
goalchemy compile -target rust -out out/rust ./sdk
```

## Emission and execution

`compile` accepts `-target` (default `go`), required `-out`, and `--compact-names`. The `ir` target writes to standard output and does not need `-out`.

`run` accepts `-target` (default `typescript`), `-keep`, and `--compact-names`. The selected target's tools must be installed. `-keep` retains the temporary output and prints its path.

```sh
goalchemy compile -target ir .
goalchemy run -target python -keep .
```

Target IDs are `go`, `typescript`, `python`, `java`, `csharp`, `rust`, `c`, and `swift`. Swift is experimental.

## Multi-target builds

`build` accepts `-config` (default `goalchemy.yaml`), `-json`, and `--compact-names`. It loads package patterns, tags, the gate, and output directories from [project configuration](/guides/project-configuration/). An explicit naming flag overrides the configuration value.

`compile` and `run` do not read that configuration.

## Contract tools

Run maintenance commands from a repository checkout with target prerequisites available:

```sh
goalchemy spec validate -root .
goalchemy spec generate -check
goalchemy test -target go -target typescript -v
```

`spec validate` accepts `-json` and optional `-root`; its default is the embedded catalog. `spec generate` accepts `-root` (default `.`) and `-check` to detect stale output without writing it. `test` accepts `-root` (default `.`), repeatable `-target`, and `-v`; without a target it uses every target with a harness.

## Diagnostics and status

Source-checking and emission failures return nonzero. CLI flag or configuration errors generally return status `2`; compilation or conformance errors return `1`. The `help` command prints usage and returns `2`. Target execution can expose additional runtime failure behavior; consult [commands and output](/guides/usage/).

Human-readable and JSON diagnostics carry the same semantic information. See [diagnostic categories](/reference/diagnostics/). `GCW` warnings permit compilation to continue.
