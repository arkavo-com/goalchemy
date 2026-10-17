// Canonical value encoding for the TypeScript harness.
import { Slice, NIL, BYTE_NIL, allocate, type Backing } from "../../types/slice.ts";
import { GoMap, identityKey } from "../../types/map.ts";
import { mapSet } from "../../runtime/map_store.ts";
import { mapIter, mapNext } from "../../runtime/map_iterate.ts";
import { fromHost } from "../../types/utf8.ts";
import { stdErrorsNew } from "../../runtime/std_errors_new.ts";
import type { Box } from "../../types/iface.ts";

export function decError(raw: any): Box | null {
  return raw.nil ? null : stdErrorsNew(fromHost(raw.error));
}

export function encError(v: Box | null): unknown {
  if (v === null) return { nil: true };
  return { error: Buffer.from(String(v.t.methods["Error"](v.v)), "latin1").toString("utf8") };
}

import * as rt from "./runtime_index.ts";

export function decChan<T>(raw: any, dec: (r: any) => T, zero: () => T): rt.Chan | null {
  if (raw.nil) return null;
  const ch = rt.makeChan(Number(raw.cap ?? "0"), zero);
  for (const v of raw.chan) ch.buf.push(dec(v));
  if (raw.closed) ch.closed = true;
  return ch;
}

export function encChan<T>(ch: rt.Chan | null, enc: (v: T) => unknown): unknown {
  if (ch === null) return { nil: true };
  return { chan: ch.buf.toArray().map((v) => enc(v as T)), cap: String(ch.size), closed: ch.closed };
}

export function encZero(_: unknown): unknown {
  return { zero: true };
}

/** Reports whether a spawned task ran before the parent yielded. */
class SpawnCheck extends rt.Frame {
  ran = false;
  before = false;
  step(t: rt.Task): void {
    if (this.pc === 0) {
      rt.spawn(rt.sync(() => {
        this.ran = true;
        return [];
      }));
      this.before = this.ran;
      this.pc = 1;
      rt.stdRuntimeGosched(t);
      return;
    }
    if (!this.ran) throw new Error("spawned task did not run after the parent yielded");
    rt.ret(t, this);
  }
  results(): unknown[] {
    return [this.before];
  }
}

export function newSpawnCheck(): rt.Frame {
  return new SpawnCheck();
}

export function harnessSelect2(t: rt.Task, a: rt.Chan | null, b: rt.Chan | null, dflt: boolean): void {
  rt.select(t, [{ ch: a, send: false }, { ch: b, send: false }], dflt);
}

export interface H {
  let(name: string): any;
  after(name: string, v: unknown): void;
}

const WIDE = new Set(["i64", "u64"]);

export function decInt(raw: string, kind: string): number | bigint {
  return WIDE.has(kind) ? BigInt(raw) : Number(raw);
}

export function decBool(raw: string): boolean {
  return raw === "true";
}

export function decString(raw: any): string {
  if (typeof raw.str === "string") return fromHost(raw.str);
  return Buffer.from(raw.hex, "hex").toString("latin1");
}

export function decSlice<T>(raw: any, dec: (r: any) => T, bytes = false): Slice<T> {
  if (raw.nil) return (bytes ? BYTE_NIL : NIL) as Slice<T>;
  const items: T[] = raw.slice.map(dec);
  const c = raw.cap !== undefined ? Number(raw.cap) : items.length;
  const a = allocate<T>(c, bytes);
  for (let i = 0; i < items.length; i++) a[i] = items[i];
  if (!bytes) for (let i = items.length; i < c; i++) a[i] = items.length > 0 ? items[0] : (undefined as T);
  return new Slice(a, 0, items.length, c);
}

export function view<T>(base: Slice<T>, raw: any): Slice<T> {
  const lo = raw.lo !== undefined ? Number(raw.lo) : 0;
  const hi = raw.hi !== undefined ? Number(raw.hi) : base.l;
  const max = raw.max !== undefined ? Number(raw.max) : base.c;
  return new Slice(base.a, base.o + lo, hi - lo, max - lo, base.bytes);
}

export function decMap<K, V>(raw: any, dk: (r: any) => K, dv: (r: any) => V): GoMap<K, V> | null {
  if (raw.nil) return null;
  const m = new GoMap<K, V>(identityKey);
  for (const e of raw.map) mapSet(m, dk(e.key), dv(e.value));
  return m;
}

export function encInt(v: any): string {
  return String(v);
}

export function encBool(v: any): string {
  return v ? "true" : "false";
}

export function encString(v: any): unknown {
  return { hex: Buffer.from(v, "latin1").toString("hex") };
}

export function encSlice<T>(s: Slice<T>, enc: (v: T) => unknown): unknown {
  if (s.a === null) return { nil: true };
  const items: unknown[] = [];
  for (let i = 0; i < s.l; i++) items.push(enc(s.a[s.o + i]));
  return { slice: items, cap: String(s.c) };
}

export function encArray<T>(a: Backing<T>, enc: (v: T) => unknown): unknown {
  return { array: Array.from(a, enc) };
}

export function encMap<K, V>(m: GoMap<K, V> | null, ek: (k: K) => unknown, ev: (v: V) => unknown): unknown {
  if (m === null) return { nil: true };
  const items: unknown[] = [];
  const it = mapIter(m);
  while (mapNext(it)) items.push({ key: ek(it.k as K), value: ev(it.v as V) });
  return { map: items };
}

// Opaque conformance wire only admits nil keys; real ownership has native tests.
export function decKey(raw:any):null{if(raw.nil!==true)throw new Error("opaque key cannot be fabricated");return null;}
export function encKey(value:unknown):unknown{if(value!==null)throw new Error("native key is not a conformance scalar");return {nil:true};}
class NativeHostFrame extends rt.Frame {
 fn:(t:rt.Task)=>void;res:unknown[]=[];
 constructor(fn:(t:rt.Task)=>void){super();this.fn=fn;}
 step(t:rt.Task):void{if(this.pc===0){this.pc=1;this.fn(t);return;}this.res=t.rv;rt.ret(t,this);}
 results():unknown[]{return this.res;}
}
export function harnessHost(fn:(t:rt.Task)=>void):Promise<unknown[]>{return rt.driveLibrary(()=>new NativeHostFrame(fn),rv=>rv);}
