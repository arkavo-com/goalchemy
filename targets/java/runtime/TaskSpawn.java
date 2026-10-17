package rt;

import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.function.Supplier;
import java.util.function.Function;
import java.util.function.LongSupplier;
import java.util.LinkedHashMap;
import java.util.LinkedHashSet;
import java.util.Map;
import java.util.Set;
import java.util.IdentityHashMap;
import java.util.concurrent.CompletionStage;
import java.util.concurrent.locks.LockSupport;

/** core.task.spawn and the cooperative scheduler. Suspending functions are
 * compiled to resumable frames: a frame holds the function's locals and the
 * block to resume at, and step runs it until it returns or reaches a pause
 * point. A task is a stack of frames driven by a trampoline; exactly one
 * task runs at a time and runnable tasks are dispatched in FIFO order. Pause
 * primitives either complete immediately, leaving their results in task.rv,
 * or block the task until another task or a timer readies it. Deferred
 * calls, panics, and recover are managed per task by the runtime.
 *
 * Under the native pause model a task body is an ordinary method taking the
 * task, run on its own virtual thread by a NativeFrame; exactly one thread
 * holds the turn. A task that blocks runs the scheduler itself (pass) and
 * hands the turn straight to the next task's thread. The driver takes the
 * turn back when nothing is runnable, when main is done, on a scheduler
 * failure (raised by the driver, never through a task body), and after every
 * turn in host mode. */
public final class TaskSpawn {
    private TaskSpawn() {}

    public abstract static class Frame {
        public int pc;
        public ArrayList<Program.Deferred> defers = new ArrayList<>();
        public Frame parent;
        public GoPanic panicking;

        public abstract void step(Task t);

        public Object[] results() {
            return new Object[0];
        }
    }

    public static final class Task extends Program.PanicState {
        final int id;
        Scheduler owner;
        public Frame frame;
        public Object[] rv = new Object[0];
        public boolean blocked;
        public boolean done;
        public GoPanic resumePanic;
        public Runnable cleanup;

        Task(int id, Frame frame) {
            this.id = id;
            this.frame = frame;
        }
    }

    /** Thrown out of a harness case when its task blocks with nothing runnable. */
    public static final class Blocked extends RuntimeException {
        Blocked() {
            super("blocked", null, false, false);
        }
    }

    static final class FatalPanic extends RuntimeException {
        final GoPanic p;

        FatalPanic(GoPanic p) {
            super("fatal panic", null, false, false);
            this.p = p;
        }
    }

    record Timer(long at, long seq, Task task, Runnable fn) {}

