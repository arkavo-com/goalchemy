// core.slice.append: append with Goalchemy's growth rule. Aggregate elements
// are copied with clone when they move to new storage.
import { Slice, type Backing } from "../types/slice.ts";
import { fault } from "../types/panic.ts";

const MAX_CAP = Number.MAX_SAFE_INTEGER;

export function growCap(old: number, required: number): number {
  const doubled = old <= MAX_CAP / 2 ? 2 * old : MAX_CAP;
  return Math.max(required, Math.max(1, doubled));
}

function grownCap(old: number, required: number): number {
  const c = growCap(old, required);
  if (c > 2 ** 32 - 1) throw fault("slice growth to " + c + " elements exceeds host limits");
  return c;
}

function byteStorage(s: Slice<unknown>, vs: unknown): boolean {
  return s.a === null ? s.bytes || vs instanceof Uint8Array : s.a instanceof Uint8Array;
}

/** Byte append; vs may view the same buffer because TypedArray.set copies as memmove. */
function appendBytes(s: Slice<number>, vs: Uint8Array | ArrayLike<number>): Slice<number> {
  const k = vs.length;
  const n = s.l + k;
  const old = s.a as Uint8Array | null;
  if (n <= s.c) {
    const p = s.o + s.l;
    if (k === 1) old![p] = vs[0];
    else old!.set(vs, p);
    return new Slice(old, s.o, n, s.c);
  }
  const c = grownCap(s.c, n);
  const a = new Uint8Array(c);
  if (old !== null && s.l > 0) a.set(old.subarray(s.o, s.o + s.l));
  a.set(vs, s.l);
  return new Slice(a, 0, n, c);
}

function moveForward<T>(dst: Backing<T>, dp: number, src: Backing<T>, sp: number, k: number, clone?: (v: T) => T): void {
  if (clone) for (let i = 0; i < k; i++) dst[dp + i] = clone(src[sp + i]);
  else for (let i = 0; i < k; i++) dst[dp + i] = src[sp + i];
}

function moveBackward<T>(dst: Backing<T>, dp: number, src: Backing<T>, sp: number, k: number, clone?: (v: T) => T): void {
  if (clone) for (let i = k - 1; i >= 0; i--) dst[dp + i] = clone(src[sp + i]);
  else for (let i = k - 1; i >= 0; i--) dst[dp + i] = src[sp + i];
}

/** Generic append of src[so:so+k]; srcClone applies to the appended values, clone to moved elements. */
function appendGeneric<T>(s: Slice<T>, src: Backing<T>, so: number, k: number, clone: ((v: T) => T) | undefined, srcClone: ((v: T) => T) | undefined): Slice<T> {
  const n = s.l + k;
  if (n <= s.c) {
    const a = s.a!;
    const p = s.o + s.l;
    if (a === src && p > so && p < so + k) moveBackward(a, p, src, so, k, srcClone);
    else moveForward(a, p, src, so, k, srcClone);
    return new Slice(a, s.o, n, s.c);
  }
  const c = grownCap(s.c, n);
  const a = new Array<T>(c);
  if (s.a !== null) moveForward(a, 0, s.a, s.o, s.l, clone);
  moveForward(a, s.l, src, so, k, srcClone);
  return new Slice(a, 0, n, c);
}

/** append(s, e1, e2, ...) with already-copied element values. */
export function append<T>(s: Slice<T>, vs: Backing<T>, clone?: (v: T) => T): Slice<T> {
  if (vs.length === 0) return s;
  if (byteStorage(s, vs)) return appendBytes(s as unknown as Slice<number>, vs as unknown as ArrayLike<number>) as unknown as Slice<T>;
  return appendGeneric(s, vs, 0, vs.length, clone, undefined);
}

/** append(s, t...) with overlap-safe moves; the result behaves as if t were copied first. */
export function appendSlice<T>(s: Slice<T>, t: Slice<T>, clone?: (v: T) => T): Slice<T> {
  if (t.l === 0) return s;
  if (t.a instanceof Uint8Array && byteStorage(s, t.a)) {
    return appendBytes(s as unknown as Slice<number>, t.a.subarray(t.o, t.o + t.l)) as unknown as Slice<T>;
  }
  return appendGeneric(s, t.a!, t.o, t.l, clone, clone);
}

/** append(b, s...) for a byte slice and a string. */
export function appendString(b: Slice<number>, s: string): Slice<number> {
  const k = s.length;
  if (k === 0) return b;
  const n = b.l + k;
  let a = b.a as Uint8Array | null;
  let o = b.o;
  let c = b.c;
  if (n > c) {
    c = grownCap(c, n);
    const grown = new Uint8Array(c);
    if (a !== null && b.l > 0) grown.set(a.subarray(o, o + b.l));
    a = grown;
    o = 0;
  }
  const p = o + b.l;
  for (let i = 0; i < k; i++) a![p + i] = s.charCodeAt(i);
  return new Slice(a, o, n, c);
}
