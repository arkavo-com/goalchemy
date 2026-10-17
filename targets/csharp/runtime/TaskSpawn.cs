namespace Rt;

/// <summary>A resumable activation of a suspending function: its locals live
/// in subclass fields and pc is the block to resume at.</summary>
public abstract class Frame
{
    public int pc;
    public List<Deferred> defers = new();
    public Frame parent;
    internal int depth;
    public GoPanic panicking;
    internal Action<GoTask, object[], GoPanic> completion;

    public abstract void step(GoTask t);

    public virtual object[] results() => Array.Empty<object>();
}

public sealed class GoTask : PanicState
{
    internal readonly int id;
    internal Scheduler owner;
    public Frame frame;
    public object[] rv = Array.Empty<object>();
    public bool blocked;
    public bool done;
    public GoPanic resumePanic;
    public Action cleanup;

    internal GoTask(int id, Frame frame)
    {
        this.id = id;
        this.frame = frame;
    }
}

/// <summary>Thrown out of a harness case when its task blocks with nothing runnable.</summary>
public sealed class Blocked : Exception
{
    public Blocked() : base("blocked") { }
}

sealed class FatalPanic : Exception
{
    internal readonly GoPanic p;

    internal FatalPanic(GoPanic p) : base("fatal panic") => this.p = p;
}

public sealed class Scheduler
{
    internal readonly List<(object key, GoPanic panic)> unawaited = new();
    internal void checkFuturePanics() { if (unawaited.Count > 0) throw new FatalPanic(unawaited[0].panic); }
    internal sealed record Timer(long at, long seq, GoTask task, Action fn);
    internal readonly Queue<GoTask> runq = new();
    public GoTask cur;
    internal GoTask main;
    internal readonly HashSet<GoTask> tasks = new();
    public readonly HashSet<Action> disposers = new();
    internal readonly Mailbox mail = new();
    internal readonly Dictionary<long, HostOperation> operations = new();
    long nextOperation;
    internal bool closed, hostMode, harness;
    internal Action libraryObserve;
    internal System.Threading.Thread driver = System.Threading.Thread.CurrentThread;
    readonly Func<long> monotonic;
    readonly long epoch, frequency;
    internal int nextId = 1;
    long rng;
    public long clock;
    internal readonly List<Timer> timers = new();
    long seq;
    /// <summary>The native frame that runs next: set when a task body must
    /// take the turn, run by the driver's trampoline (turnBack).</summary>
    internal NativeFrame handoff;
    Exception fault;

