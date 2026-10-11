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
        final java.util.LinkedHashMap<String,Callback.Provider> streams=new java.util.LinkedHashMap<>();long nextStream;
        void retireNow(){if(retired)return;retired=true;globals=null;objects.clear();streams.clear();try{nativeState.retire();}catch(Throwable e){}}
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
        Callback.Provider[] streams=new Callback.Provider[0];long[] ids=new long[0];
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
                    Native.install(owner.nativeState);Callback.install(registry(owner,callbacks,streams,ids));
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
        return submit(options,state,handles,entry,capture,closing,new Callback.Provider[0],new long[0]);
    }
    /** streams serve host std/io arguments; each is registered on the
     * instance under a fresh id, written to ids before entry runs. */
    public static <T> Operation<T> submit(Options options,State state,Handle[] handles,
                                         java.util.function.BiFunction<StdContextErr.Context,Boolean,TaskSpawn.Frame> entry,
                                         java.util.function.BiFunction<Instance,Object[],T> capture,Handle closing,
                                         Callback.Provider[] streams,long[] ids) {
        if(STREAM_THREAD.get())return failed(new Failure("reentrant"));
        try {
            Callback.Registration[] callbacks=Callback.snapshot(options==null?null:options.callbacks);
            Operation<T> op=new Operation<>();Job<T> job=new Job<>(op,entry,state,capture,callbacks,handles,closing);op.job=job;
            job.streams=streams;job.ids=ids;
            if(job.self!=null&&job.self.closed)throw new Failure("closed");
            enqueue(job);
            return op;
        } catch(Failure e) {return failed(e);}
    }
    private static Callback.Registration[] registry(Instance inst,Callback.Registration[] callbacks,Callback.Provider[] streams,long[] ids){
        for(int i=0;i<streams.length;i++){
            if(streams[i]==null)continue;
            ids[i]=++inst.nextStream;inst.streams.put("goalchemy.stream."+inst.nextStream,streams[i]);
        }
        if(inst.streams.isEmpty())return callbacks;
        var all=new java.util.ArrayList<>(java.util.Arrays.asList(callbacks));
        for(var e:inst.streams.entrySet())all.add(new Callback.Registration(e.getKey(),e.getValue()));
        return all.toArray(Callback.Registration[]::new);
    }
    static final ThreadLocal<Boolean> STREAM_THREAD=ThreadLocal.withInitial(()->false);
    /** Adapts a host stream to the provider serving one std/io parameter:
     * InputStream, OutputStream, SeekableByteChannel (seekable) or byte[]
     * (seekable). Each operation runs on its own thread with copied buffers;
     * end of stream becomes io.EOF. Returns null for a null stream. */
    public static Callback.Provider stream(Object v,String kind){
        if(v==null)return null;
        Object s=v instanceof byte[] b?new java.nio.channels.SeekableByteChannel(){
            final byte[] data=b.clone();long pos;boolean open=true;
            public int read(java.nio.ByteBuffer dst){if(pos>=data.length)return -1;int n=(int)Math.min(dst.remaining(),data.length-pos);dst.put(data,(int)pos,n);pos+=n;return n;}
            public int write(java.nio.ByteBuffer src){throw new java.nio.channels.NonWritableChannelException();}
            public long position(){return pos;}
            public java.nio.channels.SeekableByteChannel position(long p){pos=p;return this;}
            public long size(){return data.length;}
            public java.nio.channels.SeekableByteChannel truncate(long size){throw new java.nio.channels.NonWritableChannelException();}
            public boolean isOpen(){return open;}
            public void close(){open=false;}
        }:v;
        boolean reads=s instanceof java.io.InputStream||s instanceof java.nio.channels.ReadableByteChannel;
        boolean writes=s instanceof java.io.OutputStream||s instanceof java.nio.channels.WritableByteChannel;
        boolean seeks=s instanceof java.nio.channels.SeekableByteChannel;
        if((kind.contains("Read")&&!reads)||(kind.contains("Write")&&!writes)||(kind.contains("Seek")&&!seeks))throw invalid();
        Object stream=s;
        return request->{
            request.retain();
            Thread.ofPlatform().daemon().name("goalchemy-stream").start(()->{
                STREAM_THREAD.set(true);
                byte[] reply;
                try{reply=serve(stream,request.input());}
                catch(Throwable e){String m=e.getMessage()==null?e.toString():e.getMessage();byte[] t=m.getBytes(java.nio.charset.StandardCharsets.UTF_8);reply=new byte[1+t.length];reply[0]=2;System.arraycopy(t,0,reply,1,t.length);}
                finally{STREAM_THREAD.set(false);}
                request.resolve(reply);
            });
        };
    }
    private static long streamInt(byte[] b,int at){long v=0;for(int i=0;i<8;i++)v=v<<8|(b[at+i]&0xff);return v;}
    private static byte[] streamReply(long v){byte[] b=new byte[9];for(int i=8;i>=1;i--){b[i]=(byte)v;v>>=8;}return b;}
    private static byte[] serve(Object s,byte[] req)throws java.io.IOException{
        switch(req[0]){
        case 'r':{
            int n=(int)streamInt(req,1);if(n<0||n>=128<<10)throw new java.io.IOException("hostio: malformed stream request");
            byte[] buf=new byte[1+n];int k;
            if(s instanceof java.io.InputStream in)k=n==0?0:in.read(buf,1,n);
            else k=((java.nio.channels.ReadableByteChannel)s).read(java.nio.ByteBuffer.wrap(buf,1,n));
            if(k<0)return new byte[]{1};
            return java.util.Arrays.copyOf(buf,1+k);
        }
        case 'w':{
            int n=req.length-1;
            if(s instanceof java.io.OutputStream out){out.write(req,1,n);out.flush();}
            else{var buf=java.nio.ByteBuffer.wrap(req,1,n);var ch=(java.nio.channels.WritableByteChannel)s;while(buf.hasRemaining())ch.write(buf);}
            return streamReply(n);
        }
        case 's':{
            var ch=(java.nio.channels.SeekableByteChannel)s;long off=streamInt(req,1);
            long base=switch(req[9]){case 0->0;case 1->ch.position();case 2->ch.size();default->throw new java.io.IOException("Seek: invalid whence");};
            long pos=base+off;if(pos<0)throw new java.io.IOException("Seek: negative position");ch.position(pos);return streamReply(pos);
        }
        case 'c':((java.io.Closeable)s).close();return new byte[]{0};
        }
        throw new java.io.IOException("hostio: malformed stream request");
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
    /** Source std/io operations behind a host stream view; each one is a
     * library call on the handle. read completes with {byte[] data, Boolean eof}. */
    public static final class StreamOps {
        public interface Read{Operation<Object[]> apply(Handle h,long n);}
        public interface Write{Operation<Long> apply(Handle h,byte[] data);}
        public interface Seek{Operation<Long> apply(Handle h,long offset,long whence);}
        final Read read;final Write write;final Seek seek;final Function<Handle,Operation<Void>> close;
        public StreamOps(Read read,Write write,Seek seek,Function<Handle,Operation<Void>> close){this.read=read;this.write=write;this.seek=seek;this.close=close;}
    }
    static <T> T await(Operation<T> op){
        try{return op.completion().toCompletableFuture().join();}
        catch(java.util.concurrent.CompletionException e){if(e.getCause() instanceof Failure f)throw f;throw e;}
    }
    /** The host view of a source std/io reader or writer. Operations block
     * until their library call completes and fail with Library.Failure; an
     * operation the source lacks fails with kind "unsupported". close runs
     * the source Close, if any, and releases the handle. */
    public static final class SourceStream implements AutoCloseable {
        final Handle h;final StreamOps ops;
        public SourceStream(Handle h,StreamOps ops){this.h=h;this.ops=ops;}
        public boolean canRead(){return ops.read!=null;}
        public boolean canWrite(){return ops.write!=null;}
        public boolean canSeek(){return ops.seek!=null;}
        /** Reads up to n bytes; completes with null at end of stream. */
        public Operation<byte[]> readAsync(int n){
            if(ops.read==null)return failed(new Failure("unsupported"));
            if(n<0)return failed(invalid());
            Operation<byte[]> out=new Operation<>();
            if(n==0){out.result.complete(new byte[0]);return out;}
            ops.read.apply(h,n).completion().whenComplete((rv,e)->{
                if(e!=null){out.result.completeExceptionally(e instanceof java.util.concurrent.CompletionException c&&c.getCause()!=null?c.getCause():e);return;}
                byte[] data=(byte[])rv[0];if(data==null)data=new byte[0];
                out.result.complete(data.length==0&&(Boolean)rv[1]?null:data);
            });
            return out;
        }
        public Operation<Long> writeAsync(byte[] data){
            if(ops.write==null)return failed(new Failure("unsupported"));
            if(data==null)return failed(invalid());
            return ops.write.apply(h,data.clone());
        }
        public Operation<Long> seekAsync(long offset,int whence){
            if(ops.seek==null)return failed(new Failure("unsupported"));
            if(whence<0||whence>2)return failed(invalid());
            return ops.seek.apply(h,offset,whence);
        }
        public Operation<Void> closeAsync(){return ops.close.apply(h);}
        /** Reads into b; returns the count, or -1 at end of stream. */
        public int read(byte[] b,int off,int len){
            java.util.Objects.checkFromIndexSize(off,len,b.length);
            if(len==0)return 0;
            byte[] data=await(readAsync(Math.min(len,1<<20)));
            if(data==null)return -1;
            System.arraycopy(data,0,b,off,data.length);return data.length;
        }
        public void write(byte[] b,int off,int len){
            java.util.Objects.checkFromIndexSize(off,len,b.length);
            int done=0;
            do{int n=Math.min(len-done,1<<20);done+=(int)(long)await(writeAsync(java.util.Arrays.copyOfRange(b,off+done,off+done+n)));}while(done<len);
        }
        /** Seeks; whence is 0 (start), 1 (current) or 2 (end). */
        public long seek(long offset,int whence){return await(seekAsync(offset,whence));}
        @Override public void close(){await(closeAsync());}
        /** An InputStream over the reader; its close closes the handle.
         * Failures surface as IOException with the Library.Failure as cause. */
        public java.io.InputStream asInputStream(){
            if(!canRead())throw new Failure("unsupported");
            return new java.io.InputStream(){
                public int read()throws java.io.IOException{byte[] one=new byte[1];int n=read(one,0,1);return n<0?-1:one[0]&0xff;}
                public int read(byte[] b,int off,int len)throws java.io.IOException{try{return SourceStream.this.read(b,off,len);}catch(Failure f){throw new java.io.IOException(f.getMessage(),f);}}
                public long skip(long n)throws java.io.IOException{
                    if(n<=0)return 0;
                    if(canSeek()){try{long at=seek(0,1),end=seek(0,2),to=Math.min(end,at+n);seek(to,0);return to-at;}catch(Failure f){throw new java.io.IOException(f.getMessage(),f);}}
                    return super.skip(n);
                }
                public void close()throws java.io.IOException{try{SourceStream.this.close();}catch(Failure f){throw new java.io.IOException(f.getMessage(),f);}}
            };
        }
        /** An OutputStream into the writer; its close closes the handle.
         * Failures surface as IOException with the Library.Failure as cause. */
        public java.io.OutputStream asOutputStream(){
            if(!canWrite())throw new Failure("unsupported");
            return new java.io.OutputStream(){
                public void write(int b)throws java.io.IOException{write(new byte[]{(byte)b},0,1);}
                public void write(byte[] b,int off,int len)throws java.io.IOException{try{SourceStream.this.write(b,off,len);}catch(Failure f){throw new java.io.IOException(f.getMessage(),f);}}
                public void close()throws java.io.IOException{try{SourceStream.this.close();}catch(Failure f){throw new java.io.IOException(f.getMessage(),f);}}
            };
        }
    }
}
