// core.task.spawn and the cooperative scheduler. Suspending functions are
// compiled to resumable frames: a frame holds the function's locals and the
// block to resume at, and step runs it until it returns or reaches a pause
// point. A task is a stack of frames driven by a trampoline; exactly one
// task runs at a time and runnable tasks are dispatched in FIFO order.
// Pause primitives either complete immediately, leaving their results in
// task.rv, or block the task until another task or a timer readies it.
// Deferred calls, panics, and recover are managed per task by the runtime.
import { installRuntimeHost, runtimeHost, type RuntimeHost } from "../types/host.ts";
import { Fault, GoPanic, nilDeref } from "../types/panic.ts";
import { catchPanic, formatChain, panicState, resetPanicBinding, type Deferred, type PanicState } from "../types/program.ts";
import { writeStderr } from "../types/print.ts";
import { Slice } from "../types/slice.ts";

export abstract class Frame {
  pc = 0;
  defers: Deferred[] = [];
  parent: Frame | null = null;
  panicking: GoPanic | null = null;
  abstract step(t: Task): void;
  results(): unknown[] {
    return [];
  }
}

export class Task implements PanicState {
  id: number;
  frame: Frame | null;
  rv: unknown[] = [];
  blocked = false;
  done = false;
  resumePanic: GoPanic | null = null;
  curPanic: GoPanic | null = null;
  deferTarget: unknown = undefined;
  cleanup: (() => void) | null = null;
  constructor(id: number, frame: Frame) {
    this.id = id;
    this.frame = frame;
  }
}

interface Timer {
  at: bigint;
  seq: number;
  task: Task | null;
  fn: (() => void) | null;
}

/** Thrown out of a harness case when its task blocks with nothing runnable. */
export class Blocked extends Error {}

export class FatalPanic extends Error {
  p: GoPanic;
  constructor(p: GoPanic) {
    super("fatal panic");
    this.p = p;
  }
}

/** Adapter faults bypass source panic/recover. */
export class HostFault extends Fault {}
export class NativeCanceled extends Error {}
class HostFatal extends Error {}
interface Completion { readonly id: number; readonly task: number; readonly rv: readonly unknown[]; readonly fault: HostFault | null; }
class Mailbox {
  closed = false;
  retiring = false;
  queue: Completion[] = [];
  cleaned = new Set<number>();
  live = new Map<number, number>();
  wake: (() => void) | null = null;
  notify(): void { this.wake?.(); }
}
/** Tokens retain mailbox identity and IDs only, never scheduler/frame/inputs. */
export class HostToken {
  private readonly mail: Mailbox;
  readonly operation: number;
  readonly task: number;
  constructor(mail: Mailbox, operation: number, task: number) { this.mail=mail; this.operation=operation; this.task=task; }
  complete(rv: unknown[] = [], failure: HostFault | null = null): void {
    if (this.mail.closed || this.mail.retiring || this.mail.live.get(this.operation) !== this.task) return;
    // Adapter results must be structured-cloneable owned data. No functions,
    // source descriptors, native handles or source frames cross this boundary.
    let owned: unknown[] = [];
    let fault = failure === null ? null : new HostFault(failure.message);
    try { owned = structuredClone(rv); }
    catch (e) { fault = new HostFault("uncloneable adapter result: " + String(e)); }
    this.mail.queue.push(Object.freeze({id: this.operation, task: this.task, rv: Object.freeze(owned), fault}));
    this.mail.notify();
  }
  /** Call only after native resources and copied inputs have been released. */
  acknowledgeCleanup(): void {
    if (this.mail.closed || this.mail.live.get(this.operation) !== this.task) return;
    this.mail.cleaned.add(this.operation);
    this.mail.notify();
  }
}
interface HostOperation {
  task: Task;
  cancel: () => void;
  cleanup: () => void;
  canceled: () => unknown[] | null;
  decode: (rv: readonly unknown[]) => unknown[];
}