    internal Scheduler(GoTask main) : this(main, System.Diagnostics.Stopwatch.GetTimestamp, System.Diagnostics.Stopwatch.Frequency) { }
    internal Scheduler(GoTask main, Func<long> monotonic, long frequency)
    {
        if (frequency <= 0) throw new HostFault("invalid monotonic frequency");
        cur = this.main = main; rng = R.seed();
        this.monotonic = monotonic; this.frequency = frequency; epoch = monotonic();
        main.owner = this; tasks.Add(main);
    }
    public int choose(int n)
    {
        assertDriver();
        long x = rng;
        x ^= (x << 13) & 0xFFFFFFFFL;
        x ^= (long)((ulong)x >> 17);
        x ^= (x << 5) & 0xFFFFFFFFL;
        rng = x;
        return (int)(x % n);
    }
    public void assertDriver()
    {
        if (System.Threading.Thread.CurrentThread != driver) throw new HostFault("source state requires owner driver");
    }
    public void assertTask(GoTask t)
    {
        assertDriver(); if (!live(t)) throw new HostFault("foreign or retired source task");
    }
    public bool live(GoTask t) => !closed && t.owner == this && !t.done;
    public void ready(GoTask t)
    {
        assertDriver(); if (live(t)) { tasks.Add(t); runq.Enqueue(t); }
    }
    public void block(GoTask t) { assertDriver(); t.blocked = true; }
    public long now()
    {
        assertDriver();
        if (hostMode)
        {
            long ticks = unchecked(monotonic() - epoch);
            if (ticks > 0)
            {
                // One division, no double rounding or overflowing ticks * 1e9.
                UInt128 ns = (UInt128)(ulong)ticks * 1000000000UL / (ulong)frequency;
                long elapsed = ns > (UInt128)long.MaxValue ? long.MaxValue : (long)ns;
                if (elapsed > clock) clock = elapsed;
            }
        }
        return clock;
    }
    public Action addTimer(long d, GoTask task, Action fn) => addTimerAt(R.deadlineAfter(now(), d), task, fn);
    public Action addTimerAt(long at, GoTask task, Action fn)
    {
        assertDriver(); var timer = new Timer(Math.Max(0, at), ++seq, task, fn); timers.Add(timer);
        return () => { assertDriver(); timers.Remove(timer); };
    }
    public HostToken registerHost(GoTask t, Action cancel, Action cleanup = null,
        Func<object[]> canceled = null, Func<object[], object[]> decode = null)
    {
        assertDriver();
        if (!hostMode || !live(t)) throw new HostFault("host registration requires a live owner task");
        long id = ++nextOperation;
        operations.Add(id, new HostOperation(t, cancel, cleanup ?? (() => {}), canceled ?? (() => null), decode ?? (a => a)));
        tasks.Add(t); lock (mail) mail.live.Add(id, t.id);
        block(t); return new HostToken(mail, id, t.id);
    }
    /// Work must settle AFTER resource release; Task cancellation is not cleanup.
    public void launchHost(HostToken token, Func<System.Threading.Tasks.Task<object[]>> work)
    {
        assertDriver();
        try { _ = settle(token, work() ?? throw new HostFault("null adapter Task")); }
        catch (Exception e) { token.complete(Array.Empty<object>(), new HostFault("adapter submission: " + e)); token.acknowledgeCleanup(); }
    }
    static async System.Threading.Tasks.Task settle(HostToken token, System.Threading.Tasks.Task<object[]> work)
    {
        try { token.complete(await work.ConfigureAwait(false)); }
        catch (Exception e) { token.complete(Array.Empty<object>(), new HostFault("unexpected Task exception: " + e)); }
        finally { token.acknowledgeCleanup(); }
    }
    internal void drainHost()
    {
        assertDriver(); var applicable = new List<KeyValuePair<long, Completion>>();
        lock (mail)
        {
            foreach (var c in mail.records) if (mail.cleaned.Contains(c.Key)) applicable.Add(c);
            foreach (var c in applicable) mail.records.Remove(c.Key);
        }
        applicable.Sort((a, b) => a.Value.sequence.CompareTo(b.Value.sequence));
        foreach (var record in applicable)
        {
            if (!operations.Remove(record.Key, out var op)) continue;
            lock (mail) { mail.live.Remove(record.Key); mail.cleaned.Remove(record.Key); }
            if (op.task.id != record.Value.task) continue;
            Exception cleanupFault = null;
            try { op.cleanup(); } catch (Exception e) { cleanupFault = e; }
            if (record.Value.fault != null) throw record.Value.fault;
            if (cleanupFault != null) throw new HostFault("operation cleanup: " + cleanupFault);
            try
            {
                op.task.rv = op.canceled() ?? op.decode(record.Value.values);
                if (op.task.rv == null) throw new HostFault("null source result vector");
            }
            catch (Exception e) { throw new HostFault("driver decode/cancellation bridge: " + e); }
            ready(op.task);
        }
    }
    internal GoTask nextHost()
    {
        for (;;)
        {
            long version = mail.versionNow();
            libraryObserve?.Invoke();
            fireDue(now()); drainHost();
            if (runq.Count > 0) return runq.Dequeue();
            if (operations.Count == 0 && timers.Count == 0) { checkFuturePanics(); throw new HostFatal("all goroutines are asleep - deadlock!"); }
            long at = long.MaxValue; foreach (var t in timers) at = Math.Min(at, t.at);
            mail.awaitChange(version, timers.Count == 0 ? -1 : Math.Max(0, at - now()));
        }
    }
    internal void retireIdle() { driver = System.Threading.Thread.CurrentThread; shutdown(); }
    public void shutdown()
    {
        assertDriver(); if (closed) return;
        closed = true; lock (mail) mail.retiring = true;
        Exception failure = null;
        foreach (var op in operations.Values) try { op.cancel?.Invoke(); } catch (Exception e) { failure ??= e; }
        for (;;)
        {
            long version = mail.versionNow(); bool pending;
            lock (mail) { pending = false; foreach (long id in operations.Keys) if (!mail.cleaned.Contains(id)) { pending = true; break; } }
            if (!pending) break;
            mail.awaitChange(version, -1);
        }
        foreach (var op in operations.Values) try { op.cleanup(); } catch (Exception e) { failure ??= e; }
        operations.Clear();
        lock (mail) { mail.closed = true; mail.records.Clear(); mail.live.Clear(); mail.cleaned.Clear(); }
        runq.Clear(); timers.Clear();
        foreach (var dispose in new List<Action>(disposers)) try { dispose(); } catch (Exception e) { failure ??= e; }
        disposers.Clear();
        foreach (var t in tasks)
        {
            try { t.cleanup?.Invoke(); } catch (Exception e) { failure ??= e; }
            t.cleanup = null; t.frame = null; t.rv = Array.Empty<object>(); t.done = true; t.blocked = false;
            t.curPanic = null; t.resumePanic = null; t.deferTarget = null;
        }
        tasks.Clear(); cur = null; main = null; libraryObserve=null; handoff = null;
        if (failure != null) throw new HostFault("owner cleanup: " + failure);
    }
    internal GoTask next()
    {
        while (runq.Count == 0)
        {
            if (timers.Count == 0) { checkFuturePanics(); if (harness) throw new Blocked(); throw new HostFatal("all goroutines are asleep - deadlock!"); }
            long at = long.MaxValue; foreach (var t in timers) at = Math.Min(at, t.at);
            clock = at; fireDue(at);
        }
        return runq.Dequeue();
    }
    internal void fireDue(long at)
    {
        var due = timers.FindAll(t => t.at <= at); timers.RemoveAll(t => t.at <= at);
        due.Sort((a,b) => { int c = a.at.CompareTo(b.at); return c != 0 ? c : a.seq.CompareTo(b.seq); });
        foreach (var t in due) { t.fn?.Invoke(); if (t.task != null) ready(t.task); }
    }

