namespace Rt;

public sealed class FutureState
{
    internal bool done, observed = false;
    internal Slice results;
    internal GoPanic panic;
    internal readonly List<GoTask> waiters = new();
}

sealed class FutureChild : Frame
{
    readonly Fn fn;
    object[] values = Array.Empty<object>();
    internal FutureChild(Fn fn, FutureState future)
    {
        this.fn = fn;
        completion = (_, results, p) => {
            future.done = true; future.panic = p;
            if (p == null) future.results = (Slice)results[0];
            if (p != null && !future.observed) R.sched.unawaited.Add((future, p));
            foreach (var waiter in future.waiters) {
                waiter.rv = new object[] { future.results };
                waiter.resumePanic = R.copyFuturePanic(p);
                R.sched.ready(waiter);
            }
            future.waiters.Clear();
        };
    }
    public override object[] results() => values;
    public override void step(GoTask t)
    {
        if (pc == 0) {
            pc = 1;
            if (fn == null) throw Panics.nilDeref();
            var result = fn.Call();
            if (result is Frame frame) { R.call(t, frame); return; }
            values = new[] { result };
        } else values = t.rv;
        R.ret(t, this);
    }
}

public static partial class R
{
    public static FutureState libTaskStart(Box fn)
    {
        var future = new FutureState();
        spawn(new FutureChild((Fn)fn?.v, future));
        return future;
    }
    internal static GoPanic copyFuturePanic(GoPanic p) => p == null ? null : new GoPanic(p.value) { prev = copyFuturePanic(p.prev) };
}