    /** Adapter implementation faults bypass source panic/recover. */
    public static final class HostFault extends RuntimeException {
        public HostFault(String message) { super(message); }
    }
    public static final class HostFatal extends RuntimeException {
        HostFatal(String message) { super(message, null, false, false); }
    }
    record Completion(int task, Object[] values, HostFault fault) {}
    /** This is the only state native threads may mutate. No source roots live here. */
    static final class Mailbox {
        final Map<Long, Integer> live = new LinkedHashMap<>();
        final Set<Long> cleaned = new LinkedHashSet<>();
        final LinkedHashMap<Long, Completion> records = new LinkedHashMap<>();
        boolean retiring, closed;
        long version;
        synchronized void signal() { version++; notifyAll(); }
        synchronized long version() { return version; }
        synchronized void await(long observed, long nanos) {
            if (version != observed) return;
            try {
                if (nanos < 0) wait();
                else if (nanos > 0) wait(nanos / 1000000, (int) (nanos % 1000000));
            } catch (InterruptedException e) {
                // Retirement still has to await real cleanup. Preserve the interrupt
                // at entry return, without allowing it to turn the wait into a spin.
                interrupted = true;
            }
        }
        boolean interrupted;
    }
    /** Retains mailbox identity and IDs only, never owner/frame/input references. */
    public static final class HostToken {
        private final Mailbox mail;
        public final long operation;
        public final int task;
        HostToken(Mailbox mail, long operation, int task) { this.mail=mail; this.operation=operation; this.task=task; }
        public void complete(Object[] values) { complete(values, null); }
        public void complete(Object[] values, HostFault fault) {
            synchronized (mail) {
                if (mail.closed || mail.retiring || !Integer.valueOf(task).equals(mail.live.get(operation)) || mail.records.containsKey(operation)) return;
                Object[] owned = new Object[0];
                HostFault failure = fault == null ? null : new HostFault(fault.getMessage());
                try { owned = (Object[]) snapshot(values); }
                catch (Throwable e) { failure = new HostFault("invalid adapter wire result: " + e); }
                mail.records.put(operation, new Completion(task, owned, failure));
                mail.signal();
            }
        }
        /** Only after transport/body/key/input cleanup; Future.cancel is not an ACK. */
        public void acknowledgeCleanup() {
            synchronized (mail) {
                if (mail.closed || !Integer.valueOf(task).equals(mail.live.get(operation))) return;
                mail.cleaned.add(operation); mail.signal();
            }
        }
    }
    /** Restricted wire data; source descriptors/closures/native handles are decoded
     * on the driver via registerHost's decoder, never cloned across threads. */
    public static Object snapshot(Object value) { return snapshot(value, new IdentityHashMap<>()); }
    private static Object snapshot(Object value, IdentityHashMap<Object, Boolean> path) {
        if (value == null || value instanceof String || value instanceof Boolean || value instanceof Long || value instanceof Integer || value instanceof Double || value instanceof Float || value instanceof Short || value instanceof Byte) return value;
        if (value instanceof byte[] b) return b.clone();
        if (value instanceof String[] a) return a.clone();
        if (path.put(value, true) != null) throw new HostFault("cyclic wire value");
        try {
            if (value instanceof Object[] a) {
                Object[] copy = new Object[a.length];
                for (int i=0;i<a.length;i++) copy[i]=snapshot(a[i], path);
                return copy;
            }
            if (value instanceof java.util.List<?> a) {
                ArrayList<Object> copy = new ArrayList<>();
                for (Object item:a) copy.add(snapshot(item,path));
                return java.util.Collections.unmodifiableList(copy);
            }
            if (value instanceof Map<?,?> a) {
                LinkedHashMap<String,Object> copy=new LinkedHashMap<>();
                for (var entry:a.entrySet()) {
                    if (!(entry.getKey() instanceof String key)) throw new HostFault("wire map keys must be strings");
                    copy.put(key,snapshot(entry.getValue(),path));
                }
                return java.util.Collections.unmodifiableMap(copy);
            }
            throw new HostFault("unsupported wire value " + value.getClass().getName());
        } finally { path.remove(value); }
    }
    record HostOperation(Task task, Runnable cancel, Runnable cleanup,
                         Supplier<Object[]> canceled, Function<Object[],Object[]> decode) {}
    public static long deadlineAfter(long now, long duration) {
        return duration <= 0 ? now : duration > Long.MAX_VALUE-now ? Long.MAX_VALUE : now+duration;
    }

    public static final class Scheduler {
        final ArrayDeque<Task> runq = new ArrayDeque<>();
        public Task cur;
        Task main;
        final Set<Task> tasks = new LinkedHashSet<>();
        public final Set<Runnable> disposers = new LinkedHashSet<>();
        final Mailbox mail = new Mailbox();
        final LinkedHashMap<Long, HostOperation> operations = new LinkedHashMap<>();
        long nextOperation;
        boolean closed, hostMode;
        Runnable libraryControl=()->{};
        Thread driver = Thread.currentThread();
        final LongSupplier monotonic;
        final long epoch;
        int nextId = 1;
        long rng;
        public long clock;
        final ArrayList<Timer> timers = new ArrayList<>();
        long seq;
        boolean harness;
        /** The native frame that takes the turn next, set by its step. */
        NativeFrame handoff;
        Thread home;
        volatile boolean homeTurn;
        Throwable fault;

        Scheduler(Task main) { this(main, System::nanoTime); }