    internal void run(GoTask t)
    {
        assertDriver();
        enter(t);
        while (!t.blocked && t.frame != null)
        {
            var p = t.resumePanic;
            if (p != null && t.frame is not NativeFrame)
            {
                t.resumePanic = null;
                exit(t, t.frame, p);
                continue;
            }
            var f = t.frame;
            try
            {
                f.step(t);
            }
            catch (GoPanic e)
            {
                exit(t, f, e);
            }
            if (handoff != null) return;
        }
    }

    void enter(GoTask t)
    {
        cur = t;
        t.blocked = false;
        if (t.cleanup != null)
        {
            var c = t.cleanup;
            t.cleanup = null;
            c();
        }
    }

    /// <summary>Runs task bodies the turn was handed to until it comes back
    /// to the driver: nothing is runnable, main is done, or the scheduler
    /// failed on a body's side, which is raised here.</summary>
    internal void turnBack()
    {
        while (handoff != null)
        {
            var n = handoff;
            handoff = null;
            n.resume();
            if (fault != null)
            {
                var e = fault;
                fault = null;
                System.Runtime.ExceptionServices.ExceptionDispatchInfo.Throw(e);
            }
        }
    }

    /// <summary>Runs the scheduler for a native task body that blocked, or
    /// finished (self null), while holding the turn: frame tasks run inline
    /// and the next native body is handed the turn. Reports true when self
    /// is next. Host drivers take every turn back. Scheduler failures never
    /// unwind a task body: the driver raises them.</summary>
    internal bool turn(GoTask self)
    {
        if (hostMode) return false;
        try
        {
            while (!main.done && runq.Count > 0)
            {
                var u = runq.Dequeue();
                if (u == self)
                {
                    enter(u);
                    return true;
                }
                run(u);
                if (handoff != null) return false;
            }
        }
        catch (Exception e) { fault = e; }
        return false;
    }

