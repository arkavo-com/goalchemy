package rt;

public final class LibTaskAwait {
    private LibTaskAwait() {}
    public static void libTaskAwait(TaskSpawn.Task t, LibTaskStart.FutureState future) {
        if(future == null) { TaskSpawn.sched.block(t); return; }
        future.observed = true; TaskSpawn.sched.unawaited.remove(future);
        if(future.done) {
            if(future.panic != null) throw LibTaskStart.copyFuturePanic(future.panic);
            t.rv = new Object[]{future.results};
            return;
        }
        future.waiters.add(t); TaskSpawn.sched.block(t);
    }
}