        Scheduler(Task main, LongSupplier monotonic) {
            this.cur = main;
            this.main = main;
            this.rng = seed();
            this.monotonic=monotonic; this.epoch=monotonic.getAsLong();
            main.owner=this; tasks.add(main);
        }

        /** xorshift32 choice source, identical on every target. */
        public int choose(int n) {
            long x = rng;
            x ^= (x << 13) & 0xFFFFFFFFL;
            x ^= x >>> 17;
            x ^= (x << 5) & 0xFFFFFFFFL;
            rng = x;
            return (int) (x % n);
        }

        public void assertDriver() {
            if (Thread.currentThread() != driver) throw new HostFault("source state requires owner driver");
        }
        public boolean live(Task t) { return !closed && t.owner == this && !t.done; }
        public void ready(Task t) {
            assertDriver();
            if (live(t)) { tasks.add(t); runq.add(t); }
        }
        public void block(Task t) { assertDriver(); t.blocked = true; }
        public long now() {
            assertDriver();
            if (hostMode) {
                long elapsed=monotonic.getAsLong()-epoch;
                if (elapsed > clock) clock=elapsed;
            }
            return clock;
        }
        public Runnable addTimer(long d, Task task, Runnable fn) {
            return addTimerAt(deadlineAfter(now(), d), task, fn);
        }
        public Runnable addTimerAt(long at, Task task, Runnable fn) {
            assertDriver();
            Timer timer=new Timer(Math.max(0,at), ++seq, task, fn);
            timers.add(timer);
            return () -> { assertDriver(); timers.remove(timer); };
        }
        public HostToken registerHost(Task t, Runnable cancel, Runnable cleanup, Supplier<Object[]> canceled) {
            return registerHost(t,cancel,cleanup,canceled,Function.identity());
        }
        public HostToken registerHost(Task t, Runnable cancel, Runnable cleanup, Supplier<Object[]> canceled, Function<Object[],Object[]> decode) {
            assertDriver();
            if (!hostMode || !live(t)) throw new HostFault("host registration requires a live owner task");
            long id=++nextOperation;
            operations.put(id,new HostOperation(t,cancel,cleanup,canceled,decode));
            tasks.add(t);
            synchronized(mail) { mail.live.put(id,t.id); }
            block(t);
            return new HostToken(mail,id,t.id);
        }
        public HostToken registerHost(Task t, Runnable cancel) {
            return registerHost(t,cancel,()->{},()->null);
        }
        /** Work stages must settle only AFTER their own resource cleanup. A
         * cancellation-completed transport future alone does not satisfy this API. */
        public void launchHost(HostToken token, Supplier<? extends CompletionStage<Object[]>> work) {
            assertDriver();
            try {
                work.get().whenComplete((rv,fault)-> {
                    token.complete(fault == null ? rv : new Object[0], fault == null ? null : new HostFault("unexpected future rejection: " + fault));
                    token.acknowledgeCleanup();
                });
            } catch (Throwable e) {
                token.complete(new Object[0],new HostFault("adapter submission: " + e)); token.acknowledgeCleanup();
            }
        }
        void drainHost() {
            assertDriver();
            ArrayList<Map.Entry<Long,Completion>> applicable=new ArrayList<>();
            synchronized(mail) {
                var it=mail.records.entrySet().iterator();
                while(it.hasNext()) {
                    var c=it.next();
                    if(mail.cleaned.contains(c.getKey())) { applicable.add(Map.entry(c.getKey(),c.getValue())); it.remove(); }
                }
            }
            for(var record:applicable) {
                long id=record.getKey(); Completion c=record.getValue();
                HostOperation op=operations.get(id);
                if(op == null || op.task.id != c.task) continue;
                operations.remove(id);
                synchronized(mail) { mail.live.remove(id); mail.cleaned.remove(id); }
                Throwable cleanupFault=null;
                try { op.cleanup.run(); } catch(Throwable e) { cleanupFault=e; }
                if(c.fault != null) throw c.fault;
                if(cleanupFault != null) throw new HostFault("operation cleanup: " + cleanupFault);
                try {
                    Object[] canceled=op.canceled.get();
                    op.task.rv=canceled != null ? canceled : op.decode.apply(c.values);
                    if(op.task.rv==null) throw new HostFault("null source result vector");
                } catch(Throwable e) { throw new HostFault("driver decode/cancellation bridge: " + e); }
                ready(op.task);
            }
        }
        Task nextHost() {
            for(;;) {
                long version=mail.version();
                libraryControl.run(); fireDue(now()); drainHost();
                if(!runq.isEmpty()) return runq.poll();
                if(operations.isEmpty() && timers.isEmpty()) throw new HostFatal("all goroutines are asleep - deadlock!");
                long at=Long.MAX_VALUE;
                for(Timer t:timers) at=Math.min(at,t.at);
                mail.await(version,timers.isEmpty() ? -1 : Math.max(0,at-now()));
            }
        }
        void driverRetireIdle() { driver=Thread.currentThread(); shutdown(); }
        public void shutdown() {
            assertDriver(); if(closed) return;
            closed=true; synchronized(mail) { mail.retiring=true; }
            for(Task t:tasks) if(t.frame instanceof NativeFrame n) n.kill(mail);
            Throwable failure=null;
            for(HostOperation op:operations.values()) try { op.cancel.run(); } catch(Throwable e) { if(failure==null) failure=e; }
            for(;;) {
                long version=mail.version(); boolean pending;
                synchronized(mail) { pending=!mail.cleaned.containsAll(operations.keySet()); }
                if(!pending) break;
                mail.await(version,-1);
            }
            for(HostOperation op:operations.values()) try { op.cleanup.run(); } catch(Throwable e) { if(failure==null) failure=e; }
            operations.clear();
            synchronized(mail) { mail.closed=true; mail.records.clear(); mail.live.clear(); mail.cleaned.clear(); }
            runq.clear(); timers.clear();
            for(Runnable dispose:new ArrayList<>(disposers)) try { dispose.run(); } catch(Throwable e) { if(failure==null) failure=e; }
            disposers.clear();
            for(Task t:tasks) {
                try { if(t.cleanup!=null) t.cleanup.run(); } catch(Throwable e) { if(failure==null) failure=e; }
                t.cleanup=null; t.frame=null; t.rv=new Object[0]; t.done=true; t.blocked=false;
                t.curPanic=null; t.resumePanic=null; t.deferTarget=null;
            }
            tasks.clear(); cur=null; main=null; handoff=null;
            if(failure!=null) throw new HostFault("owner cleanup: " + failure);
        }
        Task next() {
            while(runq.isEmpty()) {
                if(timers.isEmpty()) { if(harness) throw new Blocked(); throw new HostFatal("all goroutines are asleep - deadlock!"); }
                fireTimers();
            }
            return runq.poll();
        }
        void fireTimers() {
            long at=Long.MAX_VALUE;
            for(Timer t:timers) at=Math.min(at,t.at);
            clock=at; fireDue(at);
        }
        void fireDue(long at) {
            ArrayList<Timer> due=new ArrayList<>();
            timers.removeIf(t->{ if(t.at<=at) { due.add(t); return true; } return false; });
            due.sort((a,b)->{ int order=Long.compare(a.at,b.at); return order!=0 ? order : Long.compare(a.seq,b.seq); });
            for(Timer t:due) { if(t.fn!=null) t.fn.run(); if(t.task!=null) ready(t.task); }
        }

