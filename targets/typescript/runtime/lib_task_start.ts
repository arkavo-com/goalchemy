import { call, Frame, ret, sched, spawn, type Task } from "./task_spawn.ts";
import { GoPanic, nilDeref } from "../types/panic.ts";
import type { Box } from "../types/iface.ts";
import type { Slice } from "../types/slice.ts";

export class FutureState {
  done = false;
  observed = false;
  results: Slice<Box | null> | null = null;
  panic: GoPanic | null = null;
  waiters: Task[] = [];
}

export function copyFuturePanic(p: GoPanic | null): GoPanic | null {
  if (p === null) return null;
  const result = new GoPanic(p.value);
  result.prev = copyFuturePanic(p.prev);
  return result;
}

class FutureChild extends Frame {
  readonly fn: (() => unknown) | null;
  constructor(fn: (() => unknown) | null, future: FutureState) {
    super();
    this.fn = fn;
    this.completion = (_t, results, p) => {
      future.done = true;
      future.panic = p;
      if (p === null) future.results = results[0] as Slice<Box | null>;
      if (p !== null && !future.observed) sched.unawaited.set(future, p);
      for (const waiter of future.waiters) {
        waiter.rv = [future.results];
        waiter.resumePanic = copyFuturePanic(p);
        sched.ready(waiter);
      }
      future.waiters = [];
    };
  }
  private values: unknown[] = [];
  results(): unknown[] { return this.values; }
  step(t: Task): void {
    if (this.pc === 0) {
      this.pc = 1;
      if (this.fn === null) throw nilDeref();
      const result = this.fn();
      if (result instanceof Frame) { call(t, result); return; }
      this.values = [result];
    } else this.values = t.rv;
    ret(t, this);
  }
}

export function libTaskStart(value: Box | (() => unknown) | null): FutureState {
  const fn = typeof value === "function" ? value : value?.v;
  const future = new FutureState();
  spawn(new FutureChild(fn, future));
  return future;
}
