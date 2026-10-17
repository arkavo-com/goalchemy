package rt;

import java.util.ArrayList;

public final class LibTaskStart {
    private LibTaskStart() {}
    public static final class FutureState {
        boolean done, observed;
        Slice results;
        GoPanic panic;
        final ArrayList<TaskSpawn.Task> waiters = new ArrayList<>();
    }
    static final class FutureChild extends TaskSpawn.Frame {
        final Fn fn;
        Object[] values = new Object[0];
        FutureChild(Fn fn, FutureState future) {
            this.fn = fn;
            completion = (t, results, p) -> {
                future.done = true; future.panic = p;
                if(p == null) future.results = (Slice)results[0];
                if(p != null && !future.observed) TaskSpawn.sched.unawaited.put(future,p);
                for(var waiter : future.waiters) {
                    waiter.rv = new Object[]{future.results};
                    waiter.resumePanic = copyFuturePanic(p);
                    TaskSpawn.sched.ready(waiter);
                }
                future.waiters.clear();
            };
        }
        public Object[] results() { return values; }
        public void step(TaskSpawn.Task t) {
            if(pc == 0) {
                pc = 1;
                if(fn == null) throw Panics.nilDeref();
                Object result = fn.call();
                if(result instanceof TaskSpawn.Frame frame) { TaskSpawn.call(t,frame); return; }
                values = new Object[]{result};
            } else values = t.rv;
            TaskSpawn.ret(t,this);
        }
    }
    public static FutureState libTaskStart(Box fn) {
        var future = new FutureState();
        TaskSpawn.spawn(new FutureChild(fn == null ? null : (Fn)fn.v,future));
        return future;
    }
    static GoPanic copyFuturePanic(GoPanic p) {
        if(p == null) return null;
        var result = new GoPanic(p.value);
        result.prev = copyFuturePanic(p.prev);
        return result;
    }
}