        void run(Task t) {
            assertDriver();
            enter(t);
            while (!t.blocked && t.frame != null) {
                GoPanic p = t.resumePanic;
                if (p != null && !(t.frame instanceof NativeFrame)) {
                    t.resumePanic = null;
                    exit(t, t.frame, p);
                    continue;
                }
                Frame f = t.frame;
                try {
                    f.step(t);
                } catch (GoPanic e) {
                    exit(t, f, e);
                }
                if (handoff != null) return;
            }
        }

        void enter(Task t) {
            cur = t;
            t.blocked = false;
            if (t.cleanup != null) {
                Runnable c = t.cleanup;
                t.cleanup = null;
                c.run();
            }
        }

        /** Runs native bodies the turn was handed to until it comes back to
         * the driver, and raises a failure that occurred on their side. */
        void turnBack() {
            home = Thread.currentThread();
            transfer();
            while (!homeTurn) {
                LockSupport.park(this);
                if (Thread.interrupted()) mail.interrupted = true;
            }
            homeTurn = false;
            Throwable e = fault;
            if (e != null) {
                fault = null;
                throw rethrow(e);
            }
        }

        /** Gives the turn to the handoff frame's thread, starting its body. */
        void transfer() {
            NativeFrame n = handoff;
            handoff = null;
            if (n.thread == null) {
                n.thread = Thread.ofVirtual().unstarted(() -> body(n.task, n));
                driver = n.thread;
                n.thread.start();
                return;
            }
            driver = n.thread;
            n.turn = true;
            LockSupport.unpark(n.thread);
        }

