package rt;

import java.io.*;
import java.net.http.*;
import java.nio.charset.StandardCharsets;
import java.util.*;
import java.util.concurrent.CompletableFuture;
import java.util.function.Function;

/** Streamed lib/http exchanges on java.net.http; one virtual thread per operation. */
public final class LibHttpExchangeClose {
    private LibHttpExchangeClose() {}
    static final String CLOSED="http: exchange closed",STATE="http: invalid exchange operation",FAILED="http: transport failure";

    /** Request body handoff: each write blocks until the client has consumed it, so at most one chunk is held. */
    static final class Pipe extends InputStream {
        private byte[] chunk;private int offset;private boolean ended;private IOException failure;
        synchronized void write(byte[] data)throws IOException,InterruptedException {
            if(failure!=null)throw failure;if(ended)throw new IOException("pipe ended");
            chunk=data;offset=0;notifyAll();
            while(chunk!=null&&failure==null)wait();
            if(failure!=null)throw failure;
        }
        synchronized void end(){ended=true;notifyAll();}
        synchronized void fail(IOException e){if(failure==null)failure=e;notifyAll();}
        @Override public synchronized int read(byte[] b,int off,int len)throws IOException {
            if(len==0)return 0;
            try{while(chunk==null&&!ended&&failure==null)wait();}catch(InterruptedException e){Thread.currentThread().interrupt();throw new InterruptedIOException();}
            if(failure!=null)throw failure;if(chunk==null)return -1;
            int n=Math.min(len,chunk.length-offset);System.arraycopy(chunk,offset,b,off,n);offset+=n;
            if(offset==chunk.length){chunk=null;notifyAll();}
            return n;
        }
        @Override public int read()throws IOException {byte[] one=new byte[1];int n=read(one,0,1);return n<0?-1:one[0]&255;}
        @Override public void close(){fail(new IOException("request body closed"));}
    }

    public static final class Exchange {
        final TaskSpawn.Scheduler owner;StdContextErr.Boundary boundary;final long length;long written;boolean closed,responded;
        final Pipe pipe;HttpClient client;CompletableFuture<HttpResponse<InputStream>> future;volatile InputStream body;Runnable dispose;
        Exchange(TaskSpawn.Scheduler owner,long length,Pipe pipe){this.owner=owner;this.length=length;this.pipe=pipe;}
        /** Nonblocking abort from any thread: blocked operations fail. */
        void abort(){
            if(pipe!=null)pipe.fail(new IOException("exchange aborted"));
            CompletableFuture<?> f=future;if(f!=null)f.cancel(true);
            InputStream b=body;if(b!=null)Thread.ofVirtual().start(()->{try{b.close();}catch(IOException e){}});
            HttpClient c=client;if(c!=null)c.shutdownNow();
        }
        void release(){
            if(closed)return;closed=true;abort();
            if(boundary!=null)boundary.close();
            owner.disposers.remove(dispose);
            HttpClient c=client;if(c!=null)Thread.ofVirtual().start(c::close);
        }
    }

    interface Work {Object[] run()throws Exception;}

    /** Runs work on a virtual thread; its wire result starts with an error message or null. */
    static void op(TaskSpawn.Task task,Exchange ex,Object[] zero,Work work,Function<Object[],Object[]> decode){
        if(ex.owner!=TaskSpawn.sched)throw new TaskSpawn.HostFault("HTTP exchange belongs to another owner");
        TaskSpawn.HostToken token=TaskSpawn.sched.registerHost(task,ex::abort,()->{},
            ()->{Box e=ex.boundary.error();if(e==null)return null;Object[] r=zero.clone();r[r.length-1]=e;return r;},
            values->{Box e=ex.boundary.error();if(e!=null){Object[] r=zero.clone();r[r.length-1]=e;return r;}return values[0]!=null?Native.failure(zero,(String)values[0]):decode.apply(values);});
        Thread worker;
        try{
            worker=Thread.ofVirtual().name("goalchemy-http").unstarted(()->{
                Object[] wire;TaskSpawn.HostFault fault=null;
                try{wire=work.run();}
                catch(IOException|InterruptedException|java.util.concurrent.ExecutionException|java.util.concurrent.CancellationException e){wire=new Object[]{FAILED};}
                catch(Throwable e){wire=new Object[0];fault=new TaskSpawn.HostFault("HTTP exchange adapter: "+e);}
                token.complete(wire,fault);
            });
            worker.start();
        }catch(Throwable e){token.complete(new Object[0],new TaskSpawn.HostFault("HTTP submission: "+e));token.acknowledgeCleanup();return;}
        Native.acknowledgeAfter(worker,token);
    }

    /** Sorted canonical headers, without Transfer-Encoding and Connection: close, as Go reports them. */
    static String[] headers(HttpHeaders raw)throws IOException {
        TreeMap<String,List<String>> exposed=new TreeMap<>();int total=0;
        for(var e:raw.map().entrySet()){
            String name=LibHttpDo.canonical(e.getKey());
            if(name.equals(":status")||name.equals("Transfer-Encoding")||name.equals("Connection")&&e.getValue().stream().anyMatch(v->Arrays.stream(v.split(",")).anyMatch(t->t.trim().equalsIgnoreCase("close"))))continue;
            for(String v:e.getValue()){total+=name.length()+v.getBytes(StandardCharsets.ISO_8859_1).length+4;if(total>65536)throw new IOException("response headers exceed limit");}
            exposed.put(name,e.getValue());
        }
        ArrayList<String> pairs=new ArrayList<>();for(var e:exposed.entrySet())for(String v:e.getValue()){pairs.add(e.getKey());pairs.add(Library.binaryString(v));}
        return pairs.toArray(String[]::new);
    }

    public static void libHttpExchangeClose(TaskSpawn.Task task,Exchange ex){if(ex!=null)ex.release();task.rv=new Object[0];}
}
