---
title: TypeScript
description: TypeScript target setup, generated output, native library boundaries, and implementation details.
---

## Prerequisites

Node.js 22.6 or later to execute generated `.ts` files through native type stripping.

## Compile and run

From a prepared source module containing a `main` package:

```sh
goalchemy compile -target typescript -out out/typescript .
cd out/typescript
node --enable-source-maps main.ts
```

Or use `goalchemy run -target typescript .` to compile and execute in a temporary directory. Add `-gate cooperative` for source tasks or suspending operations.

## Generated output

The executable target emits ES2022 TypeScript in native ESM modules: `main.ts`, `pkg_*.ts`, leaf `shared.ts`, and selected `rt/` files. Cooperative output adds portable `program.ts` and `host.ts`; `main.ts` remains the Node executable wrapper.

Every output includes its own `README.md`, selected runtime files, source mappings, and `goalchemy.manifest.json`. See the [full output reference](/guides/usage/#output) for the inventory and entry behavior.

## Native libraries

Library output provides portable JavaScript and declarations for Node and browsers. The WebCrypto/fetch library graph is tested in Node and an actual browser. Browser consumers use the library API or portable host entry rather than the Node executable wrapper. Read the [TypeScript library boundary](/targets/details/typescript-library-boundary/).

## Runtime and diagnostics

Byte slices and arrays use native `Uint8Array` storage while retaining source nil, length, capacity, and aliasing behavior. Per-file `.ts.map` source maps support stack traces. The portable `runHost(host)` entry takes an explicit output/failure adapter.

## Implementation details

[Library boundary](/targets/details/typescript-library-boundary/) · [Host operations](/targets/details/typescript-host-operations/) · [Byte storage](/targets/details/typescript-byte-storage/)