        void toDriver() {
            driver = home;
            homeTurn = true;
            LockSupport.unpark(home);
        }

        /** Runs the scheduler on a task's thread while it holds the turn: self
         * is blocked, or null when its body finished. Frame tasks run inline
         * and the next native body is handed the turn. Reports true when self
         * runs next. */
        boolean pass(Task self) {
            try {
                while (!main.done && !hostMode) {
                    Task u = runq.poll();
                    if (u == null) break;
                    if (u == self) {
                        enter(u);
                        return true;
                    }
                    run(u);
                    if (handoff != null) {
                        transfer();
                        return false;
                    }
                }
            } catch (Throwable e) {
                fault = e;
            }
            toDriver();
            return false;
        }

        /** A native body's thread: delivers its results or panic like a
         * frame return, then continues the task's frames and the scheduler. */
        void body(Task t, NativeFrame n) {
            Throwable failure = null;
            try {
                n.res = n.body.call(t);
            } catch (Kill k) {
                return;
            } catch (Throwable e) {
                failure = e;
            }
            try {
                if (failure != null && !(failure instanceof GoPanic)) throw failure;
                exit(t, n, (GoPanic) failure);
                if (t.frame != null) {
                    run(t);
                    if (handoff != null) {
                        transfer();
                        return;
                    }
                }
            } catch (Throwable e) {
                fault = e;
                toDriver();
                return;
            }
            pass(null);
        }

        void exit(Task t, Frame f, GoPanic p) {
            if (p != null) {
                if (p.prev == null && f.panicking != null && f.panicking != p) p.prev = f.panicking;
                f.panicking = p;
            }
            t.frame = f;
            if (!f.defers.isEmpty()) {
                DeferRunner r = new DeferRunner(f);
                r.parent = f;
                t.frame = r;
                return;
            }
            finish(t, f);
        }

        void finish(Task t, Frame f) {
            GoPanic p = f.panicking;
            Frame parent = f.parent;
            t.frame = parent;
            if (parent == null) {
                t.done = true;
                t.rv=new Object[0]; t.curPanic=null; t.deferTarget=null;
                if(t!=main) tasks.remove(t);
                if (p != null) throw new FatalPanic(p);
                return;
            }
            if (parent instanceof DeferRunner r && r.child == f) {
                r.childDone(t, p);
                return;
            }
            if (p != null) {
                exit(t, parent, p);
                return;
            }
            t.rv = f.results();
        }
    }

    static final class DeferRunner extends Frame {
        final Frame target;
        Frame child;
        GoPanic savedPanic;
        Object savedTarget;

        DeferRunner(Frame target) {
            this.target = target;
        }

