// lib.task.all: run each function as a task, in order, and resume the
// caller when all have finished; a pause primitive. The functions arrive
// in the resumable form (each call returns a frame), or, from a native-pause
// caller, as functions taking the task last.
import { call, Frame, NativeTask, ret, sched, spawn, type Task } from "./task_spawn.ts";
import type { Slice } from "../types/slice.ts";
import { nilDeref } from "../types/panic.ts";

interface AllState {
  n: number;
  waiter: Task;
}

class AllChild extends Frame {
  start: () => Frame;
  st: AllState;
  constructor(start: () => Frame, st: AllState) {
    super();
    this.start = start;
    this.st = st;
  }
  step(t: Task): void {
    if (this.pc === 0) {
      this.pc = 1;
      call(t, this.start());
      return;
    }
    this.st.n--;
    if (this.st.n === 0) sched.ready(this.st.waiter);
    ret(t, this);
  }
}

export function libTaskAll(t: Task, fns: Slice<((...a: any[]) => any) | null>): void {
  t.rv = [];
  if (fns.l === 0) return;
  const st: AllState = { n: fns.l, waiter: t };
  const native = t.frame instanceof NativeTask;
  for (let i = 0; i < fns.l; i++) {
    const f = fns.a![fns.o + i];
    if (native) {
      spawn(new NativeTask(async (c: Task) => {
        if (f === null) throw nilDeref();
        await f(c);
        if (--st.n === 0) sched.ready(st.waiter);
      }, 0));
      continue;
    }
    spawn(
      new AllChild(() => {
        if (f === null) throw nilDeref();
        return f();
      }, st),
    );
  }
  sched.block(t);
}