export class Scheduler {
  runq: Task[] = [];
  cur: Task;
  main: Task;
  nextId = 1;
  rng: number;
  clock = 0n;
  timers: Timer[] = [];
  seq = 0;
  harness = false;
  readonly mail = new Mailbox();
  readonly operations = new Map<number, HostOperation>();
  readonly tasks = new Set<Task>();
  /** Native resources, such as HTTP exchanges, released when the scheduler shuts down. */
  readonly resources = new Set<() => void>();
  readonly disposers = new Set<() => void>();
  /** Native resources owned by source values; a library instance keeps them
   * past the call that created them. */
  readonly retained = new Set<() => void>();
  nextOperation = 0;
  closed = false;
  hostMode = false;
  libraryMode = false;
  boundary: (()=>void)|null = null;
  /** Identity shared by the schedulers of one library instance; native
   * objects created by any of its calls remain usable by later calls. */
  domain: object = this;
  host: RuntimeHost;
  epoch: number;
  constructor(main: Task, host: RuntimeHost = runtimeHost) {
    this.cur = main;
    this.main = main;
    this.host = host;
    this.epoch = host.now();
    this.rng = host.seed;
    this.tasks.add(main);
  }
  /** xorshift32 choice source, identical on every target. */
  choose(n: number): number {
    let x = this.rng;
    x ^= x << 13;
    x >>>= 0;
    x ^= x >>> 17;
    x ^= x << 5;
    x >>>= 0;
    this.rng = x;
    return x % n;
  }
  ready(t: Task): void {
    if (!this.closed && !t.done) { this.tasks.add(t); this.runq.push(t); }
  }
  block(t: Task): void {
    t.blocked = true;
  }
  now(): bigint {
    if (this.hostMode) {
      const measured = BigInt(Math.max(0, Math.floor((this.host.now() - this.epoch) * 1e6)));
      const bounded = measured > 9223372036854775807n ? 9223372036854775807n : measured;
      if (bounded > this.clock) this.clock = bounded;
    }
    return this.clock;
  }
  addTimer(d: bigint, task: Task | null, fn: (() => void) | null): () => void {
    return this.addTimerAt(this.now() + (d > 0n ? d : 0n), task, fn);
  }
  addTimerAt(at: bigint, task: Task | null, fn: (() => void) | null): () => void {
    const timer = { at: at > 9223372036854775807n ? 9223372036854775807n : at, seq: ++this.seq, task, fn };
    this.timers.push(timer);
    return () => { this.timers = this.timers.filter(t => t !== timer); };
  }
  registerHost(t: Task, cancel: () => void, cleanup: () => void = () => {}, canceled: () => unknown[] | null = () => null, decode: (rv: readonly unknown[]) => unknown[] = rv => [...rv]): HostToken {
    if (this.closed || !this.hostMode) throw new HostFault("host registration requires a live host driver");
    const id = ++this.nextOperation;
    this.mail.live.set(id, t.id);
    this.operations.set(id, {task: t, cancel, cleanup, canceled, decode});
    this.tasks.add(t);
    this.block(t);
    return new HostToken(this.mail, id, t.id);
  }
  /** Promise adapter boundary. Work owns native snapshots until settlement;
   * declared failures are result data, unexpected rejection is a host fault.
   * Both rejection handlers are attached immediately, including synchronous throws. */
  launchHost(token: HostToken, work: () => Promise<unknown[]>): void {
    let promise: Promise<unknown[]>;
    try { promise = work(); }
    catch (e) { token.complete([], new HostFault(String(e))); token.acknowledgeCleanup(); return; }
    Promise.resolve(promise).then(
      rv => { token.complete(rv); token.acknowledgeCleanup(); },
      e => { token.complete([], new HostFault(String(e))); token.acknowledgeCleanup(); },
    );
  }
  drainHost(): void {
    const q = this.mail.queue;
    this.mail.queue = [];
    for (const c of q) {
      const op = this.operations.get(c.id);
      if (!op || op.task.id !== c.task) continue;
      if (!this.mail.cleaned.has(c.id)) { this.mail.queue.push(c); continue; }
      this.operations.delete(c.id);
      this.mail.live.delete(c.id);
      this.mail.cleaned.delete(c.id);
      try {
        if (c.fault !== null) throw c.fault;
        op.task.rv = op.canceled() ?? op.decode(c.rv);
      } finally { op.cleanup(); }
      this.ready(op.task);
    }
  }
  wait(ms?: number): Promise<void> {
    return new Promise(resolve => {
      let cancel = () => {};
      const wake = () => { cancel(); this.mail.wake = null; resolve(); };
      this.mail.wake = wake;
      if (ms !== undefined) cancel = this.host.alarm(Math.min(2147483647, Math.max(0, ms)), wake);
    });
  }
  async nextHost(): Promise<Task> {
    for (;;) {
      this.boundary?.();
      this.fireDue(this.now());
      this.drainHost();
      if (this.runq.length) return this.runq.shift()!;
      if (!this.operations.size && !this.timers.length) throw new HostFatal("fatal error: all goroutines are asleep - deadlock!\n");
      const at = this.timers.reduce<bigint | null>((a,t) => a === null || t.at < a ? t.at : a, null);
      await this.wait(at === null ? undefined : Number(at - this.now()) / 1e6);
    }
  }
  async shutdown(): Promise<void> {
    if (this.closed) return;
    this.closed = true;
    this.mail.retiring = true;
    let failure: unknown;
    for (const release of [...this.resources]) {
      try { release(); } catch (e) { failure ??= e; }
    }
    this.resources.clear();
    for (const op of this.operations.values()) {
      try { op.cancel(); } catch (e) { failure ??= e; }
    }
    while ([...this.operations.keys()].some(id => !this.mail.cleaned.has(id))) await this.wait();
    for (const op of this.operations.values()) {
      try { op.cleanup(); } catch (e) { failure ??= e; }
    }
    const rootFailure = this.clearRoots();
    failure ??= rootFailure;
    if (failure !== undefined) throw failure;
  }
  /** Synchronous reset is only valid with no native operations. */
  retireVirtual(): void {
    if (this.closed) return;
    if (this.operations.size) throw new HostFault("pending owner requires asynchronous shutdown");
    this.closed = true;
    const failure = this.clearRoots();
    if (failure !== undefined) throw failure;
  }
  private clearRoots(): unknown {
    let failure: unknown;
    this.operations.clear();
    this.mail.closed = true;
    this.mail.queue = [];
    this.mail.cleaned.clear();
    this.mail.live.clear();
    this.mail.wake = null;
    this.boundary = null;
    this.runq = [];
    this.timers = [];
    for (const dispose of [...this.disposers, ...this.retained]) { try { dispose(); } catch (e) { failure ??= e; } }
    this.disposers.clear();
    this.retained.clear();
    for (const t of this.tasks) {
      try { t.cleanup?.(); } catch (e) { failure ??= e; }
      t.cleanup = null; t.frame = null; t.rv = []; t.blocked = false; t.done = true; t.curPanic = null; t.resumePanic = null; t.deferTarget = undefined;
    }
    this.tasks.clear();
    this.cur = this.main = null as unknown as Task;
    return failure;
  }

