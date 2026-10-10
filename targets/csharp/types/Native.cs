namespace Rt;

/// Owner-scoped native key registry. Native workers exchange copied values and numeric IDs.
public static class Native
{
    public const int MAX=64<<20;
    public sealed class Reject : Exception {public Reject(string message):base(message){}}
    public sealed class Material
    {
        internal byte[] publicDer,privateDer;internal readonly bool ec;bool closing;int leases;
        public Material(byte[] publicDer,byte[] privateDer,bool ec){this.publicDer=publicDer;this.privateDer=privateDer;this.ec=ec;}
        internal Lease acquire(){lock(this){if(closing)throw new Reject("crypto: key is closed");leases++;return new Lease(this,publicDer,privateDer,ec);}}
        internal void invalidate(){lock(this){closing=true;}}
        public void close(){lock(this){closing=true;while(leases!=0)System.Threading.Monitor.Wait(this);if(privateDer!=null)System.Security.Cryptography.CryptographicOperations.ZeroMemory(privateDer);privateDer=null;publicDer=null;}}
        internal void release(){lock(this){if(--leases<0)throw new HostFault("native key lease underflow");System.Threading.Monitor.PulseAll(this);}}
    }
    public sealed class Lease : IDisposable
    {
        Material owner;internal byte[] publicDer,privateDer;internal readonly bool ec;
        internal Lease(Material owner,byte[] pub,byte[] priv,bool ec){this.owner=owner;publicDer=pub;privateDer=priv;this.ec=ec;}
        public void Dispose(){var m=owner;owner=null;publicDer=null;privateDer=null;m?.release();}
    }
    public sealed class Key{internal readonly State owner;internal readonly long id;internal Key(State owner,long id){this.owner=owner;this.id=id;}}
    public sealed class State
    {
        readonly System.Collections.Concurrent.ConcurrentDictionary<long,Material> keys=new();readonly System.Collections.Concurrent.ConcurrentDictionary<long,bool> staged=new();long next;internal volatile bool stopped;
        public long stage(Material value){long id=System.Threading.Interlocked.Increment(ref next);keys[id]=value;staged[id]=true;if(stopped){discard(id);return 0;}return id;}
        public void discard(long id){staged.TryRemove(id,out _);if(keys.TryRemove(id,out var key))key.close();}
        internal void discardStaged(){Exception failure=null;foreach(long id in staged.Keys)try{discard(id);}catch(Exception e){failure=e;}if(failure!=null)throw new HostFault("staged native retirement: "+failure);}
        public void retire(){stopped=true;Exception failure=null;foreach(long id in keys.Keys)try{discard(id);}catch(Exception e){failure=e;}if(failure!=null)throw new HostFault("native retirement: "+failure);}
        internal bool contains(long id)=>keys.ContainsKey(id);
        internal Material material(long id){keys.TryGetValue(id,out var result);return result;}
        internal void transfer(long id)=>staged.TryRemove(id,out _);
    }
    static State current;
    public static void install(){R.sched.assertDriver();if(current!=null)throw new HostFault("native owner already installed");var state=new State();current=state;R.sched.disposers.Add(()=>{try{state.retire();}finally{if(current==state)current=null;}});}
    /// Installs a library instance's persistent registry; driver shutdown detaches it without closing its keys.
    public static void install(State state){R.sched.assertDriver();if(current!=null)throw new HostFault("native owner already installed");state.stopped=false;current=state;R.sched.disposers.Add(()=>{if(current==state)current=null;});}
    public static void detach(){current=null;}
    public static void retire(){var state=current;if(state!=null)try{state.retire();}finally{if(current==state)current=null;}}
    public static void requestStop(){if(current!=null)current.stopped=true;}
    public static State state(){R.sched.assertDriver();if(current==null)install();return current;}
    public static Key transfer(State state,long id){R.sched.assertDriver();if(id==0||state.stopped){state.discard(id);throw new Reject("context canceled");}if(!state.contains(id))throw new HostFault("missing staged key");state.transfer(id);return new Key(state,id);}
    public static Lease acquire(Key key){if(key==null||key.owner!=state())throw new Reject("crypto: invalid input or key");var m=key.owner.material(key.id);if(m==null)throw new Reject("crypto: key is closed");return m.acquire();}
    public static Material invalidate(Key key){if(key==null)return null;if(key.owner!=state())throw new Reject("crypto: invalid input or key");var m=key.owner.material(key.id);m?.invalidate();return m;}
    public static byte[] bytes(Slice value,int maximum=MAX){if(value==null||value.l<0||value.l>maximum||!value.bytes&&value.l>0)throw new Reject("crypto: invalid input or key");if(value.a==null)return Array.Empty<byte>();var result=new byte[value.l];Array.Copy((byte[])value.a,value.o,result,0,value.l);return result;}
    public static byte[] bytesNullable(Slice value)=>value.a==null?null:bytes(value);
    public static Slice slice(byte[] value)=>value==null?Slice.BYTE_NIL:new Slice(value,0,value.Length,value.Length);
    public static string[] strings(Slice value){if(value==null||value.l>MAX)throw new Reject("crypto: invalid input or key");var result=new string[value.l];for(int i=0;i<result.Length;i++)result[i]=(string)value.Get(i);return result;}
    public static Slice strings(string[] value){object[] result=new object[value.Length];Array.Copy(value,result,value.Length);return new Slice(result,0,result.Length,result.Length);}
    public static string binary(byte[] value)=>System.Text.Encoding.Latin1.GetString(value);
    public static byte[] binary(string value){if(value==null||value.Length>MAX)throw new Reject("crypto: invalid input or key");foreach(char c in value)if(c>255)throw new Reject("crypto: invalid input or key");return System.Text.Encoding.Latin1.GetBytes(value);}
    public static string utf8(string value){try{return new System.Text.UTF8Encoding(false,true).GetString(binary(value));}catch(System.Text.DecoderFallbackException){throw new Reject("crypto: invalid input or key");}}
    public static Box error(string value)=>R.stdErrorsNew(value);
    public static object[] failure(object[] zero,string error){var result=(object[])zero.Clone();if(result.Length>0)result[^1]=Native.error(error);return result;}
    public static void reject(GoTask task,object[] zero,Reject error)=>task.rv=failure(zero,error.Message);
    public static void async(GoTask task,object[] zero,Func<object[]> work,Func<object[],object[]> decode,params IDisposable[] leases)
    {
        var owner=state();var token=R.sched.registerHost(task,()=>owner.stopped=true,null,()=>owner.stopped?failure(zero,"context canceled"):null,
            values=>values[0]!=null?failure(zero,(string)values[0]):decode((object[])values[1]));
        var operation=new Work(owner,token,work,leases);System.Threading.Thread worker;
        try{worker=new System.Threading.Thread(operation.run){IsBackground=true,Name="goalchemy-native"};worker.Start();}
        catch(Exception e){Exception fault=e;try{operation.release();}catch(Exception cleanup){fault=cleanup;}token.complete(Array.Empty<object>(),new HostFault("native submission/cleanup: "+fault));token.acknowledgeCleanup();return;}
        acknowledgeAfter(worker,token);
    }
    sealed class Work
    {
        readonly State owner;readonly HostToken token;Func<object[]> work;IDisposable[] leases;
        internal Work(State owner,HostToken token,Func<object[]> work,IDisposable[] leases){this.owner=owner;this.token=token;this.work=work;this.leases=leases;}
        internal void release(){work=null;var resources=leases;leases=null;Exception failure=null;if(resources!=null)for(int i=0;i<resources.Length;i++){var resource=resources[i];resources[i]=null;try{resource?.Dispose();}catch(Exception e){failure=e;}}if(failure!=null)throw new HostFault("native cleanup: "+failure);}
        internal void run(){object[] values=null;string rejected=null;HostFault fault=null;try{var current=work;work=null;values=current();}catch(Reject e){rejected=e.Message;}catch(Exception e){fault=new HostFault("native adapter: "+e);}finally{try{release();}catch(Exception e){fault=new HostFault("native cleanup: "+e);}}try{if(owner.stopped)owner.discardStaged();}catch(Exception e){fault=new HostFault("staged cleanup: "+e);}token.complete(new object[]{rejected,values??Array.Empty<object>()},fault);}
    }
    public static void acknowledgeAfter(System.Threading.Thread worker,HostToken token)
    {
        Action settle=()=>{for(;;)try{worker.Join();break;}catch(System.Threading.ThreadInterruptedException){}token.acknowledgeCleanup();};
        try{var observer=new System.Threading.Thread(()=>settle()){IsBackground=true,Name="goalchemy-native-settlement"};observer.Start();}
        catch(Exception e){settle();throw new HostFault("settlement observer submission: "+e);}
    }
}