        public void step(Task t) {
            Frame tf = target;
            while (!tf.defers.isEmpty()) {
                Program.Deferred d = tf.defers.remove(tf.defers.size() - 1);
                savedPanic = t.curPanic;
                savedTarget = t.deferTarget;
                t.curPanic = tf.panicking;
                t.deferTarget = d.fid;
                if (d.start) {
                    Frame c;
                    try {
                        if (d.f == null) throw Panics.nilDeref();
                        c = (Frame) d.f.call(d.args);
                    } catch (GoPanic e) {
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
                try {
                    if (d.f == null) throw Panics.nilDeref();
                    d.f.call(d.args);
                } catch (GoPanic e) {
                    p = e;
                }
                t.curPanic = savedPanic;
                t.deferTarget = savedTarget;
                after(tf, p);
            }
            t.frame = tf;
            sched.finish(t, tf);
        }

        void childDone(Task t, GoPanic p) {
            t.curPanic = savedPanic;
            t.deferTarget = savedTarget;
            child = null;
            t.frame = this;
            after(target, p);
        }

        void after(Frame tf, GoPanic p) {
            if (p != null) {
                if (p.prev == null && tf.panicking != null && tf.panicking != p) p.prev = tf.panicking;
                tf.panicking = p;
                return;
            }
            if (tf.panicking != null && tf.panicking.recovered) tf.panicking = null;
        }
    }

    static long seed() {
        try {
            String s = System.getenv("GOALCHEMY_SEED");
            long v = s == null ? 1 : Long.parseLong(s);
            return v > 0 && v < (1L << 32) ? v : 1;
        } catch (NumberFormatException e) {
            return 1;
        }
    }

    public static void fatal(String msg) {
        throw new HostFatal(msg);
    }

    public static Scheduler sched = new Scheduler(new Task(0, null));

    public static void call(Task t, Frame child) {
        t.owner.assertDriver();
        child.parent = t.frame;
        t.frame = child;
    }

    public static void ret(Task t, Frame f) {
        sched.assertDriver();
        sched.exit(t, f, null);
    }

    static final class SyncFrame extends Frame {
        final Supplier<Object[]> fn;
        Object[] res = new Object[0];

        SyncFrame(Supplier<Object[]> fn) {
            this.fn = fn;
        }

        public void step(Task t) {
            res = fn.get();
            ret(t, this);
        }

        public Object[] results() {
            return res;
        }
    }

    /** Runs an ordinary call as a frame. */
    public static Frame sync(Supplier<Object[]> fn) {
        return new SyncFrame(fn);
    }

    /** Adapts an ordinary function value with n results to the resumable form. */
    public static Fn adapt(Fn f, int n) {
        if (f == null) return null;
        return Program.closure(f.fid(), a -> sync(() -> {
            Object r = f.call(a);
            return n == 0 ? new Object[0] : n == 1 ? new Object[] {r} : (Object[]) r;
        }));
    }

    public static Slice adaptSlice(Slice s, int n) {
        if (s.a == null) return s;
        Object[] a = new Object[s.l];
        for (int i = 0; i < s.l; i++) a[i] = adapt((Fn) s.get(i), n);
        return new Slice(a, 0, a.length, a.length);
    }

    /** go f(args): starts a task running frame f. */
    public static void spawn(Frame f) {
        sched.assertDriver();
        Task t = new Task(sched.nextId++, f);
        t.owner=sched;
        sched.ready(t);
    }

    private static boolean activeEntry;
    private static synchronized void reserveEntry() {
        if(activeEntry) throw new HostFault("overlapping executable entry/reset is unsupported");
        activeEntry=true;
    }
    private static synchronized void releaseEntry() { activeEntry=false; }
    static Scheduler install(Task main, boolean harness) {
        if(!sched.closed) {
            // An idle virtual owner may have been created on a different thread
            // by the harness. It has no asynchronous work and cannot be running.
            if(!sched.operations.isEmpty()) throw new HostFault("pending owner requires driver retirement");
            sched.driverRetireIdle();
        }
        Scheduler s = new Scheduler(main);
        s.harness = harness;
        sched = s;
        Program.panicState = () -> s.cur;
        return s;
    }

    /** Runs the existing virtual executable and reports only after retirement. */
    public static void runMain(Frame entry) {
        reserveEntry();
        Program.runLarge(() -> drive(() -> entry,false));
        System.out.flush(); System.exit(0);
    }
    /** Explicit monotonic executable entry; returns after native cleanup. Source
     * globals persist and init runs again. This is not an isolated library API. */
    public static void runMainHost(Supplier<Frame> entry) {
        reserveEntry(); drive(entry,true);
    }
    public static void runMainHost(Frame entry) { runMainHost(() -> entry); }
    private static void drive(Supplier<Frame> entry, boolean host) {
        Scheduler s=null; Throwable failure=null;

        try {
            Task main=new Task(0,null); s=install(main,false); s.hostMode=host;
            main.frame=entry.get(); s.ready(main);
            while(!main.done) {
                s.run(host ? s.nextHost() : s.next());
                if(s.handoff!=null) s.turnBack();
            }
        } catch(Throwable e) { failure=e; }
        if(s!=null) try { s.shutdown(); } catch(Throwable e) { if(failure==null) failure=e; }
        Program.resetPanicBinding();
        releaseEntry();
        if(s!=null && s.mail.interrupted) Thread.currentThread().interrupt();
        if(failure instanceof FatalPanic e) Program.reportPanic(e.p);
        if(failure instanceof HostFatal e) { Out.stderr("fatal error: " + e.getMessage() + "\n"); System.exit(2); }
        if(failure instanceof StackOverflowError) { Out.stderr("runtime: goroutine stack exceeds limit\nfatal error: stack overflow\n"); System.exit(2); }
        if(failure instanceof RuntimeException e) throw e;
        if(failure instanceof Error e) throw e;
        if(failure!=null) throw new HostFault(String.valueOf(failure));
    }

    /** A library entry owns the same guard as executables, but returns copied
     * host values and faults only after native resource retirement. */
    public static <T> T driveLibrary(Supplier<Frame> entry, Function<Object[],T> capture,
                                     Runnable control, java.util.function.Consumer<Runnable> attach) {
        return driveLibrary(entry,capture,control,attach,()->{});
    }
    public static <T> T driveLibrary(Supplier<Frame> entry, Function<Object[],T> capture,
                                     Runnable control, java.util.function.Consumer<Runnable> attach,Runnable retire) {
        reserveEntry(); Scheduler s=null; Throwable failure=null; T result=null;
        try {
            Task main=new Task(0,null); s=install(main,false); s.hostMode=true;
            s.libraryControl=control;
            Mailbox mailbox=s.mail;
            attach.accept(mailbox::signal);
            Frame root=entry.get();main.frame=root; s.ready(main);
            while(!main.done) { control.run(); s.run(s.nextHost()); if(s.handoff!=null) s.turnBack(); }
            // Frame results, including structured errors, remain source-owned here.
            result=capture.apply(root.results());
        } catch(Throwable e) { failure=e; }
        if(s!=null) try { s.shutdown(); } catch(Throwable e) { failure=e; }
        if(s!=null) { s.libraryControl=()->{}; if(s.mail.interrupted) Thread.currentThread().interrupt(); }
        try { retire.run(); } catch(Throwable e) { failure=e; }
        try { Program.resetPanicBinding(); } catch(Throwable e) { failure=e; }
        try { attach.accept(()->{}); } catch(Throwable e) { failure=e; }
        finally { releaseEntry(); }
        if(failure instanceof Library.Failure e) throw e;
        if(failure instanceof FatalPanic || failure instanceof HostFatal || failure instanceof StackOverflowError)
            throw new Library.Failure("source_panic");
        if(failure!=null) throw new Library.Failure("host_fault",java.util.Map.of(),failure);
        return result;
    }

    /** Requeues the running task: a pause primitive. */
    public static void yieldTask(Task t) {
        sched.ready(t);
        sched.block(t);
    }

    public interface Primitive {
        void run(Task t);
    }

    static final class AwaitFrame extends Frame {
        final Primitive fn;
        Object[] res = new Object[0];

        AwaitFrame(Primitive fn) {
            this.fn = fn;
        }

        public void step(Task t) {
            if (pc == 0) {
                pc = 1;
                fn.run(t);
                return;
            }
            res = t.rv;
            ret(t, this);
        }

        public Object[] results() {
            return res;
        }
    }

    /** Frames preserve the Task-style ABI of possibly suspending capabilities. */
    public static Frame nativeFrame(Primitive fn) { return new AwaitFrame(fn); }

    static RuntimeException rethrow(Throwable e) {
        if (e instanceof RuntimeException r) throw r;
        if (e instanceof Error r) throw r;
        throw new HostFault(String.valueOf(e));
    }

    public interface Body {
        Object[] call(Task t);
    }

    /** Unwinds a parked native body at shutdown without running source code. */
    static final class Kill extends Error {
        Kill() {
            super(null, null, false, false);
        }
    }

    /** A task body of the native pause model, run on its own thread. */
    public static final class NativeFrame extends Frame {
        final Body body;
        Task task;
        Thread thread;
        volatile boolean turn;
        boolean killed;
        Object[] res = new Object[0];

        NativeFrame(Body body) {
            this.body = body;
        }

        public void step(Task t) {
            task = t;
            t.owner.handoff = this;
        }

        public Object[] results() {
            return res;
        }

        void kill(Mailbox mail) {
            if (thread == null || killed) return;
            killed = true;
            turn = true;
            LockSupport.unpark(thread);
            for (;;) {
                try {
                    thread.join();
                    return;
                } catch (InterruptedException e) {
                    mail.interrupted = true;
                }
            }
        }
    }

    /** The frame of a native-pause function, for go, defer, function values
     * and library calls. */
    public static Frame nativeTask(Body body) { return new NativeFrame(body); }

    /** Completes a pause primitive that left native task t blocked: the
     * scheduler runs other tasks until it chooses t again. */
    public static void await(Task t) {
        NativeFrame n = (NativeFrame) t.frame;
        if (!t.owner.pass(t)) {
            while (!n.turn) LockSupport.park(n);
            n.turn = false;
            if (n.killed) throw new Kill();
        }
        GoPanic p = t.resumePanic;
        if (p != null) {
            t.resumePanic = null;
            throw p;
        }
    }

    /** Runs a function value's or method's frame on native task t and leaves
     * its results in t.rv. */
    public static void callFrame(Task t, Frame f) {
        if (f instanceof NativeFrame n) {
            t.rv = n.body.call(t);
        } else if (f instanceof SyncFrame s) {
            t.rv = s.fn.get();
        } else if (f instanceof AwaitFrame a) {
            a.fn.run(t);
            if (t.blocked) await(t);
        } else {
            throw new HostFault("synchronous call paused");
        }
    }

    /** Runs a native function's deferred calls, those that pause on task t;
     * recover and the panic chain behave as in Program.runDefers. */
    public static void runDefers(Task t, ArrayList<Program.Deferred> ds, GoPanic p) {
        GoPanic panicking = p;
        while (!ds.isEmpty()) {
            Program.Deferred d = ds.remove(ds.size() - 1);
            GoPanic savedPanic = t.curPanic;
            Object savedTarget = t.deferTarget;
            t.curPanic = panicking;
            t.deferTarget = d.fid;
            try {
                if (d.f == null) throw Panics.nilDeref();
                if (d.start) callFrame(t, (Frame) d.f.call(d.args));
                else d.f.call(d.args);
            } catch (GoPanic np) {
                if (np != panicking && np.prev == null) np.prev = panicking;
                panicking = np;
                continue;
            } finally {
                t.curPanic = savedPanic;
                t.deferTarget = savedTarget;
            }
            if (panicking != null && panicking.recovered) panicking = null;
        }
        if (panicking != null) throw panicking;
    }

    /** Runs one pause primitive in an isolated scheduler for a harness case;
     * throws Blocked when no task can run, and the source panic on panic. */
    public static Object[] runIsolated(Primitive fn) {
        reserveEntry();
        try {
        AwaitFrame h = new AwaitFrame(fn);
        Task main = new Task(0, h);
        Scheduler s = install(main, true);
        s.ready(main);
        try {
            while (!main.done) {
                s.run(s.next());
                if (s.handoff != null) s.turnBack();
            }
        } catch (FatalPanic e) {
            throw e.p;
        }
        return h.res;
        } finally { releaseEntry(); }
    }

    public static void resetScheduler() {
        reserveEntry();
        try { install(new Task(0, null), true); } finally { releaseEntry(); }
    }
}
