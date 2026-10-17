---
title: Examples
description: Runnable examples covering parsing, errors, HTTP, cryptography, collections, and cooperative tasks.
sidebar:
  order: 4
---

The repository's [examples directory](https://github.com/eugenioenko/goalchemy/tree/main/examples) contains complete source programs with multi-target configurations. Run these commands from a checkout:

```sh
go run ./cmd/goalchemy run -target python ./examples/calc
cd examples/bank
go run ../../cmd/goalchemy build
```

The first command uses the local compiler directly. The second compiles every target listed in the bank example's `goalchemy.yaml`.

| Example | What to study |
| --- | --- |
| [calc](https://github.com/eugenioenko/goalchemy/tree/main/examples/calc) | An integer expression parser and evaluator. |
| [bank](https://github.com/eugenioenko/goalchemy/tree/main/examples/bank) | Typed errors, interfaces, and deferred audit logging. |
| [wordfreq](https://github.com/eugenioenko/goalchemy/tree/main/examples/wordfreq) | Word counting, maps, sorting, and string operations. |
| [life](https://github.com/eugenioenko/goalchemy/tree/main/examples/life) | Conway's Game of Life on a toroidal board. |
| [jwt](https://github.com/eugenioenko/goalchemy/tree/main/examples/jwt) | Signing and verifying HS256 JSON Web Tokens. |
| [fetch](https://github.com/eugenioenko/goalchemy/tree/main/examples/fetch) | Fetching a CSV over HTTPS and summarizing it. |
| [pipeline](https://github.com/eugenioenko/goalchemy/tree/main/examples/pipeline) | Workers, channels, `select`, and per-job deadlines. |

The pipeline example requires the cooperative gate:

```sh
go run ./cmd/goalchemy run -gate cooperative -target python ./examples/pipeline
```

HTTP and crypto examples may need native libraries or target packages. Read the generated output's `README.md` and the relevant [target guide](/targets/) before running them.
