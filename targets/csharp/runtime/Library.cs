namespace Rt;

/// Serialized copied value calls; source construction and retirement stay on the acquired owner.
public static class Library
{
    public sealed class Failure : Exception
    {
        public readonly string Kind;
        readonly Dictionary<string,object> fields;
        public Failure(string kind, IDictionary<string,object> fields = null, Exception cause = null) : base("goalchemy library: " + kind, cause)
        { Kind = kind; this.fields = CopyFields(fields); }
        public Dictionary<string,object> Fields => CopyFields(fields);
        static Dictionary<string,object> CopyFields(IDictionary<string,object> input)
        { var result = new Dictionary<string,object>(); if (input != null) foreach (var p in input) result.Add(p.Key,R.snapshot(p.Value)); return result; }
    }
    public sealed class Options { public Callback.Registration[] Callbacks; public System.Threading.CancellationToken Cancellation; }
    public sealed class Operation<T>
    {
        internal readonly System.Threading.Tasks.TaskCompletionSource<T> result = new(System.Threading.Tasks.TaskCreationOptions.RunContinuationsAsynchronously);
        internal volatile bool stopped; internal Action wake = () => {}; internal IJob job;
        public System.Threading.Tasks.Task<T> Completion => result.Task;
        public bool Cancel()
        {
            bool queued = false; Action notify; IJob detached=null;
            lock (queue) { if (result.Task.IsCompleted || stopped) return false; stopped = true; if (job != null && queue.Remove(job)) { detached=job; job = null; queued = true; } notify = wake; }
            if (queued) { Exception failure=null;try{detached.ReleaseRegistration();}catch(Exception e){failure=e;}result.TrySetException(failure==null?new Failure("canceled"):new Failure("host_fault",null,failure)); }
            notify(); return true;
        }
    }
    internal interface IJob { void Run(); void ReleaseRegistration(); Handle Self { get; } void Fail(Failure failure); }
    static readonly List<IJob> queue = new(); static bool running;
    internal static Func<System.Threading.ThreadStart,System.Threading.Thread> ownerThreads=work=>new System.Threading.Thread(work,64<<20){IsBackground=true,Name="goalchemy-library-owner"};
    /// Saves, restores and zeroes generated source globals. Zero keeps canonical field references for a persistent instance.
    public sealed class State
    {
        internal readonly Action reset,zero;internal readonly Func<object> save;internal readonly Action<object> load;
        public State(Action reset,Action zero,Func<object> save,Action<object> load){this.reset=reset;this.zero=zero;this.save=save;this.load=load;}
    }
    /// Persistent source state behind a family of handles; only touched by the library owner thread.
    public sealed class Instance
    {
        internal object globals;internal readonly Native.State nativeState=new();
        internal readonly Dictionary<object,(Handle handle,WeakReference<object> wrapper)> objects=new(System.Collections.Generic.ReferenceEqualityComparer.Instance);
        internal int live;internal bool poisoned,retired;
        internal void RetireNow(){if(retired)return;retired=true;globals=null;objects.Clear();try{nativeState.retire();}catch(Exception){}}
    }
    /// The host's reference to one source object of an instance.
    public sealed class Handle
    {
        internal readonly Instance inst;internal object obj;internal readonly string name;
        internal volatile bool closed;internal bool released;internal volatile IJob active;internal Action cancelActive;
        internal Handle(Instance inst,object obj,string name){this.inst=inst;this.obj=obj;this.name=name;}
        internal void Release(){if(released)return;released=true;if(inst.objects.TryGetValue(obj,out var e)&&e.handle==this)inst.objects.Remove(obj);inst.live--;obj=null;if(inst.live==0)inst.RetireNow();}
    }
    /// Boundary warnings such as leaked handles and abandoned goroutines.
    public static volatile Action<string> Warn=m=>Console.Error.WriteLine("goalchemy: warning: "+m);
    sealed class Finalizer{readonly Handle h;internal Finalizer(Handle h){this.h=h;}~Finalizer(){FinalizeHandle(h);}}
    static readonly System.Runtime.CompilerServices.ConditionalWeakTable<object,Finalizer> finalizers=new();
    /// Returns the host wrapper for obj, reusing the live wrapper of the same source object so host identity follows source identity.
    public static W wrap<W>(Instance inst,object obj,string name,Func<Handle,W> make) where W:class
    {
        if(obj==null)return null;
        if(inst.objects.TryGetValue(obj,out var e)&&!e.handle.closed&&e.wrapper.TryGetTarget(out var existing))return (W)existing;
        var h=new Handle(inst,obj,name);var w=make(h);
        inst.objects[obj]=(h,new WeakReference<object>(w));inst.live++;finalizers.Add(w,new Finalizer(h));
        return w;
    }
    public static T obj<T>(Handle h)=>h==null?default:(T)h.obj;
    public static Handle handle(object v,Type type,Func<object,Handle> get){if(v==null)return null;if(v.GetType()!=type)throw invalid();return get(v);}
    static void FinalizeHandle(Handle h)
    {
        if(h.closed)return;h.closed=true;
        Enqueue(new ReleaseJob(h));
    }
    sealed class ReleaseJob : IJob
    {
        readonly Handle h;internal ReleaseJob(Handle h){this.h=h;}
        public Handle Self=>null;public void ReleaseRegistration(){}public void Fail(Failure failure){}
        public void Run(){if(!h.released){Warn(h.name+" handle was not closed; releasing it without running Close");h.Release();}}
    }
    sealed class Job<T> : IJob
    {
        readonly Operation<T> op; readonly Func<GoContext,bool,Frame> entry; readonly State state; readonly Func<Instance,object[],T> capture; readonly Callback.Registration[] callbacks;
        readonly Handle[] handles;readonly Handle closing;readonly Handle self;
        internal System.Threading.CancellationTokenRegistration cancellation;
        internal Job(Operation<T> op,Func<GoContext,bool,Frame> entry,State state,Func<Instance,object[],T> capture,Callback.Registration[] callbacks,Handle[] handles,Handle closing)
        {
            this.op=op;this.entry=entry;this.state=state;this.capture=capture;this.callbacks=callbacks;this.handles=handles;this.closing=closing;
            Handle first=null;foreach(var h in handles)if(h!=null){first=h;break;}self=first==closing?null:first;
        }
        public Handle Self=>self;
        public void Fail(Failure failure){ReleaseRegistration();op.result.TrySetException(failure);}
        public void ReleaseRegistration()=>cancellation.Dispose();
        internal void Register(System.Threading.CancellationToken token) { cancellation=token.Register(() => op.Cancel()); }
        public void Run()
        {
            T value=default; Exception failure=null; GoContext ctx=null; bool owns=false;
            Instance inst=null;bool fresh=true,keep=false;int left=0;
            try
            {
                if(op.stopped)throw new Failure("canceled");
                foreach(var h in handles)
                {
                    if(h==null)continue;
                    if(h.released||(h!=closing&&h.closed))throw new Failure("closed");
                    if(inst!=null&&h.inst!=inst)throw new Failure("instance_mismatch");
                    inst=h.inst;
                }
                fresh=inst==null;var owner=inst??new Instance();inst=owner;
                if(!fresh&&owner.poisoned)throw new Failure("poisoned");
                keep=!fresh;
                if(self!=null){self.active=this;self.cancelActive=()=>op.Cancel();if(self.closed)throw new Failure("closed");}
                value=R.driveLibrary(() => {
                        owns=true;if(fresh)state.reset();else state.load(owner.globals);
                        Native.install(owner.nativeState);Callback.install(callbacks);ctx=(GoContext)R.stdContextWithCancel(R.BACKGROUND)[0];return entry(ctx,fresh);
                    },
                    rv => {
                        if(op.stopped)throw new Failure("canceled");
                        var output=capture(owner,rv);if(fresh&&owner.live>0)keep=true;
                        var s=R.sched;foreach(var t in s.tasks)if(t!=s.main&&!t.done)left++;
                        return output;
                    },
                    () => { if(op.stopped && ctx!=null) { Native.requestStop();R.cancel(ctx,R.CONTEXT_CANCELED); } },
                    w => {lock(queue)op.wake=w;}, () => {
                        if(!owns)return;
                        Exception fault=null;
                        if(keep){try{owner.globals=state.save();}catch(Exception e){fault=e;}Native.detach();}
                        else{try{Native.retire();}catch(Exception e){fault=e;}}
                        try {Callback.install(Array.Empty<Callback.Registration>());}catch(Exception e){fault=e;}
                        try{if(keep)state.zero();else state.reset();}catch(Exception e){fault=e;}
                        if(fault!=null)throw new Failure("host_fault",null,fault);
                    });
                if(keep&&left>0)Warn(left+" goroutine(s) still running when a handle call returned were abandoned");
            }
            catch(Exception e)
            {
                var f=e as Failure ?? new Failure("host_fault",null,e);failure=f;
                if(!fresh&&inst!=null&&(f.Kind=="panic"||f.Kind=="fatal"||f.Kind=="host_fault"))inst.poisoned=true;
                if(f.Kind=="canceled"&&self!=null&&self.closed)failure=new Failure("canceled",new Dictionary<string,object>{["CauseCategory"]="closed"});
            }
            finally
            {
                ctx=null;lock(queue){op.wake=()=>{};op.job=null;}cancellation.Dispose();
                if(self!=null&&self.active==this){self.active=null;self.cancelActive=null;}
                if(closing!=null){closing.Release();if(failure is Failure f&&f.Kind=="poisoned")failure=null;}
                else if(keep&&inst!=null&&inst.live==0)inst.RetireNow();
            }
            // TCS uses RunContinuationsAsynchronously: user code cannot block this owner or execute under its queue monitor.
            if(failure!=null)op.result.TrySetException(failure);else op.result.TrySetResult(value);
        }
    }
    public static Operation<T> failed<T>(Failure error){var op=new Operation<T>();op.result.SetException(error);return op;}
    public static Operation<T> submit<T>(Options options,Func<GoContext,Frame> entry,Action reset,Func<object[],T> capture)
        =>submit<T>(options,new State(reset,reset,()=>null,_=>{}),Array.Empty<Handle>(),(ctx,fresh)=>entry(ctx),(inst,rv)=>capture(rv),null);
    public static Operation<T> submit<T>(Options options,State state,Handle[] handles,Func<GoContext,bool,Frame> entry,Func<Instance,object[],T> capture,Handle closing)
    {
        Job<T> job=null;
        try
        {
            var callbacks=Callback.snapshot(options?.Callbacks); var op=new Operation<T>();job=new Job<T>(op,entry,state,capture,callbacks,handles,closing);op.job=job;
            if(job.Self!=null&&job.Self.closed)throw new Failure("closed");
            job.Register(options?.Cancellation ?? default);
            Enqueue(job);
            return op;
        }
        catch(Failure e){try{job?.ReleaseRegistration();}catch(Exception cleanup){return failed<T>(new Failure("host_fault",null,cleanup));}return failed<T>(e);}
        catch(Exception e){try{job?.ReleaseRegistration();}catch(Exception cleanup){e=cleanup;}return failed<T>(new Failure("host_fault",null,e));}
    }
    static void Enqueue(IJob job)
    {
        lock(queue)
        {
            queue.Add(job);if(!running){running=true;try{var owner=ownerThreads(Drain);owner.Start();}
                catch(Exception e){queue.Remove(job);running=false;throw new Failure("host_fault",null,e);}}
        }
    }
    /// Fails calls queued on h, cancels its active call, runs the source Close when entry is given and the instance is healthy, then releases h.
    public static Operation<object> close(Handle h,State state,Func<GoContext,bool,Frame> entry,Func<Box,Failure> failure)
    {
        var dropped=new List<IJob>();
        lock(queue)
        {
            if(h.closed){var done=new Operation<object>();done.result.SetResult(null);return done;}
            h.closed=true;
            for(int i=queue.Count-1;i>=0;i--)if(queue[i].Self==h){dropped.Add(queue[i]);queue.RemoveAt(i);}
        }
        foreach(var j in dropped)j.Fail(new Failure("closed"));
        h.cancelActive?.Invoke();
        return submit<object>(null,state,new[]{h},entry??((ctx,fresh)=>R.sync(()=>new object[]{null})),(inst,rv)=>{var f=failure((Box)rv[0]);if(f!=null)throw f;return null;},h);
    }
    static void Drain(){for(;;){IJob job;lock(queue){if(queue.Count==0){running=false;return;}job=queue[0];queue.RemoveAt(0);}job.Run();}}
    sealed class Sequence : Frame
    {
        readonly Frame init;readonly Func<Frame> call;object[] res=Array.Empty<object>();
        internal Sequence(Frame init,Func<Frame> call){this.init=init;this.call=call;}
        public override void step(GoTask t){if(pc==0){pc=1;R.call(t,init);return;}if(pc==1){pc=2;R.call(t,call());return;}res=t.rv;R.ret(t,this);}
        public override object[] results()=>res;
    }
    public static Frame sequence(Frame init,Func<Frame> call)=>new Sequence(init,call);
    public sealed class CopyContext
    {
        readonly HashSet<object> path=new(System.Collections.Generic.ReferenceEqualityComparer.Instance);int nodes;
        public void enter(object value){if(value!=null && (++nodes>1000000||path.Count>=128||!path.Add(value)))throw invalid();}
        public void leave(object value){if(value!=null)path.Remove(value);}
    }
    public static Failure invalid()=>new("invalid_argument");
    public static void length(int n){if(n>64<<20)throw invalid();}
    public static byte[] bytes(byte[] value){if(value==null)return null;length(value.Length);return (byte[])value.Clone();}
    public static string binaryString(string value){value??="";length(value.Length);foreach(char c in value)if(c>255)throw invalid();return value;}
    public static long integer(long value,int bits,bool signed){if(bits<64){long low=signed?-(1L<<(bits-1)):0,high=(1L<<(signed?bits-1:bits))-1;if(value<low||value>high)throw invalid();}return value;}
}
