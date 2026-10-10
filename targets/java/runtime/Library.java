package rt;

import java.util.ArrayDeque;
import java.util.Arrays;
import java.util.Map;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.CompletionStage;
import java.util.function.Function;
import java.util.function.Supplier;

/** Serialized value calls. Only the owner builds source objects or resets globals. */
public final class Library {
    private Library() {}
    public static final class Failure extends RuntimeException {
        public final String kind;
        private final Map<String,Object> fields;
        public Failure(String kind) { this(kind,Map.of()); }
        public Failure(String kind,Map<String,Object> fields) { this(kind,fields,null); }
        public Failure(String kind,Map<String,Object> fields,Throwable cause) {
            super("goalchemy library: "+kind,cause);this.kind=kind;
            this.fields=copyFields(fields);
        }
        public Map<String,Object> fields() { return copyFields(fields); }
        private static Map<String,Object> copyFields(Map<String,Object> input) {
            var result=new java.util.LinkedHashMap<String,Object>();
            for(var e:input.entrySet()) result.put(e.getKey(),copy(e.getValue()));
            return java.util.Collections.unmodifiableMap(result);
        }
        private static Object copy(Object v) {
            if(v instanceof byte[] a)return a.clone();
            if(v instanceof Object[] a) {var r=new Object[a.length];for(int i=0;i<a.length;i++)r[i]=copy(a[i]);return r;}
            if(v instanceof Map<?,?> m){var r=new java.util.LinkedHashMap<String,Object>();for(var e:m.entrySet())r.put((String)e.getKey(),copy(e.getValue()));return java.util.Collections.unmodifiableMap(r);}
            return v;
        }
    }
    public static final class Options {
        public Callback.Registration[] callbacks;
    }
    /** Cancellation requests a stop; completion remains pending until actual cleanup. */
    public static final class Operation<T> {
        private final CompletableFuture<T> result=new CompletableFuture<>();
        private volatile boolean stopped;
        private volatile Runnable wake=()->{};
        private Job<T> job;
        public CompletionStage<T> completion() { return result.minimalCompletionStage(); }
        public boolean cancel() {
            boolean queued=false;
            synchronized(queue) {
                if(result.isDone()||stopped)return false;stopped=true;
                if(job!=null&&queue.remove(job)){job=null;queued=true;}
            }
            // Completion continuations must never execute while holding the queue monitor.
            if(queued)result.completeExceptionally(new Failure("canceled"));
            wake.run();return true;
        }
    }
    private static final ArrayDeque<Job<?>> queue=new ArrayDeque<>();
    private static boolean running;
    // Isolated native-launch fixtures only; no public scheduling API.
    static volatile java.util.concurrent.ThreadFactory completionThreads=work->Thread.ofVirtual().name("goalchemy-library-completion").unstarted(work);
    static volatile java.util.concurrent.Executor completionFallback=work->java.util.concurrent.ForkJoinPool.commonPool().execute(work);
    /** Saves, restores and zeroes generated source globals. zero keeps
     * canonical field references for a persistent instance. */
    public static final class State {
        final Runnable reset,zero;final Supplier<Object> save;final java.util.function.Consumer<Object> load;
        public State(Runnable reset,Runnable zero,Supplier<Object> save,java.util.function.Consumer<Object> load){this.reset=reset;this.zero=zero;this.save=save;this.load=load;}
    }
    /** Persistent source state behind a family of handles; only touched by
     * the library owner thread. */
    public static final class Instance {
        Object globals;final Native.State nativeState=new Native.State();
        final java.util.IdentityHashMap<Object,Object[]> objects=new java.util.IdentityHashMap<>();
        int live;boolean poisoned,retired;
        void retireNow(){if(retired)return;retired=true;globals=null;objects.clear();try{nativeState.retire();}catch(Throwable e){}}
    }
    /** The host's reference to one source object of an instance. */
    public static final class Handle {
        final Instance inst;Object obj;final String name;
        volatile boolean closed;boolean released;volatile Operation<?> active;
        Handle(Instance inst,Object obj,String name){this.inst=inst;this.obj=obj;this.name=name;}
        void release(){
            if(released)return;released=true;
            Object[] e=inst.objects.get(obj);if(e!=null&&e[0]==this)inst.objects.remove(obj);
            inst.live--;obj=null;if(inst.live==0)inst.retireNow();
        }
    }
    /** Boundary warnings such as leaked handles and abandoned goroutines. */
    public static volatile java.util.function.Consumer<String> warn=m->System.err.println("goalchemy: warning: "+m);
    private static final java.lang.ref.Cleaner cleaner=java.lang.ref.Cleaner.create();
    /** Returns the host wrapper for obj, reusing the live wrapper of the same
     * source object so host identity follows source identity. */
    @SuppressWarnings("unchecked")
    public static <W> W wrap(Instance inst,Object obj,String name,Function<Handle,W> make){
        if(obj==null)return null;
        Object[] e=inst.objects.get(obj);
        if(e!=null&&!((Handle)e[0]).closed){W w=((java.lang.ref.WeakReference<W>)e[1]).get();if(w!=null)return w;}
        Handle h=new Handle(inst,obj,name);W w=make.apply(h);
        inst.objects.put(obj,new Object[]{h,new java.lang.ref.WeakReference<>(w)});inst.live++;
        cleaner.register(w,()->finalizeHandle(h));
        return w;
    }
    @SuppressWarnings("unchecked")
    public static <T> T obj(Handle h){return h==null?null:(T)h.obj;}
    public static Handle handle(Object v,Class<?> type,Function<Object,Handle> get){if(v==null)return null;if(v.getClass()!=type)throw invalid();return get.apply(v);}
    private static void finalizeHandle(Handle h){
        if(h.closed)return;h.closed=true;
        enqueue(new Job<Void>(new Operation<>(),null,null,null,null,new Handle[0],null){
            @Override boolean run(){if(!h.released){warn.accept(h.name+" handle was not closed; releasing it without running Close");h.release();}return false;}
        });
    }
    private static class Job<T> {
        final Operation<T> op; final java.util.function.BiFunction<StdContextErr.Context,Boolean,TaskSpawn.Frame> entry;
        final State state;final java.util.function.BiFunction<Instance,Object[],T> capture;final Callback.Registration[] callbacks;
        final Handle[] handles;final Handle closing;final Handle self;
        Job(Operation<T> op,java.util.function.BiFunction<StdContextErr.Context,Boolean,TaskSpawn.Frame> entry,State state,
            java.util.function.BiFunction<Instance,Object[],T> capture,Callback.Registration[] callbacks,Handle[] handles,Handle closing) {
            this.op=op;this.entry=entry;this.state=state;this.capture=capture;this.callbacks=callbacks;this.handles=handles;this.closing=closing;
            Handle first=null;for(Handle h:handles)if(h!=null){first=h;break;}
            this.self=first==closing?null:first;
        }
        boolean run() {
            T value=null;Throwable failure=null;
            final StdContextErr.Context[] ctx={null};final boolean[] owns={false};
            Instance inst=null;boolean fresh=true;final boolean[] keep={false};final int[] left={0};
            try {
                if(op.stopped)throw new Failure(self!=null&&self.closed?"closed":"canceled");
                for(Handle h:handles){
                    if(h==null)continue;
                    if(h.released||(h!=closing&&h.closed))throw new Failure("closed");
                    if(inst!=null&&h.inst!=inst)throw new Failure("instance_mismatch");
                    inst=h.inst;
                }
                fresh=inst==null;
                final Instance owner=fresh?new Instance():inst;
                inst=owner;
                if(!fresh&&owner.poisoned)throw new Failure("poisoned");
                keep[0]=!fresh;
                if(self!=null){self.active=op;if(self.closed)throw new Failure("closed");}
                final boolean isFresh=fresh;
                value=TaskSpawn.driveLibrary(()-> {
                    owns[0]=true;
                    if(isFresh)state.reset.run();else state.load.accept(owner.globals);
                    Native.install(owner.nativeState);Callback.install(callbacks);
                    ctx[0]=(StdContextErr.Context)StdContextWithCancel.stdContextWithCancel(StdContextErr.BACKGROUND)[0];
                    return entry.apply(ctx[0],isFresh);
                },rv-> {
                    if(op.stopped)throw new Failure("canceled");
                    T out=capture.apply(owner,rv);
                    if(isFresh&&owner.live>0)keep[0]=true;
                    TaskSpawn.Scheduler s=TaskSpawn.sched;
                    for(TaskSpawn.Task t:s.tasks)if(t!=s.main&&!t.done)left[0]++;
                    return out;
                },()-> {if(op.stopped&&ctx[0]!=null){ Native.requestStop();StdContextErr.cancel(ctx[0],StdContextErr.CONTEXT_CANCELED); }},w->op.wake=w,()-> {
                    if(!owns[0])return;
                    Throwable f=null;
                    if(keep[0]){
                        try{owner.globals=state.save.get();}catch(Throwable e){f=e;}
                        Native.detach();
                    }else{try{Native.retire();}catch(Throwable e){f=e;}}
                    try{Callback.install(new Callback.Registration[0]);}catch(Throwable e){f=e;}
                    try{if(keep[0])state.zero.run();else state.reset.run();}catch(Throwable e){f=e;}
                    if(f!=null)throw new Failure("host_fault",Map.of(),f);
                });
                if(keep[0]&&left[0]>0)warn.accept(left[0]+" goroutine(s) still running when a handle call returned were abandoned");
            } catch(Throwable e) {
                failure=e instanceof Failure?e:new Failure("host_fault",Map.of(),e);
                String k=((Failure)failure).kind;
                if(!fresh&&inst!=null&&(k.equals("source_panic")||k.equals("host_fault")))inst.poisoned=true;
                if(k.equals("canceled")&&self!=null&&self.closed)failure=new Failure("canceled",Map.of("CauseCategory","closed"));
            }
            finally {
                ctx[0]=null;op.wake=()->{};op.job=null;
                if(self!=null&&self.active==op)self.active=null;
                if(closing!=null){
                    closing.release();
                    if(failure instanceof Failure f&&f.kind.equals("poisoned"))failure=null;
                }else if(keep[0]&&inst!=null&&inst.live==0)inst.retireNow();
            }
            // Host continuations may submit and wait for another call. Publish copied
            // results after retirement on a native thread, keeping the owner queue free.
            T ownedValue=value;Throwable ownedFailure=failure;
            try {
                completionThreads.newThread(()->{
                    if(ownedFailure!=null)op.result.completeExceptionally(ownedFailure);else op.result.complete(ownedValue);
                }).start();
            }catch(Throwable launchFailure){
                Failure dispatchFault=new Failure("host_fault",Map.of(),launchFailure);
                try{completionFallback.execute(()->op.result.completeExceptionally(dispatchFault));}
                catch(Throwable fallbackFailure){
                    Failure terminal=new Failure("host_fault",Map.of(),fallbackFailure);terminal.addSuppressed(dispatchFault);
                    return emergencyPublication(op,terminal);
                }
            }
            return false;
        }
    }
    public static <T> Operation<T> failed(Failure error) {
        Operation<T> op=new Operation<>();op.result.completeExceptionally(error);return op;
    }
    public static <T> Operation<T> submit(Options options,Function<StdContextErr.Context,TaskSpawn.Frame> entry,
                                         Runnable reset,Function<Object[],T> capture) {
        return submit(options,new State(reset,reset,()->null,v->{}),new Handle[0],(ctx,fresh)->entry.apply(ctx),(inst,rv)->capture.apply(rv),null);
    }
    public static <T> Operation<T> submit(Options options,State state,Handle[] handles,
                                         java.util.function.BiFunction<StdContextErr.Context,Boolean,TaskSpawn.Frame> entry,
                                         java.util.function.BiFunction<Instance,Object[],T> capture,Handle closing) {
        try {
            Callback.Registration[] callbacks=Callback.snapshot(options==null?null:options.callbacks);
            Operation<T> op=new Operation<>();Job<T> job=new Job<>(op,entry,state,capture,callbacks,handles,closing);op.job=job;
            if(job.self!=null&&job.self.closed)throw new Failure("closed");
            enqueue(job);
            return op;
        } catch(Failure e) {return failed(e);}
    }
    private static void enqueue(Job<?> job){
        synchronized(queue) {
            queue.add(job);
            if(!running) {
                running=true;
                try{Thread.ofPlatform().daemon().name("goalchemy-library-owner").stackSize(64L<<20).start(Library::drain);}
                catch(Throwable launchFailure){queue.remove(job);job.op.job=null;running=false;throw new Failure("host_fault",Map.of(),launchFailure);}
            }
        }
    }
    /** Fails calls queued on h, cancels its active call, runs the source Close
     * when entry is given and the instance is healthy, then releases h. */
    public static Operation<Void> close(Handle h,State state,java.util.function.BiFunction<StdContextErr.Context,Boolean,TaskSpawn.Frame> entry,Function<Box,Failure> failure){
        java.util.List<Job<?>> dropped=new java.util.ArrayList<>();
        synchronized(queue){
            if(h.closed){Operation<Void> done=new Operation<>();done.result.complete(null);return done;}
            h.closed=true;
            for(java.util.Iterator<Job<?>> it=queue.iterator();it.hasNext();){Job<?> j=it.next();if(j.self==h){it.remove();j.op.job=null;dropped.add(j);}}
        }
        for(Job<?> j:dropped)j.op.result.completeExceptionally(new Failure("closed"));
        Operation<?> active=h.active;if(active!=null){active.stopped=true;active.wake.run();}
        java.util.function.BiFunction<StdContextErr.Context,Boolean,TaskSpawn.Frame> run=entry!=null?entry:(ctx,fresh)->TaskSpawn.sync(()->new Object[]{null});
        return submit(null,state,new Handle[]{h},run,(inst,rv)->{Failure f=failure.apply((Box)rv[0]);if(f!=null)throw f;return null;},h);
    }
    private static void drain() {
        for(;;) {
            Job<?> job;
            synchronized(queue) {job=queue.poll();if(job==null){running=false;return;}}
            if(job.run())return;
        }
    }
    /** No independent publisher is available. Relinquish source-driver ownership
     * before synchronous emergency publication; arbitrary blocking host callback
     * graphs cannot be guaranteed parallel progress in this terminal path. */
    private static boolean emergencyPublication(Operation<?> current,Failure fault){
        Job<?>[] detached;
        synchronized(queue){detached=queue.toArray(Job<?>[]::new);queue.clear();running=false;for(Job<?> job:detached){job.op.job=null;job.op.wake=()->{};}}
        for(Job<?> job:detached)job.op.result.completeExceptionally(fault);
        current.result.completeExceptionally(fault);
        return true;
    }
    private static final class Sequence extends TaskSpawn.Frame {
        final TaskSpawn.Frame init;final Supplier<TaskSpawn.Frame> call;Object[] res=new Object[0];
        Sequence(TaskSpawn.Frame init,Supplier<TaskSpawn.Frame> call) {this.init=init;this.call=call;}
        public void step(TaskSpawn.Task t) {
            if(pc==0){pc=1;TaskSpawn.call(t,init);return;}
            if(pc==1){pc=2;TaskSpawn.call(t,call.get());return;}
            res=t.rv;TaskSpawn.ret(t,this);
        }
        public Object[] results(){return res;}
    }
    public static TaskSpawn.Frame sequence(TaskSpawn.Frame init,Supplier<TaskSpawn.Frame> call) {return new Sequence(init,call);}
    public static final class CopyContext {
        private final java.util.IdentityHashMap<Object,Boolean> path=new java.util.IdentityHashMap<>();
        private int nodes;
        public void enter(Object value){if(value==null)return;if(++nodes>1000000||path.size()>=128||path.put(value,true)!=null)throw invalid();}
        public void leave(Object value){if(value!=null)path.remove(value);}
    }
    public static Failure invalid(){return new Failure("invalid_argument");}
    public static void length(int n){if(n>64<<20)throw invalid();}
    public static byte[] bytes(byte[] v){if(v==null)return null;length(v.length);return v.clone();}
    public static String binaryString(String s){if(s==null)return "";length(s.length());for(int i=0;i<s.length();i++)if(s.charAt(i)>255)throw invalid();return s;}
    public static long integer(long value,int bits,boolean signed) {
        if(bits<64) {long lo=signed?-(1L<<(bits-1)):0,hi=(1L<<(signed?bits-1:bits))-1;if(value<lo||value>hi)throw invalid();}
        return value;
    }
}
