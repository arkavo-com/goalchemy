using System.Runtime.CompilerServices;
using System.Runtime.ExceptionServices;

namespace Rt;

/// <summary>The native pause model: functions that may pause are async
/// methods returning Async or Async&lt;T&gt;. Their builders never touch the
/// .NET thread pool, SynchronizationContext or TaskScheduler: a method that
/// finishes without suspending allocates nothing, a suspended one is resumed
/// only when its awaited callee completes or when the owner's scheduler
/// chooses its task again (R.wait).</summary>
[AsyncMethodBuilder(typeof(AsyncBuilder))]
public readonly struct Async
{
    internal readonly Promise p;

    internal Async(Promise p) => this.p = p;

    public Awaiter GetAwaiter() => new Awaiter(p);

    public readonly struct Awaiter : ICriticalNotifyCompletion
    {
        readonly Promise p;

        internal Awaiter(Promise p) => this.p = p;

        public bool IsCompleted => p == null || p.done;
        public void GetResult() => p?.check();
        public void OnCompleted(Action k) => p.next = k;
        public void UnsafeOnCompleted(Action k) => p.next = k;
    }
}

[AsyncMethodBuilder(typeof(AsyncBuilder<>))]
public readonly struct Async<T>
{
    readonly T value;
    internal readonly Promise<T> p;

    internal Async(T value, Promise<T> p)
    {
        this.value = value;
        this.p = p;
    }

    public Awaiter GetAwaiter() => new Awaiter(value, p);

    public readonly struct Awaiter : ICriticalNotifyCompletion
    {
        readonly T value;
        readonly Promise<T> p;

        internal Awaiter(T value, Promise<T> p)
        {
            this.value = value;
            this.p = p;
        }

        public bool IsCompleted => p == null || p.done;
        public T GetResult()
        {
            if (p == null) return value;
            p.check();
            return p.value;
        }
        public void OnCompleted(Action k) => p.next = k;
        public void UnsafeOnCompleted(Action k) => p.next = k;
    }
}

/// <summary>The completion of a suspended async method; its one awaiter's
/// continuation runs as soon as it completes.</summary>
public class Promise
{
    internal bool done;
    internal Action next;
    Exception error;

    internal void check()
    {
        if (error != null) ExceptionDispatchInfo.Throw(error);
    }

    internal void complete(Exception e)
    {
        error = e;
        done = true;
        var k = next;
        next = null;
        k?.Invoke();
    }
}

public class Promise<T> : Promise
{
    internal T value;
}

sealed class Machine<M> : Promise where M : IAsyncStateMachine
{
    internal M sm;
    internal readonly Action step;

    internal Machine() => step = run;

    void run() => sm.MoveNext();
}

sealed class Machine<M, T> : Promise<T> where M : IAsyncStateMachine
{
    internal M sm;
    internal readonly Action step;

    internal Machine() => step = run;

    void run() => sm.MoveNext();
}

public struct AsyncBuilder
{
    Promise p;

    public static AsyncBuilder Create() => default;
    public Async Task => new Async(p);
    public void Start<M>(ref M sm) where M : IAsyncStateMachine => sm.MoveNext();
    public void SetStateMachine(IAsyncStateMachine sm) { }
    public void SetResult() => p?.complete(null);
    public void SetException(Exception e) => (p ??= new Promise()).complete(e);
    public void AwaitOnCompleted<A, M>(ref A a, ref M sm) where A : INotifyCompletion where M : IAsyncStateMachine => a.OnCompleted(box(ref sm));
    public void AwaitUnsafeOnCompleted<A, M>(ref A a, ref M sm) where A : ICriticalNotifyCompletion where M : IAsyncStateMachine => a.UnsafeOnCompleted(box(ref sm));

    Action box<M>(ref M sm) where M : IAsyncStateMachine
    {
        if (p != null) return ((Machine<M>)p).step;
        var m = new Machine<M>();
        p = m;
        m.sm = sm;
        return m.step;
    }
}

public struct AsyncBuilder<T>
{
    Promise<T> p;
    T value;

    public static AsyncBuilder<T> Create() => default;
    public Async<T> Task => new Async<T>(value, p);
    public void Start<M>(ref M sm) where M : IAsyncStateMachine => sm.MoveNext();
    public void SetStateMachine(IAsyncStateMachine sm) { }
    public void SetResult(T v)
    {
        if (p == null)
        {
            value = v;
            return;
        }
        p.value = v;
        p.complete(null);
    }
    public void SetException(Exception e) => (p ??= new Promise<T>()).complete(e);
    public void AwaitOnCompleted<A, M>(ref A a, ref M sm) where A : INotifyCompletion where M : IAsyncStateMachine => a.OnCompleted(box(ref sm));
    public void AwaitUnsafeOnCompleted<A, M>(ref A a, ref M sm) where A : ICriticalNotifyCompletion where M : IAsyncStateMachine => a.UnsafeOnCompleted(box(ref sm));

    Action box<M>(ref M sm) where M : IAsyncStateMachine
    {
        if (p != null) return ((Machine<M, T>)p).step;
        var m = new Machine<M, T>();
        p = m;
        m.sm = sm;
        return m.step;
    }
}