    /// <summary>Delivers a finished native body like a frame return, then
    /// continues the task's remaining frames and the scheduler.</summary>
    internal void nativeDone(GoTask t, NativeFrame n, Exception failure)
    {
        try
        {
            exit(t, n, failure == null ? null : Program.catchPanic(failure));
            if (t.frame != null)
            {
                run(t);
                if (handoff != null) return;
            }
        }
        catch (Exception e)
        {
            fault = e;
            return;
        }
        turn(null);
    }

    internal void exit(GoTask t, Frame f, GoPanic p)
    {
        if (p != null)
        {
            if (p.prev == null && f.panicking != null && f.panicking != p) p.prev = f.panicking;
            f.panicking = p;
        }
        t.frame = f;
        if (f.defers.Count > 0)
        {
            t.frame = new DeferRunner(f) { parent = f };
            return;
        }
        finish(t, f);
    }

    internal void finish(GoTask t, Frame f)
    {
        var p = f.panicking;
        var parent = f.parent;
        t.frame = parent;
        if (parent == null)
        {
            t.done = true;
            t.rv = Array.Empty<object>(); t.curPanic = null; t.deferTarget = null;
            if (t != main) tasks.Remove(t);
            if (f.completion != null) { f.completion(t, p == null ? f.results() : Array.Empty<object>(), p); return; }
            if (p != null)
            {
                throw new FatalPanic(p);
            }
            return;
        }
        if (parent is DeferRunner r && r.child == f)
        {
            r.childDone(t, p);
            return;
        }
        if (p != null)
        {
            exit(t, parent, p);
            return;
        }
        t.rv = f.results();
    }
}

sealed class DeferRunner : Frame
{
    readonly Frame target;
    internal Frame child;
    GoPanic savedPanic;
    object savedTarget;

    internal DeferRunner(Frame target) => this.target = target;

    public override void step(GoTask t)
    {
        var tf = target;
        while (tf.defers.Count > 0)
        {
            var d = tf.defers[tf.defers.Count - 1];
            tf.defers.RemoveAt(tf.defers.Count - 1);
            savedPanic = t.curPanic;
            savedTarget = t.deferTarget;
            t.curPanic = tf.panicking;
            t.deferTarget = d.fid;
            if (d.start)
            {
                Frame c;
                try
                {
                    if (d.f == null) throw Panics.nilDeref();
                    c = (Frame)d.f.Call(d.args);
                }
                catch (GoPanic e)
                {
                    t.curPanic = savedPanic;
                    t.deferTarget = savedTarget;
                    after(tf, e);
                    continue;
                }
                child = c;
                c.parent = this;
                t.frame = c;
                return;
            }
            GoPanic p = null;
            try
            {
                if (d.f == null) throw Panics.nilDeref();
                d.f.Call(d.args);
            }
            catch (GoPanic e)
            {
                p = e;
            }
            t.curPanic = savedPanic;
            t.deferTarget = savedTarget;
            after(tf, p);
        }
        t.frame = tf;
        R.sched.finish(t, tf);
    }

    internal void childDone(GoTask t, GoPanic p)
    {
        t.curPanic = savedPanic;
        t.deferTarget = savedTarget;
        child = null;
        t.frame = this;
        after(target, p);
    }

    void after(Frame tf, GoPanic p)
    {
        if (p != null)
        {
            if (p.prev == null && tf.panicking != null && tf.panicking != p) p.prev = tf.panicking;
            tf.panicking = p;
            return;
        }
        if (tf.panicking != null && tf.panicking.recovered) tf.panicking = null;
    }
}

sealed class SyncFrame : Frame
{
    internal readonly Func<object[]> fn;
    object[] res = Array.Empty<object>();

    internal SyncFrame(Func<object[]> fn) => this.fn = fn;

    public override void step(GoTask t)
    {
        res = fn();
        R.ret(t, this);
    }

    public override object[] results() => res;
}

