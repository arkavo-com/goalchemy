import { sched, type Task } from "./task_spawn.ts";
import { copyFuturePanic, type FutureState } from "./lib_task_start.ts";

export function libTaskAwait(t: Task, future: FutureState | null): void {
  if (future === null) { sched.block(t); return; }
  future.observed = true;
  sched.unawaited.delete(future);
  if (future.done) {
    if (future.panic !== null) throw copyFuturePanic(future.panic);
    t.rv = [future.results];
    return;
  }
  future.waiters.push(t);
  sched.block(t);
}