  next(): Task {
    while (this.runq.length === 0) {
      if (this.timers.length === 0) {
        if (this.harness) throw new Blocked();
        fatal("all goroutines are asleep - deadlock!");
      }
      this.fireTimers();
    }
    return this.runq.shift()!;
  }
  fireTimers(): void {
    let at = this.timers[0].at;
    for (const t of this.timers) if (t.at < at) at = t.at;
    this.clock = at;
    this.fireDue(at);
  }
  fireDue(at: bigint): void {
    const due = this.timers.filter(t => t.at <= at).sort((a,b) => a.at < b.at ? -1 : a.at > b.at ? 1 : a.seq - b.seq);
    this.timers = this.timers.filter(t => t.at > at);
    for (const t of due) {
      t.fn?.();
      if (t.task !== null) this.ready(t.task);
    }
  }
  /** Drives a task's frames until it blocks or finishes. */
  run(t: Task): void {
    this.cur = t;
    t.blocked = false;
    if (t.cleanup !== null) {
      const c = t.cleanup;
      t.cleanup = null;
      c();
    }
    while (!t.blocked && t.frame !== null) {
      const p = t.resumePanic;
      if (p !== null) {
        t.resumePanic = null;
        this.exit(t, t.frame, p);
        continue;
      }
      const f = t.frame;
      try {
        f.step(t);
      } catch (e) {
        this.exit(t, f, catchPanic(e));
      }
    }
  }
  /** Leaves frame f (panicking when p is set); deferred calls run first. */
  exit(t: Task, f: Frame, p: GoPanic | null): void {
    if (p !== null) {
      if (p.prev === null && f.panicking !== null && f.panicking !== p) p.prev = f.panicking;
      f.panicking = p;
    }
    t.frame = f;
    if (f.defers.length > 0) {
      const r = new DeferRunner(f);
      r.parent = f;
      t.frame = r;
      return;
    }
    this.finish(t, f);
  }
  /** Pops f and delivers its results, or its panic, to the caller. */
  finish(t: Task, f: Frame): void {
    const p = f.panicking;
    const parent = f.parent;
    t.frame = parent;
    if (parent === null) {
      t.done = true;
      t.rv = this.libraryMode && t === this.main && p === null ? f.results() : []; t.curPanic = null; t.deferTarget = undefined;
      if (t !== this.main) this.tasks.delete(t);
      if (p !== null) {
        if (this.harness || this.hostMode) throw new FatalPanic(p);
        writeStderr(formatChain(p));
        runtimeHost.fail(2);
      }
      return;
    }
    if (parent instanceof DeferRunner && parent.child === f) {
      parent.childDone(t, p);
      return;
    }
    if (p !== null) {
      this.exit(t, parent, p);
      return;
    }
    t.rv = f.results();
  }
}