sealed class AwaitFrame : Frame
{
    internal readonly Action<GoTask> fn;
    internal object[] res = Array.Empty<object>();

    internal AwaitFrame(Action<GoTask> fn) => this.fn = fn;

    public override void step(GoTask t)
    {
        if (pc == 0)
        {
            pc = 1;
            fn(t);
            return;
        }
        res = t.rv;
        R.ret(t, this);
    }

    public override object[] results() => res;
}

/// <summary>core.task.spawn and the cooperative scheduler. Suspending
/// functions are compiled to resumable frames: a frame holds the function's
/// locals and the block to resume at, and step runs it until it returns or
/// reaches a pause point. A task is a stack of frames driven by a trampoline;
/// exactly one task runs at a time and runnable tasks are dispatched in FIFO
/// order. Pause primitives either complete immediately, leaving their results
/// in task.rv, or block the task until another task or a timer readies it.
/// Under the native pause model a task's frame is a NativeFrame running an
/// async body (types/Async.cs); the same scheduler picks the next task.</summary>
public static partial class R
{
    internal static long seed()
    {
        var s = Environment.GetEnvironmentVariable("GOALCHEMY_SEED");
        if (s == null || !long.TryParse(s, out var v)) return 1;
        return v > 0 && v < (1L << 32) ? v : 1;
    }

    public static void fatal(string msg)
    {
        throw new HostFatal(msg);
    }

    public static Scheduler sched = new Scheduler(new GoTask(0, null));

    public static void call(GoTask t, Frame child)
    {
        sched.assertTask(t);
        child.depth = t.frame.depth + 1;
        if (child.depth >= 4096) throw new SourceStackFatal();
        child.parent = t.frame;
        t.frame = child;
    }

    public static void ret(GoTask t, Frame f) { sched.assertTask(t); sched.exit(t, f, null); }

    /// <summary>Runs an ordinary call as a frame.</summary>
    public static Frame sync(Func<object[]> fn) => new SyncFrame(fn);

    /// <summary>Adapts an ordinary function value with n results to the resumable form.</summary>
    public static Fn adapt(Fn f, int n)
    {
        if (f == null) return null;
        return new Fn(a => sync(() =>
        {
            var r = f.F(a);
            return n == 0 ? Array.Empty<object>() : n == 1 ? new[] { r } : (object[])r;
        }), f.Fid);
    }

    public static Slice adaptSlice(Slice s, int n)
    {
        if (s.a == null) return s;
        var a = new object[s.l];
        for (int i = 0; i < s.l; i++) a[i] = adapt((Fn)s.Get(i), n);
        return new Slice(a, 0, a.Length, a.Length);
    }

    /// <summary>go f(args): starts a task running frame f.</summary>
    public static void spawn(Frame f) => sched.ready(new GoTask(sched.nextId++, f) { owner = sched });

    static int entryActive;
    static void reserveEntry()
    {
        if (System.Threading.Interlocked.CompareExchange(ref entryActive, 1, 0) != 0)
            throw new HostFault("overlapping executable entry/reset");
    }
    static void releaseEntry() => System.Threading.Volatile.Write(ref entryActive, 0);
    static Scheduler install(GoTask main, bool harness)
    {
        if (!sched.closed)
        {
            sched.retireIdle();
        }
        var s = new Scheduler(main) { harness = harness };
        sched = s; Program.panicState = () => s.cur;
        return s;
    }
    public static long deadlineAfter(long now, long duration) => duration <= 0 ? now : duration > long.MaxValue - now ? long.MaxValue : now + duration;

