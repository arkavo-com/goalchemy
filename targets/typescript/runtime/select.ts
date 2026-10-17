// core.select: commit exactly one ready case; a pause primitive whose
// results arrive in t.rv as [index, value, ok], index -1 for the default.
import { dequeue, hasLive, SelectState, tryRecv, Waiter, type Chan } from "./chan_make.ts";
import { plainPanic } from "../types/panic.ts";
import { sched, type Queue, type Task } from "./task_spawn.ts";

export interface SelectCase {
  ch: Chan | null;
  send: boolean;
  val?: unknown;
}

export function select(t: Task, cases: SelectCase[], hasDefault: boolean): void {
  const ready: number[] = [];
  cases.forEach((c, i) => {
    const ch = c.ch;
    if (ch === null) return;
    if (c.send) {
      if (ch.closed || hasLive(ch.recvq) || ch.buf.length < ch.size) ready.push(i);
    } else if (ch.buf.length > 0 || hasLive(ch.sendq) || ch.closed) {
      ready.push(i);
    }
  });
  if (ready.length > 0) {
    const i = ready[sched.choose(ready.length)];
    const c = cases[i];
    const ch = c.ch!;
    if (c.send) {
      if (ch.closed) throw plainPanic("send on closed channel");
      const w = dequeue(ch.recvq);
      if (w !== null) w.recvDone(c.val, true);
      else ch.buf.push(c.val);
      t.rv = [i, undefined, false];
      return;
    }
    const [v, ok] = tryRecv(ch);
    t.rv = [i, v, ok];
    return;
  }
  if (hasDefault) {
    t.rv = [-1, undefined, false];
    return;
  }
  const st = new SelectState();
  const registrations: [Queue<Waiter>, Waiter][] = [];
  cases.forEach((c, i) => {
    if (c.ch === null) return;
    const w = new Waiter(t, c.send ? c.val : undefined, st, i);
    const q = c.send ? c.ch.sendq : c.ch.recvq;
    q.push(w); registrations.push([q,w]);
  });
  sched.block(t);
  t.cleanup = () => {
    st.done = true;
    for (const [q,w] of registrations) {
      q.remove(w);
      w.clear();
    }
    registrations.length = 0;
  };
}
