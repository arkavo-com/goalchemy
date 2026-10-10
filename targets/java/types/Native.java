package rt;

import java.nio.charset.StandardCharsets;
import java.security.PrivateKey;
import java.security.PublicKey;
import java.util.Arrays;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicLong;
import java.util.function.Function;

/** Owner-scoped native registry. Workers publish only copied wires and IDs. */
public final class Native {
    private Native() {}
    public static final int MAX=64<<20;
    public static final class Reject extends Exception {
        public Reject(String message){super(message);}
    }
    public static final class Material {
        private PrivateKey privateKey;private PublicKey publicKey;
        private boolean closing;private int leases;
        public Material(PrivateKey privateKey,PublicKey publicKey){this.privateKey=privateKey;this.publicKey=publicKey;}
        synchronized Lease acquire() throws Reject {
            if(closing)throw new Reject("crypto: key is closed");
            leases++;return new Lease(this,privateKey,publicKey);
        }
        public synchronized void invalidate(){closing=true;notifyAll();}
        public synchronized void close() {
            closing=true;boolean interrupted=false;
            while(leases!=0)try{wait();}catch(InterruptedException e){interrupted=true;}
            privateKey=null;publicKey=null;
            if(interrupted)Thread.currentThread().interrupt();
        }
        synchronized void release(){if(--leases<0)throw new TaskSpawn.HostFault("native key lease underflow");notifyAll();}
    }
    public static final class Lease implements AutoCloseable {
        private Material owner;public PrivateKey privateKey;public PublicKey publicKey;
        Lease(Material owner,PrivateKey privateKey,PublicKey publicKey){this.owner=owner;this.privateKey=privateKey;this.publicKey=publicKey;}
        public void close(){Material m=owner;owner=null;privateKey=null;publicKey=null;if(m!=null)m.release();}
    }
    /** Source opaque shells are constructed only by the owning driver. */
    public static final class Key {
        final State owner;final long id;
        Key(State owner,long id){this.owner=owner;this.id=id;}
    }
    public static final class State {
        final ConcurrentHashMap<Long,Material> keys=new ConcurrentHashMap<>();
        final java.util.Set<Long> staged=ConcurrentHashMap.newKeySet();
        final AtomicLong nextKey=new AtomicLong();final AtomicBoolean stopped=new AtomicBoolean();
        public long stage(Material value){long id=nextKey.incrementAndGet();keys.put(id,value);staged.add(id);if(stopped.get()){discard(id);return 0;}return id;}
        public void discard(long id){staged.remove(id);Material m=keys.remove(id);if(m!=null)m.close();}
        private void discardAll(Iterable<Long> ids){Throwable failure=null;for(long id:ids)try{discard(id);}catch(Throwable e){failure=e;}if(failure!=null)throw new TaskSpawn.HostFault("native registry retirement: "+failure);}
        public void discardStaged(){discardAll(staged);}
        public void retire(){stopped.set(true);discardAll(keys.keySet());}
    }
    private static State current;
    public static void install(){TaskSpawn.sched.assertDriver();if(current!=null)throw new TaskSpawn.HostFault("native owner already installed");current=new State();State state=current;TaskSpawn.sched.disposers.add(()->{try{state.retire();}finally{if(current==state)current=null;}});}
    /** Installs a library instance's persistent registry; driver shutdown
     * detaches it without closing its keys. */
    public static void install(State state){TaskSpawn.sched.assertDriver();if(current!=null)throw new TaskSpawn.HostFault("native owner already installed");state.stopped.set(false);current=state;TaskSpawn.sched.disposers.add(()->{if(current==state)current=null;});}
    public static void detach(){current=null;}
    public static void retire(){State state=current;if(state!=null)try{state.retire();}finally{if(current==state)current=null;}}
    public static void requestStop(){if(current!=null)current.stopped.set(true);}
    public static State state(){TaskSpawn.sched.assertDriver();if(current==null){install();}return current;}
    public static Key transfer(State state,long id) throws Reject {
        TaskSpawn.sched.assertDriver();if(id==0||state.stopped.get()){state.discard(id);throw new Reject("context canceled");}
        if(!state.keys.containsKey(id))throw new TaskSpawn.HostFault("missing staged key");state.staged.remove(id);return new Key(state,id);
    }
    public static Lease acquire(Key key) throws Reject {
        State state=state();if(key==null||key.owner!=state)throw new Reject("crypto: invalid input or key");
        Material material=state.keys.get(key.id);if(material==null)throw new Reject("crypto: key is closed");return material.acquire();
    }
    public static Material invalidate(Key key) throws Reject {
        if(key==null)return null;
        if(key.owner!=state())throw new Reject("crypto: invalid input or key");
        Material m=key.owner.keys.get(key.id);if(m!=null)m.invalidate();return m;
    }
    public static byte[] bytes(Slice value) throws Reject { return bytes(value,MAX); }
    public static byte[] bytes(Slice value,int maximum) throws Reject {
        if(value==null||value.l>maximum||value.l<0||!value.bytes&&value.l>0)throw new Reject("crypto: invalid input or key");
        if(value.a==null)return new byte[0];return Arrays.copyOfRange((byte[])value.a,value.o,value.o+value.l);
    }
    public static byte[] bytesNullable(Slice value){if(value.a==null)return null;return Arrays.copyOfRange((byte[])value.a,value.o,value.o+value.l);}
    public static Slice slice(byte[] value){return value==null?Slice.BYTE_NIL:new Slice(value,0,value.length,value.length);}
    public static String[] strings(Slice value) throws Reject {
        if(value==null||value.l>MAX)throw new Reject("crypto: invalid input or key");
        String[] a=new String[value.l];for(int i=0;i<a.length;i++)a[i]=(String)value.get(i);return a;
    }
    public static Slice strings(String[] value){Object[] a=value.clone();return new Slice(a,0,a.length,a.length);}
    public static String binary(byte[] value){return new String(value,StandardCharsets.ISO_8859_1);}
    public static byte[] binary(String value)throws Reject {
        if(value==null||value.length()>MAX)throw new Reject("crypto: invalid input or key");
        for(int i=0;i<value.length();i++)if(value.charAt(i)>255)throw new Reject("crypto: invalid input or key");
        return value.getBytes(StandardCharsets.ISO_8859_1);
    }
    public static String utf8(String binary)throws Reject {
        try{return StandardCharsets.UTF_8.newDecoder().onMalformedInput(java.nio.charset.CodingErrorAction.REPORT)
                .decode(java.nio.ByteBuffer.wrap(binary(binary))).toString();}
        catch(java.nio.charset.CharacterCodingException e){throw new Reject("crypto: invalid input or key");}
    }
    public static Box error(String text){return StdErrorsNew.stdErrorsNew(text);}
    public interface Work { Object[] run() throws Exception; }
    /** Inputs/snapshots are native-owned before start; resource release precedes ACK. */
    public static void async(TaskSpawn.Task task,Object[] zero,Work work,Function<Object[],Object[]> decode,AutoCloseable... leases) {
        State owner=state();AtomicBoolean stopping=new AtomicBoolean();
        TaskSpawn.HostToken token=TaskSpawn.sched.registerHost(task,()->{stopping.set(true);owner.stopped.set(true);},()->{},
            ()->owner.stopped.get()?failure(zero,"context canceled"):null,
            values->{String err=(String)values[0];if(err!=null)return failure(zero,err);return decode.apply((Object[])values[1]);});
        Operation nativeWork=new Operation(owner,token,work,leases);
        Thread worker;
        try {
            worker=Thread.ofVirtual().name("goalchemy-native").unstarted(nativeWork);
            worker.start();
        }catch(Throwable e){
            Throwable fault=e;try{nativeWork.release();}catch(Throwable cleanup){fault=cleanup;}
            token.complete(new Object[0],new TaskSpawn.HostFault("native submission/cleanup: "+fault));token.acknowledgeCleanup();return;
        }
        acknowledgeAfter(worker,token);
    }
    private static final class Operation implements Runnable {
        final State owner;final TaskSpawn.HostToken token;Work work;AutoCloseable[] leases;
        Operation(State owner,TaskSpawn.HostToken token,Work work,AutoCloseable[] leases){this.owner=owner;this.token=token;this.work=work;this.leases=leases;}
        void release(){
            work=null;AutoCloseable[] resources=leases;leases=null;
            Throwable failure=null;
            if(resources!=null)for(int i=0;i<resources.length;i++){AutoCloseable resource=resources[i];resources[i]=null;if(resource!=null)try{resource.close();}catch(Throwable e){failure=e;}}
            if(failure!=null)throw new TaskSpawn.HostFault("native cleanup: "+failure);
        }
        public void run(){
            Object[] values=null;String rejection=null;TaskSpawn.HostFault fault=null;
            try{Work current=work;work=null;values=current.run();}
            catch(Reject e){rejection=e.getMessage();}
            catch(Throwable e){fault=new TaskSpawn.HostFault("native adapter: "+e);}
            finally{try{release();}catch(Throwable e){fault=new TaskSpawn.HostFault("native resource cleanup: "+e);}}
            try{if(owner.stopped.get())owner.discardStaged();}catch(Throwable e){fault=new TaskSpawn.HostFault("staged key cleanup: "+e);}
            token.complete(new Object[]{rejection,values==null?new Object[0]:values},fault);
        }
    }
    /** Observer owns only native thread identity + numeric mailbox token. It never
     * carries operation input/key/result roots and ACKs after worker termination. */
    public static void acknowledgeAfter(Thread worker,TaskSpawn.HostToken token){
        Runnable settlement=()->{
            boolean interrupted=false;for(;;)try{worker.join();break;}catch(InterruptedException e){interrupted=true;}
            token.acknowledgeCleanup();if(interrupted)Thread.currentThread().interrupt();
        };
        try{Thread.ofVirtual().name("goalchemy-native-settlement").start(settlement);}
        catch(Throwable observerFailure){
            // No resource-release claim follows a failed observer launch until the
            // already-started worker has actually terminated.
            settlement.run();
            throw new TaskSpawn.HostFault("native settlement observer submission: "+observerFailure);
        }
    }

    public static Object[] failure(Object[] zero,String message){Object[] result=zero.clone();if(result.length>0)result[result.length-1]=error(message);return result;}
    public static void reject(TaskSpawn.Task task,Object[] zero,Reject error){task.rv=failure(zero,error.getMessage());}
}