    /// Explicit executable entry: dedicated driver, serialized static source ABI.
    /// Init reruns against persistent globals. This is not an isolated library.
    public static System.Threading.Tasks.Task runMainHost(Func<Frame> entry)
    {
        reserveEntry();
        try
        {
            var done = new System.Threading.Tasks.TaskCompletionSource(System.Threading.Tasks.TaskCreationOptions.RunContinuationsAsynchronously);
            var thread = new System.Threading.Thread(() =>
            {
                try { drive(entry, true); done.SetResult(); }
                catch (Exception e) { done.SetException(e); }
            }, 16 << 20);
            thread.Start(); return done.Task;
        }
        catch { releaseEntry(); throw; }
    }
    public static System.Threading.Tasks.Task runMainHost(Frame entry) => runMainHost(() => entry);
    public static void runMain(Frame entry)
    {
        reserveEntry(); Program.runLarge(() => drive(() => entry, false)); Environment.Exit(0);
    }
    static void drive(Func<Frame> entry, bool host)
    {
        Scheduler s = null; Exception failure = null;
        try
        {
            var main = new GoTask(0, null); s = install(main, false); s.hostMode = host;
            main.frame = entry(); s.ready(main);
            while (!main.done)
            {
                s.run(host ? s.nextHost() : s.next());
                if (s.handoff != null) s.turnBack();
            }
            s.checkFuturePanics();
        }
        catch (Exception e) { failure = e; }
        if (s != null) try { s.shutdown(); } catch (Exception e) { failure ??= e; }
        Program.resetPanicBinding(); releaseEntry();
        if (failure is FatalPanic p) Program.reportPanic(p.p);
        if (failure is HostFatal f) { Out.stderr("fatal error: " + f.Message + "\n"); Environment.Exit(2); }
        if (failure is SourceStackFatal) { Out.stderr("runtime: goroutine stack exceeds limit\nfatal error: stack overflow\n"); Environment.Exit(2); }
        if (failure != null) System.Runtime.ExceptionServices.ExceptionDispatchInfo.Capture(failure).Throw();
    }

    public static Frame nativeFrame(Action<GoTask> primitive)=>new AwaitFrame(primitive);
    public static T driveLibrary<T>(Func<Frame> entry, Func<object[],T> capture, Action observe, Action<Action> wake, Action retire)
    {
        reserveEntry();Scheduler owner=null;Frame root=null;T value=default;Exception failure=null;
        try
        {
            var main=new GoTask(0,null);owner=install(main,false);owner.hostMode=true;owner.libraryObserve=observe;
            var mail=owner.mail;wake(() => {lock(mail)mail.signal();});root=entry();main.frame=root;owner.ready(main);
            while(!main.done){observe();owner.run(owner.nextHost());if(owner.handoff!=null)owner.turnBack();}
            owner.checkFuturePanics(); value=capture(root.results());
        }
        catch(FatalPanic){failure=new Library.Failure("panic");}
        catch(HostFatal e){failure=new Library.Failure("fatal",null,e);}
        catch(SourceStackFatal e){failure=new Library.Failure("fatal",null,e);}
        catch(Exception e){failure=e;}
        finally
        {
            if(owner!=null)try{owner.shutdown();}catch(Exception e){failure=new Library.Failure("host_fault",null,e);}
            try{retire();}catch(Exception e){failure=new Library.Failure("host_fault",null,e);}
            root=null;try{Program.resetPanicBinding();}catch(Exception e){failure=new Library.Failure("host_fault",null,e);}
            releaseEntry();
        }
        if(failure!=null)System.Runtime.ExceptionServices.ExceptionDispatchInfo.Capture(failure).Throw();return value;
    }

    /// <summary>Requeues the running task: a pause primitive.</summary>
    public static void yieldTask(GoTask t)
    {
        sched.ready(t);
        sched.block(t);
    }

    /// <summary>Runs one pause primitive in an isolated scheduler for a harness
    /// case; throws Blocked when no task can run, and the source panic on panic.</summary>
    public static object[] runIsolated(Action<GoTask> fn)
    {
        reserveEntry();
        try
        {
        var h = new AwaitFrame(fn);
        var main = new GoTask(0, h);
        var s = install(main, true);
        s.ready(main);
        try
        {
            while (!main.done) s.run(s.next());
        }
        catch (FatalPanic e)
        {
            throw e.p;
        }
        return h.res;
        } finally { releaseEntry(); }
    }

    public static void resetScheduler()
    {
        reserveEntry(); try { install(new GoTask(0, null), true); } finally { releaseEntry(); }
    }
}


