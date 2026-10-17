namespace Rt;

public static partial class R
{
    public static void libTaskAwait(GoTask t, FutureState future)
    {
        if (future == null) { sched.block(t); return; }
        future.observed = true; sched.unawaited.RemoveAll(item => ReferenceEquals(item.key, future));
        if (future.done) {
            if (future.panic != null) throw copyFuturePanic(future.panic);
            t.rv = new object[] { future.results };
            return;
        }
        future.waiters.Add(t); sched.block(t);
    }
}