/** Runs a frame's deferred calls in reverse order. */
class DeferRunner extends Frame {
  target: Frame;
  child: Frame | null = null;
  savedPanic: GoPanic | null = null;
  savedTarget: unknown = undefined;
  constructor(target: Frame) {
    super();
    this.target = target;
  }
  step(t: Task): void {
    const tf = this.target;
    while (tf.defers.length > 0) {
      const d = tf.defers.pop()!;
      this.savedPanic = t.curPanic;
      this.savedTarget = t.deferTarget;
      t.curPanic = tf.panicking;
      t.deferTarget = d.fid;
      if (d.start) {
        let child: Frame;
        try {
          if (d.f === null) throw nilDeref();
          child = d.f(...d.args);
        } catch (e) {
          t.curPanic = this.savedPanic;
          t.deferTarget = this.savedTarget;
          this.after(tf, catchPanic(e));
          continue;
        }
        this.child = child;
        child.parent = this;
        t.frame = child;
        return;
      }
      let p: GoPanic | null = null;
      try {
        if (d.f === null) throw nilDeref();
        d.f(...d.args);
      } catch (e) {
        p = catchPanic(e);
      }
      t.curPanic = this.savedPanic;
      t.deferTarget = this.savedTarget;
      this.after(tf, p);
    }
    t.frame = tf;
    sched.finish(t, tf);
  }
  childDone(t: Task, p: GoPanic | null): void {
    t.curPanic = this.savedPanic;
    t.deferTarget = this.savedTarget;
    this.child = null;
    t.frame = this;
    this.after(this.target, p);
  }
  after(tf: Frame, p: GoPanic | null): void {
    if (p !== null) {
      if (p.prev === null && tf.panicking !== null && tf.panicking !== p) p.prev = tf.panicking;
      tf.panicking = p;
      return;
    }
    if (tf.panicking !== null && tf.panicking.recovered) tf.panicking = null;
  }
}

