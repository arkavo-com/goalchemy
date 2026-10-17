---
title: Cooperative concurrency
description: Use tasks, channels, cancellation, and clocks under the cooperative feature gate.
sidebar:
  order: 7
---

The `cooperative` gate enables `go` statements, channels, `select`, synchronization, and cancellation. Source tasks run on a deterministic cooperative scheduler. Host workers can perform native operations; only the scheduler owner mutates source state.

## Enable the gate

```sh
goalchemy check -gate cooperative .
goalchemy run -gate cooperative -target python .
```

For multi-target builds, set `gate: cooperative` in `goalchemy.yaml`. A `go` statement under the default sequential gate is rejected.

## Pass a value through a channel

```go
package main

func main() {
    values := make(chan int, 1)
    go func() {
        values <- 42
    }()
    println(<-values)
}
```

Tasks yield at defined scheduler operations. Cooperative execution does not provide source-visible parallel Go threads. Do not rely on host thread scheduling or use a busy loop to wait for another task.

## Use mapped synchronization and contexts

Use the Goalchemy packages for [synchronization](/runtime/lib/sync/), [contexts](/runtime/lib/context/), [virtual time](/runtime/lib/time/), and [structured waiting](/runtime/lib/task/). The [pipeline example](/getting-started/examples/) combines worker channels with per-job deadlines.

## Distinguish virtual and host time

Deterministic fixtures use virtual time. Host-driven execution uses monotonic time for pending native I/O and deadlines; wall-clock observations remain a separate capability. Generated executable entries select the appropriate driver based on their linked capabilities.

Cancellation signals native work and retires operations only after required cleanup. See [host operations](/guides/host-operations/) and each target's implementation details before integrating callbacks or driving a library from a host application.

The [language specification](/language/specification/#9-cooperative-execution) defines scheduling and source behavior. [Commands and output](/guides/usage/#cooperative-execution) documents the supported forms and HTTP options.
