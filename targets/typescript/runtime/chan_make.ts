// core.chan.make: make(chan T, size), and the channel state shared by the
// channel and select runtime functions.
import { GoPanic, plainPanic } from "../types/panic.ts";
import { Queue, sched, type Task } from "./task_spawn.ts";

export class SelectState {
  done = false;
}

export class Waiter {
  task: Task | null;
  readonly owner = sched;
  val: unknown;
  sel: SelectState | null;
  idx: number;
  constructor(task: Task, val: unknown, sel: SelectState | null = null, idx = 0) {
    this.task = task;
    this.val = val;
    this.sel = sel;
    this.idx = idx;
  }
  live(): boolean { return this.task !== null && !this.task.done && !this.owner.closed && (this.sel === null || !this.sel.done); }
  clear(): void { this.task = null; this.val = undefined; }
  /** Completes a waiting receiver with a value or closure. */
  recvDone(val: unknown, ok: boolean): void {
    if (!this.live()) { this.clear(); return; }
    const task = this.task!;
    if (this.sel !== null) {
      this.sel.done = true;
      task.rv = [this.idx, val, ok];
    } else {
      task.rv = [val, ok];
    }
    this.clear();
    this.owner.ready(task);
  }
  /** Completes a waiting sender; closed makes it panic when it resumes. */
  sendDone(closed: boolean): void {
    if (!this.live()) { this.clear(); return; }
    const task = this.task!;
    if (this.sel !== null) {
      this.sel.done = true;
      task.rv = [this.idx, undefined, false];
    } else {
      task.rv = [];
    }
    if (closed) task.resumePanic = plainPanic("send on closed channel") as GoPanic;
    this.clear();
    this.owner.ready(task);
  }
}

export class Chan {
  buf = new Queue<unknown>();
  size: number;
  closed = false;
  recvq = new Queue<Waiter>();
  sendq = new Queue<Waiter>();
  zero: () => unknown;
  constructor(size: number, zero: () => unknown) {
    this.size = size;
    this.zero = zero;
  }
}

/** Removes and returns the first waiter that can still complete. */
export function dequeue(q: Queue<Waiter>): Waiter | null {
  while (q.length > 0) {
    const w = q.shift()!;
    if (w.live()) return w;
    w.clear();
  }
  return null;
}

export function hasLive(q: Queue<Waiter>): boolean {
  return q.some(w => w.live());
}

/** Receives without blocking: [value, ok, done]. */
export function tryRecv(ch: Chan): [unknown, boolean, boolean] {
  if (ch.buf.length > 0) {
    const v = ch.buf.shift();
    const w = dequeue(ch.sendq);
    if (w !== null) {
      ch.buf.push(w.val);
      w.sendDone(false);
    }
    return [v, true, true];
  }
  const w = dequeue(ch.sendq);
  if (w !== null) {
    const v = w.val;
    w.sendDone(false);
    return [v, true, true];
  }
  if (ch.closed) return [ch.zero(), false, true];
  return [undefined, false, false];
}

export function makeChan(size: number | bigint, zero: () => unknown = () => undefined): Chan {
  if (size < 0 || size > Number.MAX_SAFE_INTEGER) throw plainPanic("makechan: size out of range");
  return new Chan(Number(size), zero);
}

/** Install one removable source waiter; task cleanup also runs on retirement. */
export function parkWaiter(t: Task, q: Queue<Waiter>, w: Waiter): void {
  q.push(w);
  t.cleanup = () => {
    q.remove(w);
    w.clear();
  };
  sched.block(t);
}