export function fatal(msg: string): never {
  if (sched.hostMode && !sched.closed) throw new HostFatal("fatal error: " + msg + "\n");
  writeStderr("fatal error: " + msg + "\n");
  return runtimeHost.fail(2);
}

export let sched: Scheduler = new Scheduler(new Task(0, null as unknown as Frame));

/** Pushes a callee frame; the caller resumes with its results in t.rv. */
export function call(t: Task, child: Frame): void {
  child.parent = t.frame;
  t.frame = child;
}

/** Returns from frame f. */
export function ret(t: Task, f: Frame): void {
  sched.exit(t, f, null);
}

/** Runs an ordinary call as a frame. */
class SyncFrame extends Frame {
  fn: () => unknown[];
  res: unknown[] = [];
  constructor(fn: () => unknown[]) {
    super();
    this.fn = fn;
  }
  step(t: Task): void {
    this.res = this.fn();
    ret(t, this);
  }
  results(): unknown[] {
    return this.res;
  }
}

export function sync(fn: () => unknown[]): Frame {
  return new SyncFrame(fn);
}

type Fn = ((...args: any[]) => any) & { $fid?: unknown };

/** Adapts an ordinary function value with n results to the resumable form. */
export function adapt(f: Fn | null, n: number): Fn | null {
  if (f === null) return null;
  const g: Fn = (...a: any[]) =>
    sync(() => {
      const r = f(...a);
      return n === 0 ? [] : n === 1 ? [r] : r;
    });
  g.$fid = f.$fid;
  return g;
}

/** Adapts each ordinary function in a slice to the resumable form. */
export function adaptSlice(s: Slice<Fn | null>, n: number): Slice<Fn | null> {
  if (s.a === null) return s;
  const a: (Fn | null)[] = [];
  for (let i = 0; i < s.l; i++) a.push(adapt(s.a[s.o + i], n));
  return new Slice(a, 0, a.length, a.length);
}

/** go f(args): starts a task running frame f. */
export function spawn(f: Frame): void {
  const t = new Task(sched.nextId++, f);
  sched.ready(t);
}

let activeHost = false;
function install(main: Task, harness: boolean): Scheduler {
  if (activeHost) throw new HostFault("overlapping runtime entry is unsupported");
  sched.retireVirtual();
  sched = new Scheduler(main);
  sched.harness = harness;
  const owner = sched;
  panicState.current = () => owner.cur;
  return sched;
}

/** Runs the program entry as the first task until it returns. */
export function runMain(entry: Frame): void {
  const main = new Task(0, entry);
  const s = install(main, false);
  s.ready(main);
  while (!main.done) {
    try {
      s.run(s.next());
    } catch (e) {
      if (e instanceof RangeError && /call stack/.test(e.message)) {
        writeStderr("runtime: goroutine stack exceeds limit\nfatal error: stack overflow\n");
        runtimeHost.fail(2);
      }
      throw e;
    }
  }
}

/** Explicit asynchronous executable entry. Source globals are still executable
 * globals; overlapping entries and synchronous reset during a drive are rejected. */
