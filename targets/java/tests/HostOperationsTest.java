package rt;

import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicReference;

/** Native JVM tests. HTTP adapters live only in integration test output. */
public final class HostOperationsTest {
    static void check(boolean value,String message) { if(!value) throw new AssertionError(message); }
    static void await(CountDownLatch gate) {
        try { check(gate.await(10,TimeUnit.SECONDS),"gate timed out"); } catch(InterruptedException e) { throw new AssertionError(e); }
    }
    static TaskSpawn.Frame idle() { return TaskSpawn.sync(()->new Object[0]); }
    static TaskSpawn.Scheduler owner() {
        TaskSpawn.resetScheduler();
        TaskSpawn.sched.hostMode=true;
        return TaskSpawn.sched;
    }
    static void wireAndMailbox() {
        var s=owner(); var main=s.main;
        main.frame=idle();
        var first=s.registerHost(main,()->{});
        check(main.blocked,"park before callback");
        byte[] bytes={0,(byte)255,(byte)128};
        Object[] nested={bytes,List.of(Map.of("bytes",bytes))};
        first.complete(nested); bytes[1]=1; first.complete(new Object[]{"duplicate"});
        s.drainHost();check(s.runq.isEmpty(),"no apply before cleanup ACK");
        first.acknowledgeCleanup();s.drainHost();
        check(((byte[])main.rv[0])[1]==(byte)255,"copied unsigned byte result");
        check(((byte[])((Map<?,?>)((List<?>)main.rv[1]).get(0)).get("bytes"))[2]==(byte)128,"nested copy");
        main.rv=new Object[0]; s.runq.clear();
        var a=new TaskSpawn.Task(1,idle());a.owner=s;var b=new TaskSpawn.Task(2,idle());b.owner=s;
        var ta=s.registerHost(a,()->{});var tb=s.registerHost(b,()->{});
        tb.complete(new Object[]{2L});ta.complete(new Object[]{1L});tb.acknowledgeCleanup();ta.acknowledgeCleanup();s.drainHost();
        check(s.runq.poll()==b && s.runq.poll()==a,"out-of-order completion FIFO");
        for(int i=0;i<10000;i++){first.complete(new Object[]{i});first.acknowledgeCleanup();}
        check(s.mail.records.isEmpty() && s.mail.cleaned.isEmpty() && s.mail.live.isEmpty(),"bounded stale completion/ACKs");
        s.shutdown();
        var other=owner(); first.complete(new Object[]{3L});first.acknowledgeCleanup();other.drainHost();
        check(other.runq.isEmpty(),"foreign retired generation ignored");other.shutdown();
        var live=owner(); int[] cleaned={0};
        var valid=live.registerHost(live.main,()->{},()->cleaned[0]++,()->null);
        var foreign=new TaskSpawn.HostToken(live.mail,valid.operation,999);
        foreign.complete(new Object[]{99L},new TaskSpawn.HostFault("foreign fault"));
        foreign.acknowledgeCleanup();live.drainHost();
        check(live.operations.size()==1 && live.mail.records.isEmpty() && live.mail.cleaned.isEmpty(),"foreign task cannot consume or ACK live operation");
        valid.complete(new Object[]{7L});foreign.acknowledgeCleanup();live.drainHost();
        check(live.runq.isEmpty() && cleaned[0]==0,"foreign ACK cannot release valid resources");
        valid.acknowledgeCleanup();live.drainHost();
        check(live.runq.poll()==live.main && (Long)live.main.rv[0]==7L && cleaned[0]==1,"valid completion survives foreign publication and cleans exactly once");
        live.shutdown();check(cleaned[0]==1,"no duplicate cleanup after valid retirement");
    }
    static void precedence() {
        for(int mode=0;mode<3;mode++) {
            var s=owner();var t=s.main;t.frame=idle();
            var context=(StdContextErr.Context)StdContextWithCancel.stdContextWithCancel(StdContextErr.BACKGROUND)[0];
            AtomicInteger nativeCancel=new AtomicInteger();
            Runnable detach=StdContextErr.onCancel(context,nativeCancel::incrementAndGet);
            var token=s.registerHost(t,()->{},detach,()->context.err==null?null:new Object[]{null,context.err});
            token.complete(new Object[]{"success"},mode==2?new TaskSpawn.HostFault("adapter fault"):null);token.acknowledgeCleanup();
            if(mode!=1) StdContextErr.cancel(context,StdContextErr.CONTEXT_CANCELED);
            try {
                s.drainHost();
                check(mode!=2,"fault must bypass cancellation");
                if(mode==0)check(t.rv[1]==StdContextErr.CONTEXT_CANCELED,"cancellation before apply wins");
                else {StdContextErr.cancel(context,StdContextErr.CONTEXT_CANCELED);check(t.rv[0].equals("success"),"committed success final");}
            }catch(TaskSpawn.HostFault e){check(mode==2 && e.getMessage().equals("adapter fault"),"fault precedence");}
            check(context.hooks.isEmpty(),"hook detached");s.shutdown();
        }
        var s=owner(); var token=s.registerHost(s.main,()->{},()->{},()->null,rv->{throw Panics.nilDeref();});
        token.complete(new Object[0]);token.acknowledgeCleanup();
        try{s.drainHost();throw new AssertionError("decoder fault escaped");}catch(TaskSpawn.HostFault expected){check(expected.getMessage().contains("driver decode"),"decoder fault category");}finally{s.shutdown();}
    }
    static void clocksAndContexts() {
        TaskSpawn.resetScheduler(); var base=TaskSpawn.sched;
        var parent=(StdContextErr.Context)StdContextWithTimeout.stdContextWithTimeout(StdContextErr.BACKGROUND,10)[0];
        base.clock=10;
        var child=(StdContextErr.Context)StdContextWithTimeout.stdContextWithTimeout(parent,100)[0];
        check(child.err==StdContextErr.CONTEXT_DEADLINE_EXCEEDED && parent.err==child.err,"expired parent immediately observed before timer dispatch");
        check(base.timers.isEmpty() && base.disposers.isEmpty(),"expired child/parent registrations pruned");
        var zero=(StdContextErr.Context)StdContextWithTimeout.stdContextWithTimeout(StdContextErr.BACKGROUND,-1)[0];
        check(zero.err==StdContextErr.CONTEXT_DEADLINE_EXCEEDED,"nonpositive timeout immediate");
        base.clock=Long.MAX_VALUE-3;
        var saturated=(StdContextErr.Context)StdContextWithTimeout.stdContextWithTimeout(StdContextErr.BACKGROUND,99)[0];
        check(saturated.deadline==Long.MAX_VALUE,"positive overflow saturates");
        StdContextErr.cancel(saturated,StdContextErr.CONTEXT_CANCELED);
        base.shutdown();
        long[] nativeNow={700};var task=new TaskSpawn.Task(0,idle());var s=new TaskSpawn.Scheduler(task,()->nativeNow[0]);TaskSpawn.sched=s;s.hostMode=true;
        List<Integer> order=new ArrayList<>();s.addTimerAt(20,null,()->order.add(3));s.addTimerAt(10,null,()->order.add(1));s.addTimerAt(10,null,()->order.add(2));
        s.ready(task);nativeNow[0]=710;
        check(s.nextHost()==task && order.equals(List.of(1,2)),"due timers dispatch with runnable task, deadline then sequence");
        nativeNow[0]=709;check(s.now()==10,"monotonic measurement does not regress");
        var ctx=(StdContextErr.Context)StdContextWithTimeout.stdContextWithTimeout(StdContextErr.BACKGROUND,20)[0];
        check(ctx.deadline==30 && s.timers.stream().anyMatch(timer->timer.at()==30),"absolute context registration no resampling");
        nativeNow[0]=725;AtomicInteger cancel=new AtomicInteger();
        var request=new StdContextErr.Boundary(StdContextErr.BACKGROUND,5,cancel::incrementAndGet);
        nativeNow[0]=731;check(request.error()==StdContextErr.CONTEXT_DEADLINE_EXCEEDED,"request timeout consumes delayed submission");
        check(StdContextErr.BACKGROUND.err==null && cancel.get()==1,"request only expiry leaves parent untouched");request.close();
        s.shutdown();
        long[] drifting={900};var drift=new TaskSpawn.Scheduler(new TaskSpawn.Task(0,idle()),()->drifting[0]++);TaskSpawn.sched=drift;drift.hostMode=true;
        var anchored=(StdContextErr.Context)StdContextWithTimeout.stdContextWithTimeout(StdContextErr.BACKGROUND,10)[0];
        check(anchored.deadline==11 && drift.timers.get(0).at()==anchored.deadline,"absolute timer unchanged across distinct now samples");
        drift.shutdown();
        for(int i=0;i<1000;i++){
            TaskSpawn.resetScheduler();var owner=TaskSpawn.sched;
            var root=(StdContextErr.Context)StdContextWithCancel.stdContextWithCancel(StdContextErr.BACKGROUND)[0];
            for(int n=0;n<5;n++){
                var c=(StdContextErr.Context)StdContextWithTimeout.stdContextWithTimeout(root,Long.MAX_VALUE)[0];
                var remove=StdContextErr.onCancel(c,()->{});remove.run();StdContextErr.cancel(c,StdContextErr.CONTEXT_CANCELED);
            }
            check(root.children.isEmpty()&&owner.timers.isEmpty(),"repeated child/timer/hook pruning");
            StdContextErr.cancel(root,StdContextErr.CONTEXT_CANCELED);check(owner.disposers.isEmpty(),"repeated disposer pruning");owner.shutdown();
        }
        TaskSpawn.resetScheduler();
        try{StdContextErr.stdContextContextErr(null);throw new AssertionError("nil context accepted");}catch(GoPanic expected){}
        var foreign=(StdContextErr.Context)StdContextWithCancel.stdContextWithCancel(StdContextErr.BACKGROUND)[0];TaskSpawn.resetScheduler();
        try{StdContextErr.observe(foreign);throw new AssertionError("foreign context accepted");}catch(TaskSpawn.HostFault expected){}
    }
    static void blockedRoots() {
        for(String mode:List.of("send","recv","select","mutex","waitgroup","nil-send","nil-recv","empty-select","sleep")) {
            var s=owner();var t=s.main;t.frame=idle();
            var ch=ChanMake.makeChan(0,()->null);var second=ChanMake.makeChan(0,()->null);
            var mutex=new StdSyncMutexLock.Mutex();mutex.locked=true;
            var wg=new StdSyncWaitgroupAdd.WaitGroup();wg.n=1;
            Object payload=new byte[1048576];ChanMake.Waiter stale=null;
            switch(mode){
                case "send" -> {ChanSend.chanSend(t,ch,payload);stale=ch.sendq.peek();}
                case "recv" -> {ChanRecv.chanRecv(t,ch);stale=ch.recvq.peek();}
                case "select" -> {Select.select(t,false,Select.scase(ch,true,payload),Select.scase(second,false,null));stale=ch.sendq.peek();}
                case "mutex" -> StdSyncMutexLock.stdSyncMutexLock(t,mutex);
                case "waitgroup" -> StdSyncWaitgroupWait.stdSyncWaitgroupWait(t,wg);
                case "nil-send" -> ChanSend.chanSend(t,null,payload);
                case "nil-recv" -> ChanRecv.chanRecv(t,null);
                case "empty-select" -> Select.select(t,false);
                case "sleep" -> StdTimeSleep.stdTimeSleep(t,Long.MAX_VALUE);
            }
            check(t.blocked,"registration installed before retirement: " + mode);
            s.shutdown();
            check(ch.sendq.isEmpty()&&ch.recvq.isEmpty()&&second.recvq.isEmpty()&&mutex.waiters.isEmpty()&&wg.waiters.isEmpty(),mode+" retained external queues empty");
            check(t.frame==null&&t.rv.length==0&&t.cleanup==null&&t.curPanic==null&&s.tasks.isEmpty(),mode+" frame roots retired");
            TaskSpawn.resetScheduler();
            if(stale!=null){check(stale.val==null&&stale.task==null,"held stale task and send payload cleared");stale.recvDone(99L,true);stale.sendDone(false);}
            ChanClose.chanClose(ch);StdSyncMutexUnlock.stdSyncMutexUnlock(mutex);StdSyncWaitgroupAdd.stdSyncWaitgroupAdd(wg,-1);
            check(TaskSpawn.sched.runq.isEmpty() && t.frame==null && t.rv.length==0 && t.resumePanic==null,"stale waiter callbacks rejected");
        }
        var ch=ChanMake.makeChan(2,()->null);
        TaskSpawn.runIsolated(t->{ChanSend.chanSend(t,ch,null);ChanSend.chanSend(t,ch,7L);});
        Object[] result=TaskSpawn.runIsolated(t->ChanRecv.chanRecv(t,ch));check(result[0]==null && (Boolean)result[1],"nil channel value FIFO");
        result=TaskSpawn.runIsolated(t->ChanRecv.chanRecv(t,ch));check(result[0].equals(7L),"value after nil FIFO");
    }
    static void cleanupFaults() {
        var s=owner();AtomicInteger cleanup=new AtomicInteger();
        var a=s.main;var b=new TaskSpawn.Task(1,idle());b.owner=s;
        var ta=s.registerHost(a,()->{throw new RuntimeException("cancel fault");},()->{cleanup.incrementAndGet();throw new RuntimeException("cleanup fault");},()->null);
        var tb=s.registerHost(b,()->{},cleanup::incrementAndGet,()->null);
        ta.acknowledgeCleanup();tb.acknowledgeCleanup();
        s.disposers.add(()->{cleanup.incrementAndGet();throw new RuntimeException("dispose fault");});s.disposers.add(cleanup::incrementAndGet);
        a.cleanup=()->{cleanup.incrementAndGet();throw new RuntimeException("task cleanup fault");};b.cleanup=cleanup::incrementAndGet;
        try{s.shutdown();throw new AssertionError("cleanup error missing");}catch(TaskSpawn.HostFault expected){}
        check(cleanup.get()==6 && s.tasks.isEmpty()&&s.disposers.isEmpty()&&s.operations.isEmpty()&&s.mail.cleaned.isEmpty(),"exhaustive cleanup after multiple faults");
        TaskSpawn.resetScheduler();var context=(StdContextErr.Context)StdContextWithCancel.stdContextWithCancel(StdContextErr.BACKGROUND)[0];
        AtomicInteger hooks=new AtomicInteger();StdContextErr.onCancel(context,()->{throw new RuntimeException("hook fault");});StdContextErr.onCancel(context,hooks::incrementAndGet);
        try{StdContextErr.cancel(context,StdContextErr.CONTEXT_CANCELED);throw new AssertionError("hook failure missing");}catch(TaskSpawn.HostFault expected){}
        check(hooks.get()==1&&context.hooks.isEmpty()&&context.children.isEmpty(),"exhaustive context hooks");
    }
    static void realDriveAndOverlap() throws Exception {
        CountDownLatch started=new CountDownLatch(1),release=new CountDownLatch(1);AtomicInteger progress=new AtomicInteger(),resumed=new AtomicInteger();
        AtomicReference<Throwable> failure=new AtomicReference<>();
        Thread driver=new Thread(()->{try{
            TaskSpawn.runMainHost(new TaskSpawn.Frame(){
                public void step(TaskSpawn.Task t){
                    if(pc++==0){
                        TaskSpawn.spawn(TaskSpawn.sync(()->{progress.incrementAndGet();return new Object[0];}));
                        var token=TaskSpawn.sched.registerHost(t,release::countDown);
                        new Thread(()->{started.countDown();await(release);token.complete(new Object[]{42L});token.acknowledgeCleanup();}).start();
                    }else{check(t.rv[0].equals(42L),"real mailbox result");resumed.incrementAndGet();TaskSpawn.ret(t,this);}
                }
            });
        }catch(Throwable e){failure.set(e);}});
        driver.start();await(started);
        for(int i=0;i<20;i++) {
            try{TaskSpawn.runMainHost(idle());throw new AssertionError("overlap accepted");}catch(TaskSpawn.HostFault expected){}
            try{TaskSpawn.resetScheduler();throw new AssertionError("overlap reset accepted");}catch(TaskSpawn.HostFault expected){}
            try{TaskSpawn.runIsolated(t->{});throw new AssertionError("overlap harness accepted");}catch(TaskSpawn.HostFault expected){}
        }
        release.countDown();driver.join(10000);check(!driver.isAlive()&&failure.get()==null&&progress.get()==1&&resumed.get()==1,"pending mailbox wake and source progress");
        check(Program.panicState.get()!=null,"usable panic binding after retirement");
        AtomicInteger cleaned=new AtomicInteger(),asserted=new AtomicInteger();
        TaskSpawn.runMainHost(new TaskSpawn.Frame(){int left=256;
            public void step(TaskSpawn.Task t){
                if(pc==0){pc=1;var token=TaskSpawn.sched.registerHost(t,()->{},cleaned::incrementAndGet,()->null);
                    int expected=left;TaskSpawn.sched.launchHost(token,()->CompletableFuture.completedFuture(new Object[]{(long)expected}));
                }else{check(t.rv[0].equals((long)left),"each bounded stress result");asserted.incrementAndGet();if(--left==0)TaskSpawn.ret(t,this);else{pc=0;TaskSpawn.yieldTask(t);}}
            }
        });
        check(cleaned.get()==256&&asserted.get()==256,"stress asserts all results and cleanup before main return");
        for(int mode=0;mode<3;mode++){
            final int kind=mode;
            try{TaskSpawn.runMainHost(new TaskSpawn.Frame(){public void step(TaskSpawn.Task t){
                var token=TaskSpawn.sched.registerHost(t,()->{});
                if(kind==0)TaskSpawn.sched.launchHost(token,()->CompletableFuture.failedFuture(new RuntimeException("unexpected")));
                if(kind==1)TaskSpawn.sched.launchHost(token,()->{throw new RuntimeException("submission");});
                if(kind==2){token.complete(new Object[]{new Box(Program.STRING_TYPE,"source descriptor")});token.acknowledgeCleanup();}
            }});throw new AssertionError("host fault missing");}catch(TaskSpawn.HostFault expected){}
            check(TaskSpawn.sched.tasks.isEmpty()&&TaskSpawn.sched.operations.isEmpty(),"future fault cleanup");
        }
        // Context deadline dispatch with another source task always runnable.
        TaskSpawn.runMainHost(new TaskSpawn.Frame(){StdContextErr.Context context;
            public void step(TaskSpawn.Task t){
                if(pc++==0)context=(StdContextErr.Context)StdContextWithTimeout.stdContextWithTimeout(StdContextErr.BACKGROUND,1000000)[0];
                if(StdContextErr.stdContextContextErr(context)==null)TaskSpawn.yieldTask(t);else TaskSpawn.ret(t,this);
            }
        });
    }
    static void gatedRetirement() throws Exception {
        for(String mode:List.of("return","source-step-fault","fault","reset")) {
            CountDownLatch canceled=new CountDownLatch(1),release=new CountDownLatch(1),registered=new CountDownLatch(1);
            CompletableFuture<Void> transport=new CompletableFuture<>();
            AtomicInteger retained=new AtomicInteger(2),returned=new AtomicInteger(),resumed=new AtomicInteger();
            AtomicReference<Throwable> failure=new AtomicReference<>();
            Thread driver=new Thread(()->{
                try {
                    if(mode.equals("reset")) {
                        var s=owner();
                        s.disposers.add(()->retained.addAndGet(-2));
                        s.main.frame=idle();
                        registered.countDown();await(release);
                        // Virtual reset has no native work; its retained blocked roots
                        // are retired synchronously before installing the next owner.
                        TaskSpawn.resetScheduler();
                    } else {
                        TaskSpawn.runMainHost(new TaskSpawn.Frame(){public void step(TaskSpawn.Task t){
                            if(pc++!=0){resumed.incrementAndGet();throw new AssertionError("source resumed during retirement");}
                            var token=TaskSpawn.sched.registerHost(t,()->{transport.cancel(true);canceled.countDown();});
                            byte[] sourceInput={0,(byte)255};
                            final AtomicReference<byte[]> input=new AtomicReference<>((byte[])TaskSpawn.snapshot(sourceInput));
                            final AtomicReference<Object> keyLease=new AtomicReference<>(new Object()); sourceInput[1]=1;
                            new Thread(()->{
                                // Snapshot acquisition precedes worker submission;
                                // cancellation does not revoke these native leases.
                                await(canceled);await(release);
                                check(input.get()[1]==(byte)255&&keyLease.get()!=null,"native resources retained until cleanup");
                                input.set(null);keyLease.set(null);
                                retained.addAndGet(-2);token.acknowledgeCleanup();
                            }).start();
                            registered.countDown();
                            if(mode.equals("return"))TaskSpawn.ret(t,this);
                            else if(mode.equals("source-step-fault"))throw new TaskSpawn.HostFault("source-step host fault");
                            else {
                                var fault=TaskSpawn.sched.registerHost(t,()->{});
                                fault.complete(new Object[0],new TaskSpawn.HostFault("applicable fault"));fault.acknowledgeCleanup();
                            }
                        }});
                    }
                }catch(Throwable e){failure.set(e);}finally{returned.incrementAndGet();}
            });
            driver.start();await(registered);
            if(!mode.equals("reset")) {
                await(canceled);
                long limit=System.nanoTime()+TimeUnit.SECONDS.toNanos(5);boolean waiting=false;
                while(System.nanoTime()<limit) {
                    boolean mailbox=false,shutdown=false;
                    for(var frame:driver.getStackTrace()){
                        if(frame.getClassName().equals("rt.TaskSpawn$Mailbox")&&frame.getMethodName().equals("await"))mailbox=true;
                        if(frame.getClassName().equals("rt.TaskSpawn$Scheduler")&&frame.getMethodName().equals("shutdown"))shutdown=true;
                    }
                    if(mailbox&&shutdown&&driver.getState()==Thread.State.WAITING){waiting=true;break;}
                    Thread.yield();
                }
                check(waiting,"owner really awaiting withheld cleanup ACK: "+mode);
            }
            check(returned.get()==0&&retained.get()==2&&resumed.get()==0,"retirement withholds return/source progress before native cleanup: "+mode);
            if(!mode.equals("reset"))check(transport.isCancelled()&&transport.isDone(),"Future.cancel terminal state is not resource release/ACK");
            release.countDown();driver.join(10000);
            check(!driver.isAlive()&&returned.get()==1&&retained.get()==0&&resumed.get()==0,"retirement releases native leases and returns after ACK: "+mode);
            check(mode.equals("return")||mode.equals("reset") ? failure.get()==null : failure.get() instanceof TaskSpawn.HostFault,"retirement fault category");
        }
    }
    static void concurrentBacklog() {
        var executor=java.util.concurrent.Executors.newFixedThreadPool(8);
        AtomicInteger registered=new AtomicInteger(),cleaned=new AtomicInteger(),resumed=new AtomicInteger(),nativeCleaned=new AtomicInteger();
        CountDownLatch release=new CountDownLatch(1);
        try {
            TaskSpawn.runMainHost(new TaskSpawn.Frame(){final StdSyncWaitgroupAdd.WaitGroup group=new StdSyncWaitgroupAdd.WaitGroup();
                public void step(TaskSpawn.Task t){
                    if(pc++==0){
                        StdSyncWaitgroupAdd.stdSyncWaitgroupAdd(group,256);
                        for(int i=0;i<256;i++){
                            final int expected=i;
                            TaskSpawn.spawn(new TaskSpawn.Frame(){public void step(TaskSpawn.Task child){
                                if(pc++==0){
                                    var token=TaskSpawn.sched.registerHost(child,release::countDown,cleaned::incrementAndGet,()->null);
                                    TaskSpawn.sched.launchHost(token,()->CompletableFuture.supplyAsync(()->{
                                        await(release);nativeCleaned.incrementAndGet();return new Object[]{(long)expected};
                                    },executor));
                                    if(registered.incrementAndGet()==256){
                                        check(TaskSpawn.sched.operations.size()==256,"actual 256-operation concurrent backlog");release.countDown();
                                    }
                                }else{check(child.rv[0].equals((long)expected),"each concurrent result");resumed.incrementAndGet();StdSyncWaitgroupAdd.stdSyncWaitgroupAdd(group,-1);TaskSpawn.ret(child,this);}
                            }});
                        }
                        StdSyncWaitgroupWait.stdSyncWaitgroupWait(t,group);
                    }else{check(resumed.get()==256&&cleaned.get()==256&&nativeCleaned.get()==256,"all concurrent results/cleanup before main return");TaskSpawn.ret(t,this);}
                }
            });
        }finally{executor.shutdownNow();}
    }
    static void overflow() { overflow(); }
    static boolean externalGate;
    static void fatal(String mode,Path marker) {
        TaskSpawn.runMainHost(new TaskSpawn.Frame(){
            public void step(TaskSpawn.Task t){
                if(mode.equals("deadlock")) {
                    TaskSpawn.sched.disposers.add(()->write(marker,mode+":clean"));
                    ChanRecv.chanRecv(t,ChanMake.makeChan(0,()->null));
                    return;
                }
                CountDownLatch cancel=new CountDownLatch(1),started=new CountDownLatch(1);
                var token=TaskSpawn.sched.registerHost(t,cancel::countDown);
                new Thread(()->{started.countDown();await(cancel);
                    if(externalGate){System.out.println("native cancellation requested");System.out.flush();
                        try{check(new java.io.BufferedReader(new java.io.InputStreamReader(System.in)).readLine().equals("release"),"external cleanup gate");}catch(Exception e){throw new RuntimeException(e);}}
                    write(marker,mode+":clean");token.acknowledgeCleanup();}).start();await(started);
                if(mode.equals("mutex-fatal"))StdSyncMutexUnlock.stdSyncMutexUnlock(new StdSyncMutexLock.Mutex());
                if(mode.equals("panic"))throw new GoPanic(new Box(Program.STRING_TYPE,"source failure"));
                overflow();
            }
        });
    }
    static void write(Path marker,String text){try{Files.writeString(marker,text);}catch(Exception e){throw new RuntimeException(e);}}
    public static void main(String[] args) throws Exception {
        if(args.length!=0){externalGate=args.length>2;fatal(args[0],Path.of(args[1]));return;}
        wireAndMailbox();precedence();clocksAndContexts();blockedRoots();cleanupFaults();realDriveAndOverlap();gatedRetirement();concurrentBacklog();
        System.out.println("PASS JVM host registration/FIFO/ownership/ACK/cancellation/fault/contexts/clocks/blocked roots/overlap/stress/retirement");
    }
}
