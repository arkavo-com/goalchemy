---
title: Java
description: Java target setup, generated output, native library boundaries, and implementation details.
---

## Prerequisites

JDK 21 or later. `JAVA_HOME` can select the JDK used by the runner. Production crypto also uses the pinned Bouncy Castle dependency described by the generated output.

## Compile and run

From a prepared source module containing a `main` package:

```sh
goalchemy compile -target java -out out/java .
cd out/java
sh run.sh
```

Or use `goalchemy run -target java .` to compile and execute in a temporary directory. Add `-gate cooperative` for source tasks or suspending operations.

## Generated output

The Java target emits `Main.java`, package-owned `pkg_*.java` holders, `_GoalchemySupport.java`, selected `rt/` files, and `run.sh`. The runner compiles the explicit source inventory with `javac`.

Every output includes its own `README.md`, selected runtime files, source mappings, and `goalchemy.manifest.json`. See the [full output reference](/guides/usage/#output) for the inventory and entry behavior.

## Native libraries

Non-`main` packages emit a native named-package JAR with typed values, byte arrays, and cancellable asynchronous operations. Library output includes `Generated.java` and `build.sh`. Read the [Java library boundary](/targets/details/java-library-boundary/) for public API shapes and ownership.

## Runtime and diagnostics

Byte arrays and slices use native `byte[]` storage with explicit unsigned reads. `.java.lines` sidecars map generated diagnostics. Cooperative `Main.runHost()` serializes executable globals and returns after required cleanup.

## Implementation details

[Library boundary](/targets/details/java-library-boundary/) · [Host operations](/targets/details/java-host-operations/) · [Byte storage](/targets/details/java-byte-storage/)