export async function runMainHost(entry: Frame, host: RuntimeHost = runtimeHost): Promise<void> {
  if (activeHost) throw new HostFault("overlapping runtime entry is unsupported");
  const main = new Task(0, entry);
  const s = install(main, false);
  s.host = host;
  s.epoch = host.now();
  s.rng = host.seed;
  s.hostMode = true;
  activeHost = true;
  const previousHost = runtimeHost;
  installRuntimeHost(host);
  let failure: unknown;
  try {
    s.ready(main);
    while (!main.done) {
      s.run(await s.nextHost());
      // A real host turn lets Promise settlements, timers and I/O progress even
      // with an always-runnable cooperative source task.
      if (!main.done) await new Promise<void>(resolve => s.host.alarm(0, resolve));
    }
  } catch (e) { failure = e; }
  try { await s.shutdown(); } catch (e) { failure ??= e; }
  activeHost = false;
  resetPanicBinding();
  installRuntimeHost(previousHost);
  if (failure instanceof FatalPanic) {
    host.stderr(Uint8Array.from(formatChain(failure.p), c => c.charCodeAt(0) & 255));
    host.fail(2);
  }
  if (failure instanceof HostFatal || (failure instanceof RangeError && /call stack/.test(failure.message))) {
    const msg = failure instanceof HostFatal ? failure.message : "runtime: goroutine stack exceeds limit\nfatal error: stack overflow\n";
    host.stderr(Uint8Array.from(msg, c => c.charCodeAt(0) & 255));
    host.fail(2);
  }
  if (failure !== undefined) throw failure;
}

/** Requeues the running task: a pause primitive. */
export function yieldTask(t: Task): void {
  sched.ready(t);
  sched.block(t);
}

class AwaitFrame extends Frame {
  fn: (t: Task) => void;
  res: unknown[] = [];
  constructor(fn: (t: Task) => void) {
    super();
    this.fn = fn;
  }
  step(t: Task): void {
    if (this.pc === 0) {
      this.pc = 1;
      this.fn(t);
      return;
    }
    this.res = t.rv;
    ret(t, this);
  }
  results(): unknown[] {
    return this.res;
  }
}

/** Adapts Task-style native pauses for deferred calls and go statements. */
export function nativeFrame(fn:(t:Task)=>void):Frame{return new AwaitFrame(fn);}

/** Runs one pause primitive in an isolated scheduler for a harness case;
 * throws Blocked when no task can run, and the source panic on panic. */
export function runIsolated(fn: (t: Task) => void): unknown[] {
  const h = new AwaitFrame(fn);
  const main = new Task(0, h);
  const s = install(main, true);
  s.ready(main);
  try {
    while (!main.done) s.run(s.next());
  } catch (e) {
    if (e instanceof FatalPanic) throw e.p;
    throw e;
  }
  return h.res;
}

/** Installs an isolated scheduler for harness cases without pauses. */
export function resetScheduler(): void {
  install(new Task(0, null as unknown as Frame), true);
}

/** Library drives return owned results and categorized failures, never executable exit. */
export interface LibraryHooks { domain?: object; retire?: (s: Scheduler, failure: unknown) => void; }
export async function driveLibrary(entry: () => Frame, capture: (rv: unknown[]) => unknown[],
  signal?: AbortSignal, onAbort: () => void = () => {}, hooks: LibraryHooks = {}): Promise<unknown[]> {
  if (activeHost) throw new HostFault("overlapping runtime entry is unsupported");
  const main = new Task(0, null as unknown as Frame);
  const s = install(main, false);
  s.hostMode = true;
  s.libraryMode = true;
  if (hooks.domain) s.domain = hooks.domain;
  activeHost = true;
  let failure: unknown;
  let owned: unknown[] = [];
  let aborted = signal?.aborted ?? false;
  s.boundary = () => { if (aborted) onAbort(); };
  const wake = () => { aborted = true; s.mail.notify(); };
  signal?.addEventListener("abort", wake, {once: true});
  try {
    main.frame = entry();
    s.ready(main);
    while (!main.done) {
      if (aborted) onAbort();
      const t = await s.nextHost();
      if (aborted) onAbort();
      s.run(t);
      if (!main.done) await new Promise<void>(resolve => s.host.alarm(0, resolve));
    }
    owned = capture(main.rv);
    if (aborted) throw new NativeCanceled();
  } catch (e) { failure = e; }
  if (hooks.retire) { try { hooks.retire(s, failure); } catch (e) { failure ??= e; } }
  try { await s.shutdown(); } catch (e) { failure ??= e; }
  signal?.removeEventListener("abort", wake);
  activeHost = false;
  resetPanicBinding();
  if (failure !== undefined) throw failure;
  return owned;
}
