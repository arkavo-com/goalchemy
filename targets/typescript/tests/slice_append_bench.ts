// Diagnostic evidence for append throughput, never a pass/fail timing test.
import assert from "node:assert/strict";
import { performance } from "node:perf_hooks";
import { BYTE_NIL, NIL, Slice, zeroAppendGrowth } from "../types/slice.ts";
import { makeSlice } from "../runtime/slice_make.ts";
import { copy } from "../runtime/slice_copy.ts";
import { reslice } from "../runtime/slice_slice.ts";
import { append, appendSlice } from "../runtime/slice_append.ts";

const MiB = 1024 * 1024;
const total = 10 * MiB;
const ints = 1 << 20;
const runs = Number(process.argv[2] ?? 7);

function chunk(n: number): Slice<number> {
  const s = makeSlice(n, n, () => 0, true);
  for (let i = 0; i < n; i++) s.a![i] = i & 255;
  return s;
}

const c64k = chunk(64 * 1024);
const c32k = chunk(32 * 1024);

const scenarios: Record<string, () => number> = {
  "bytes 64KiB chunks": () => {
    let b = BYTE_NIL;
    for (let i = 0; i < total / c64k.l; i++) b = zeroAppendGrowth(appendSlice(b, c64k), b, () => 0);
    return b.l;
  },
  "bytes 1-byte append": () => {
    let b = BYTE_NIL;
    for (let i = 0; i < total; i++) b = zeroAppendGrowth(append(b, [i & 255]), b, () => 0);
    return b.l;
  },
  "bytes ReadAll growth": () => {
    let b = makeSlice(0, 512, () => 0, true);
    while (b.l < total) {
      const n = Number(copy(reslice(b, b.l, b.c), c32k));
      b = reslice(b, 0, b.l + n);
      if (b.l === b.c) {
        const g = zeroAppendGrowth(append(b, [0]), b, () => 0);
        b = reslice(g, 0, b.l);
      }
    }
    return b.l;
  },
  "[]int 1-element append": () => {
    let s: Slice<bigint> = NIL;
    for (let i = 0; i < ints; i++) s = zeroAppendGrowth(append(s, [BigInt(i)]), s, () => 0n);
    return s.l;
  },
  "[]int 4096-element chunks": () => {
    const t = makeSlice<bigint>(4096, 4096, () => 1n);
    let s: Slice<bigint> = NIL;
    for (let i = 0; i < ints / t.l; i++) s = zeroAppendGrowth(appendSlice(s, t), s, () => 0n);
    return s.l;
  },
  "[]int delete append(s[:i], s[i+1:]...)": () => {
    let s = makeSlice<bigint>(4096, 4096, () => 0n);
    for (let i = 0; i < s.l; i++) s.a![i] = BigInt(i);
    for (let k = 0; k < 2048; k++) s = zeroAppendGrowth(appendSlice(reslice(s, 0, 1), reslice(s, 2)), reslice(s, 0, 1), () => 0n);
    return s.l;
  },
};

const times: Record<string, number[]> = {};
for (const name of Object.keys(scenarios)) times[name] = [];
for (let r = -1; r < runs; r++) {
  for (const [name, fn] of Object.entries(scenarios)) {
    const start = performance.now();
    const n = fn();
    const ms = performance.now() - start;
    assert.ok(n > 0);
    if (r >= 0) times[name].push(ms);
  }
}
const median = (xs: number[]) => [...xs].sort((a, b) => a - b)[xs.length >> 1];
console.log(JSON.stringify({node: process.version, runs, medianMs: Object.fromEntries(Object.entries(times).map(([k, v]) => [k, Number(median(v).toFixed(2))]))}));
