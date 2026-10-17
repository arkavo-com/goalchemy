---
title: Contributing
description: Work on source semantics, native targets, runtime contracts, and the documentation site.
sidebar:
  order: 2
---

Start from the [repository](https://github.com/eugenioenko/goalchemy) and identify whether a change affects source semantics, shared source packages, native capabilities, or target emission. That determines which contracts and tests must move together.

## Compiler development

Use the repository-selected Go toolchain and install target prerequisites before running cross-language tests. The [CI guide](/internals/ci/) describes pinned dependencies and individual suites.

```sh
go build ./...
go test -short -timeout 5m ./...
go run ./cmd/goalchemy spec generate -check
```

Short mode is a development check, with Go and TypeScript coverage for several matrices. Full validation covers every target and needs the native tools documented in CI. Do not interpret a short-mode run as complete target verification.

For a semantic change, update the language specification, canonical contracts, generated specs, and conformance cases together. Run `make spec-generate` when contract or API documentation sources change, and review the resulting diff. The runtime reference on this site is published from that generated repository documentation.

## New target languages

Follow [adding a target](/internals/adding-a-target/). A working emitter is only one part: runtime operations, native dependencies, public library APIs, host lifecycles, diagnostic maps, and complete CI coverage all need verification.

## Documentation development

The site lives in `docs-web/` and uses Astro with Starlight. From the repository root:

```sh
pnpm install --frozen-lockfile
pnpm docs:dev
pnpm docs:check
pnpm docs:build
```

Use Node 24 and the pnpm version pinned in `package.json`. The build publishes canonical repository guides, splits the runtime reference by package, and derives the feature matrix from `specs/features.yaml`. Edit those source files rather than generated content. Introductory guides and target landing pages are authored under `docs-web/src/content/docs/`.

The production build checks internal links, fragment IDs, and local assets. See [the site maintenance guide](https://github.com/eugenioenko/goalchemy/blob/main/docs-web/README.md) for publication, deployment, and source mappings.