/// Adapter implementation faults bypass source panic/recover.
internal sealed record Completion(long sequence, int task, object[] values, HostFault fault);
internal sealed record HostOperation(GoTask task, Action cancel, Action cleanup,
    Func<object[]> canceled, Func<object[], object[]> decode);

/// Only this state may be mutated by native threads. No source roots live here.
internal sealed class Mailbox
{
    internal readonly Dictionary<long, int> live = new();
    internal readonly HashSet<long> cleaned = new();
    internal long publicationSequence;
    internal readonly Dictionary<long, Completion> records = new();
    internal bool retiring, closed, waiting;
    long version;
    internal void signal() { version++; System.Threading.Monitor.PulseAll(this); }
    internal long versionNow() { lock (this) return version; }
    internal void awaitChange(long observed, long nanos)
    {
        lock (this)
        {
            if (version != observed || nanos == 0) return;
            waiting = true;
            try
            {
                if (nanos < 0) System.Threading.Monitor.Wait(this);
                else System.Threading.Monitor.Wait(this, (int)Math.Min(int.MaxValue, (nanos - 1) / 1000000 + 1));
            }
            catch (System.Threading.ThreadInterruptedException) { /* Still await real resource cleanup. */ }
            finally { waiting = false; }
        }
    }
}
/// Retains mailbox identity and numeric IDs only, never frames or input snapshots.
public sealed class HostToken
{
    readonly Mailbox mail;
    public readonly long operation;
    public readonly int task;
    internal HostToken(Mailbox mail, long operation, int task) { this.mail=mail; this.operation=operation; this.task=task; }
    public void complete(object[] values, HostFault fault = null)
    {
        lock (mail)
        {
            if (mail.closed || mail.retiring || (!mail.live.TryGetValue(operation, out int expectedTask) || expectedTask != task) || mail.records.ContainsKey(operation)) return;
            object[] owned = Array.Empty<object>();
            HostFault failure = fault == null ? null : new HostFault(fault.Message);
            try { owned = (object[])R.snapshot(values); if (owned == null) throw new HostFault("null wire vector"); }
            catch (Exception e) { failure = new HostFault("invalid adapter wire result: " + e); }
            mail.records.Add(operation, new Completion(++mail.publicationSequence, task, owned, failure)); mail.signal();
        }
    }
    /// Only after worker/Task transport, body, key and input cleanup.
    public void acknowledgeCleanup()
    {
        lock (mail)
        {
            if (mail.closed || (!mail.live.TryGetValue(operation, out int expectedTask) || expectedTask != task)) return;
            mail.cleaned.Add(operation); mail.signal();
        }
    }
}
public static partial class R
{
    /// Restricted wire values; source descriptors and opaque handles decode on driver.
    public static object snapshot(object value) => snapshot(value, new HashSet<object>(System.Collections.Generic.ReferenceEqualityComparer.Instance));
    static object snapshot(object value, HashSet<object> path)
    {
        if (value == null || value is string || value is bool || value is byte || value is sbyte || value is short || value is ushort || value is int || value is uint || value is long || value is ulong || value is float || value is double) return value;
        if (value is byte[] bytes) return bytes.Clone();
        if (!path.Add(value)) throw new HostFault("cyclic wire value");
        try
        {
            if (value is object[] a)
            {
                var copy = new object[a.Length]; for (int i=0;i<a.Length;i++) copy[i]=snapshot(a[i],path); return copy;
            }
            if (value is System.Collections.Generic.IDictionary<string,object> map)
            {
                var copy = new Dictionary<string,object>(); foreach (var pair in map) copy.Add(pair.Key,snapshot(pair.Value,path));
                return new System.Collections.ObjectModel.ReadOnlyDictionary<string,object>(copy);
            }
            if (value is System.Collections.Generic.IList<object> list)
            {
                var copy = new List<object>(); foreach (var item in list) copy.Add(snapshot(item,path)); return copy.AsReadOnly();
            }
            throw new HostFault("unsupported wire value " + value.GetType().FullName);
        }
        finally { path.Remove(value); }
    }
}
