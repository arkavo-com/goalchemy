// Representation checks complement the independently evaluated Go fixture.
import assert from "node:assert/strict";
import test from "node:test";
import { BYTE_NIL, NIL, Slice, fromArray, toArray } from "../types/slice.ts";
import { makeSlice } from "../runtime/slice_make.ts";
import { append, appendSlice, appendString, growCap } from "../runtime/slice_append.ts";
import { copy } from "../runtime/slice_copy.ts";
import { clearSlice } from "../runtime/slice_clear.ts";
import { reslice, sliceArray } from "../runtime/slice_slice.ts";
import { sliceToArray } from "../runtime/slice_to_array.ts";
import { toBytes } from "../runtime/string_to_bytes.ts";
import { fromBytes } from "../runtime/string_from_bytes.ts";

import { decSlice, view } from "./harness/codec.ts";

function native(b: Slice<number>): Uint8Array {
  assert.ok(b.a instanceof Uint8Array);
  assert.equal(b.a.byteLength, b.c + b.o);
  return b.a;
}

test("byte construction and nil append retain native storage", () => {
  assert.equal(append(BYTE_NIL, []), BYTE_NIL);
  assert.equal(appendSlice(BYTE_NIL, BYTE_NIL), BYTE_NIL);
  assert.equal(reslice(BYTE_NIL, 0, 0, 0), BYTE_NIL);
  assert.equal(BYTE_NIL.a, null);
  native(append(reslice(BYTE_NIL, 0), [255, 128, 0]));
  native(makeSlice(0, 0, () => 0, true));
  const b = makeSlice(2, 8, () => 0, true);
  assert.deepEqual([...native(b)], [0,0,0,0,0,0,0,0]);
  const a = native(b);
  const grown = append(b, [1,2,3,4,5,6,7]);
  assert.notEqual(native(grown), a);
  assert.equal(grown.c, 16);
  assert.deepEqual([...native(grown).subarray(9)], [0,0,0,0,0,0,0]);
  const decoded = decSlice({slice:["255"], cap:"3"}, Number, true);
  assert.deepEqual([...native(decoded)], [255,0,0]);
  native(append(view(BYTE_NIL, {}), [1]));
  const host = new Uint8Array([1,2,3]);
  assert.equal(fromArray(host).a, host);
  assert.equal(sliceArray(host, 1).a, host);
  const array = sliceToArray(sliceArray(host), 2);
  assert.ok(array instanceof Uint8Array);
  array[0] = 9;
  assert.equal(host[0], 1);
  assert.ok(sliceToArray(BYTE_NIL, 0) instanceof Uint8Array);
});

test("typed copy/append/clear retain aliased buffers", () => {
  const b = toBytes("\x01\x02\x03\x04\x05\x06");
  const a = native(b);
  copy(reslice(b, 1), b);
  assert.deepEqual([...a], [1,1,2,3,4,5]);
  copy(b, reslice(b, 2));
  assert.deepEqual([...a], [2,3,4,5,4,5]);
  const appended = appendSlice(reslice(b, 0, 2), reslice(b, 1, 5));
  assert.equal(appended.a, a);
  assert.deepEqual([...a], [2,3,3,4,5,4]);
  clearSlice(reslice(b, 2, 4), () => 0);
  assert.deepEqual([...a], [2,3,0,0,5,4]);
  const detached = appendSlice(reslice(b, 1, 3, 3), b);
  assert.notEqual(native(detached), a);
  assert.deepEqual(toArray(detached), [3,0,2,3,0,0,5,4]);
});

test("arbitrary byte strings copy independently; other elements stay generic", () => {
  const s = "\x00\xff\x80\xc0\xafA";
  const b = toBytes(s);
  const a = native(b);
  assert.equal(fromBytes(b), s);
  a[0] = 100;
  assert.equal(s.charCodeAt(0), 0);
  const out = appendString(BYTE_NIL, s);
  assert.equal(fromBytes(out), s);
  assert.ok(toBytes("").a instanceof Uint8Array);
  const generic = append(NIL, [1000, -1]);
  assert.ok(Array.isArray(generic.a));
  assert.deepEqual(toArray(generic), [1000, -1]);
  assert.ok(Array.isArray(makeSlice(2, 3, () => 0n).a));
});

test("append moves in bulk with the unchanged growth rule and overlap semantics", () => {
  assert.deepEqual([0, 1, 2, 3, 5, 8].map(c => growCap(c, c + 1)), [1, 2, 4, 6, 10, 16]);
  assert.equal(growCap(2, 7), 7);
  const b = toBytes("\x01\x02\x03\x04");
  const old = native(b);
  const grown = append(b, [5]);
  assert.notEqual(native(grown), old);
  assert.equal(grown.c, 8);
  assert.deepEqual([...native(grown)], [1,2,3,4,5,0,0,0]);
  assert.deepEqual([...old], [1,2,3,4]);
  const room = append(reslice(grown, 0, 2), [9, 300]);
  assert.equal(room.a, grown.a);
  assert.deepEqual([...native(grown)], [1,2,9,44,5,0,0,0]);
  const str = appendString(reslice(grown, 0, 1), "\xff\x00");
  assert.equal(str.a, grown.a);
  assert.deepEqual(toArray(str), [1,255,0]);
  const strGrown = appendString(b, "\x80");
  assert.equal(strGrown.c, 8);
  assert.deepEqual(toArray(strGrown), [1,2,3,4,128]);
  native(appendSlice(NIL, toBytes("ab")));

  type Box = {v: number};
  const clone = (x: Box): Box => ({v: x.v});
  const boxes = (): Slice<Box> => {
    const s = makeSlice<Box>(4, 6, () => ({v: 0}));
    for (let i = 0; i < 4; i++) s.a![i] = {v: i + 1};
    return s;
  };
  const vals = (s: Slice<Box>) => toArray(s).map(x => x.v);
  const fwd = boxes();
  const before = toArray(fwd);
  const up = appendSlice(reslice(fwd, 0, 2), reslice(fwd, 1, 4), clone);
  assert.equal(up.a, fwd.a);
  assert.deepEqual(vals(up), [1,2,2,3,4]);
  assert.equal(up.a![0], before[0]);
  assert.notEqual(up.a![2], before[1]);
  const back = boxes();
  const down = appendSlice(reslice(back, 0, 1), reslice(back, 2, 4), clone);
  assert.deepEqual(vals(down), [1,3,4]);
  assert.deepEqual(vals(back), [1,3,4,4]);
  const src = boxes();
  const kept = toArray(src);
  const moved = appendSlice(src, reslice(src, 0, 3), clone);
  assert.notEqual(moved.a, src.a);
  assert.equal(moved.c, 12);
  assert.deepEqual(vals(moved), [1,2,3,4,1,2,3]);
  for (let i = 0; i < moved.l; i++) assert.ok(!kept.includes(moved.a![i]));
  assert.deepEqual(toArray(src), kept);
  const literal = append(reslice(src, 0, 4, 4), [kept[0]], clone);
  assert.equal(literal.a![4], kept[0]);
  assert.notEqual(literal.a![0], kept[0]);
});