/// <summary>A task frame whose body is an async method. The scheduler hands
/// it the turn (Scheduler.handoff); the driver's trampoline then starts the
/// body or resumes it where it parked, so task bodies never nest on each
/// other's stacks.</summary>
public abstract class NativeFrame : Frame
{
    internal GoTask task;
    internal bool started;
    internal Action cont;
    internal object[] res = Array.Empty<object>();

    public override void step(GoTask t)
    {
        task = t;
        t.owner.handoff = this;
    }

    public override object[] results() => res;

    internal void resume()
    {
        if (started)
        {
            var k = cont;
            cont = null;
            k();
            return;
        }
        started = true;
        start();
    }

    protected abstract void start();

    /// <summary>Runs the body as a callee of another body on the same task.</summary>
    internal abstract Async<object[]> call(GoTask t);
}

sealed class NativeVoid : NativeFrame
{
    readonly Func<GoTask, Async> body;
    Async.Awaiter a;

    internal NativeVoid(Func<GoTask, Async> body) => this.body = body;

    protected override void start()
    {
        try { a = body(task).GetAwaiter(); }
        catch (Exception e) { task.owner.nativeDone(task, this, e); return; }
        if (a.IsCompleted) done();
        else a.UnsafeOnCompleted(done);
    }

    void done()
    {
        Exception failure = null;
        try { a.GetResult(); }
        catch (Exception e) { failure = e; }
        a = default;
        task.owner.nativeDone(task, this, failure);
    }

    internal override async Async<object[]> call(GoTask t)
    {
        await body(t);
        return Array.Empty<object>();
    }
}

sealed class NativeBody<T> : NativeFrame
{
    readonly Func<GoTask, Async<T>> body;
    readonly int n;
    Async<T>.Awaiter a;

    internal NativeBody(Func<GoTask, Async<T>> body, int n)
    {
        this.body = body;
        this.n = n;
    }

    protected override void start()
    {
        try { a = body(task).GetAwaiter(); }
        catch (Exception e) { task.owner.nativeDone(task, this, e); return; }
        if (a.IsCompleted) done();
        else a.UnsafeOnCompleted(done);
    }

    void done()
    {
        Exception failure = null;
        try { res = pack(a.GetResult()); }
        catch (Exception e) { failure = e; }
        a = default;
        task.owner.nativeDone(task, this, failure);
    }

    object[] pack(T v) => n == 1 ? new object[] { v } : (object[])(object)v;

    internal override async Async<object[]> call(GoTask t) => pack(await body(t));
}

/// <summary>Awaits the scheduler's choice of a task that a pause primitive
/// left blocked; a panic the waker left for the task is raised here.</summary>
public readonly struct Wait : ICriticalNotifyCompletion
{
    readonly GoTask t;
    readonly bool ready;

    internal Wait(GoTask t, bool ready)
    {
        this.t = t;
        this.ready = ready;
    }

    public Wait GetAwaiter() => this;
    public bool IsCompleted => ready;
    public void GetResult()
    {
        var p = t.resumePanic;
        if (p == null) return;
        t.resumePanic = null;
        throw p;
    }
    public void OnCompleted(Action k) => ((NativeFrame)t.frame).cont = k;
    public void UnsafeOnCompleted(Action k) => ((NativeFrame)t.frame).cont = k;
}

public static partial class R
{
    /// <summary>The frame of a void async body, for go, defer, values and entries.</summary>
    public static Frame nativeTask(Func<GoTask, Async> body) => new NativeVoid(body);

    /// <summary>The frame of an async body with n results.</summary>
    public static Frame nativeTask<T>(Func<GoTask, Async<T>> body, int n) => new NativeBody<T>(body, n);

    /// <summary>Completes a pause primitive that left native task t blocked:
    /// the scheduler runs other tasks until it chooses t again.</summary>
    public static Wait wait(GoTask t) => new Wait(t, sched.turn(t));

    /// <summary>Runs a function value's or method's frame on native task t
    /// and leaves its results in t.rv.</summary>
    public static async Async awaitFrame(GoTask t, Frame f)
    {
        switch (f)
        {
            case NativeFrame n:
                t.rv = await n.call(t);
                return;
            case SyncFrame s:
                t.rv = s.fn();
                return;
            case AwaitFrame a:
                a.fn(t);
                if (t.blocked) await wait(t);
                return;
        }
        throw new HostFault("synchronous call paused");
    }

    /// <summary>Runs a native function's deferred calls, awaiting those that
    /// pause; recover and the panic chain behave as in Program.runDefers.</summary>
    public static async Async runDefersAsync(GoTask t, List<Deferred> ds, GoPanic p)
    {
        var panicking = p;
        while (ds.Count > 0)
        {
            var d = ds[ds.Count - 1];
            ds.RemoveAt(ds.Count - 1);
            var savedPanic = t.curPanic;
            var savedTarget = t.deferTarget;
            t.curPanic = panicking;
            t.deferTarget = d.fid;
            try
            {
                if (d.f == null) throw Panics.nilDeref();
                if (d.start) await awaitFrame(t, (Frame)d.f.Call(d.args));
                else d.f.Call(d.args);
            }
            catch (GoPanic np)
            {
                if (np != panicking && np.prev == null) np.prev = panicking;
                panicking = np;
                continue;
            }
            finally
            {
                t.curPanic = savedPanic;
                t.deferTarget = savedTarget;
            }
            if (panicking != null && panicking.recovered) panicking = null;
        }
        if (panicking != null) throw panicking;
    }
}
