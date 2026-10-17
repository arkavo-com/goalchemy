// Shared portable runtime suite: executed unchanged by Node and a real browser.
import { makeChan, type Waiter } from "../runtime/chan_make.ts";
import { chanSend } from "../runtime/chan_send.ts";
import { chanRecv } from "../runtime/chan_recv.ts";
import { select } from "../runtime/select.ts";
import { chanClose } from "../runtime/chan_close.ts";
import { Mutex, stdSyncMutexLock } from "../runtime/std_sync_mutex_lock.ts";
import { stdSyncMutexUnlock } from "../runtime/std_sync_mutex_unlock.ts";
import { WaitGroup, stdSyncWaitgroupAdd } from "../runtime/std_sync_waitgroup_add.ts";
import { stdSyncWaitgroupWait } from "../runtime/std_sync_waitgroup_wait.ts";
import { Frame, Task, Scheduler, HostFault, runMainHost, ret, sched, spawn, sync, yieldTask, resetScheduler, HostToken } from "../runtime/task_spawn.ts";
import { portableHost, type RuntimeHost } from "../types/host.ts";
import { testOnlyFetch } from "./test_only_transport.ts";
import { stdContextContextErr } from "../runtime/std_context_err.ts";
import { writeStdout } from "../types/print.ts";
import { GoPanic, plainPanic } from "../types/panic.ts";
import { BACKGROUND, CONTEXT_CANCELED, CONTEXT_DEADLINE_EXCEEDED, cancelContext, onContextCancel } from "../runtime/std_context_err.ts";
import { stdContextWithCancel } from "../runtime/std_context_with_cancel.ts";
import { stdContextWithTimeout } from "../runtime/std_context_with_timeout.ts";
import { stdTimeSleep } from "../runtime/std_time_sleep.ts";
function check(v: unknown, msg: string): asserts v { if (!v) throw new Error(msg); }
function eq(a: unknown, b: unknown, msg: string): void { check(JSON.stringify(a) === JSON.stringify(b), msg + ': ' + JSON.stringify(a)); }
class Steps extends Frame {
  readonly steps: ((t: Task, f: Steps) => void)[];
  constructor(steps: ((t: Task, f: Steps) => void)[]) { super(); this.steps=steps; }
  step(t: Task): void { if (this.pc === this.steps.length) ret(t,this); else this.steps[this.pc++](t,this); }
}
function owner(): Scheduler {
  const s = new Scheduler(new Task(0, new Steps([])));
  s.hostMode = true;
  return s;
}
function host(events: string[] = []): RuntimeHost {
  return {...portableHost, stdout: b => events.push('out:'+Array.from(b).join(',')), stderr: b => events.push('err:'+Array.from(b).join(',')), fail: n => { events.push('exit:'+n); throw new Error('test exit '+n); }};
}
export async function runPortableSuite(): Promise<string[]> {
  const passed: string[] = [];
  // Registration/reentrancy, out-of-order FIFO, duplicate/stale/foreign records,
  // structured result snapshots, applied success and fault precedence.
  {
    const s = owner(), other = owner();
    const a = new Task(1,new Steps([])), b = new Task(2,new Steps([]));
    const ta = s.registerHost(a,()=>{}), tb = s.registerHost(b,()=>{});
    check(a.blocked && b.blocked,'park before callback');
    const bytes = new Uint8Array([1,255]), nested = [{a:[3]}];
    tb.complete([bytes,nested]); bytes[0]=9; nested[0].a[0]=9;
    tb.acknowledgeCleanup(); ta.complete(['a']); ta.acknowledgeCleanup();
    tb.complete(['duplicate']); other.drainHost(); check(other.runq.length===0,'foreign owner');
    s.drainHost(); eq(s.runq.toArray().map(t=>t.id),[2,1],'FIFO publication');
    eq(Array.from(b.rv[0] as Uint8Array),[1,255],'owned byte result'); eq(b.rv[1],[{a:[3]}],'owned nested result');
    for(let i=0;i<2000;i++) { tb.complete([]); tb.acknowledgeCleanup(); }
    check(s.mail.queue.length===0 && s.mail.cleaned.size===0,'late record bookkeeping bounded');
    await s.shutdown(); ta.complete([]); ta.acknowledgeCleanup(); check(s.mail.queue.length===0,'retired callbacks'); await other.shutdown();
    const c=owner(); let canceled=false;
    const tc = c.registerHost(new Task(3,new Steps([])),()=>{},()=>{},()=>canceled?['cancel']:null);
    tc.complete(['success']);tc.acknowledgeCleanup();canceled=true;c.drainHost();eq(c.runq.toArray()[0].rv,['cancel'],'cancel before apply');
    canceled=false; const td=c.registerHost(new Task(4,new Steps([])),()=>{},()=>{},()=>canceled?['cancel']:null);
    td.complete(['applied']);td.acknowledgeCleanup();c.drainHost();canceled=true;eq(c.runq.toArray()[1].rv,['applied'],'applied final');
    const te=c.registerHost(new Task(5,new Steps([])),()=>{},()=>{},()=>['cancel']);te.complete([],new HostFault('adapter'));te.acknowledgeCleanup();
    let fault=false;try{c.drainHost();}catch(e){fault=e instanceof HostFault;}check(fault,'fault beats cancel');await c.shutdown();
    passed.push('registration/order/snapshot/duplicates/owners/cancel/fault');
  }
  {
    const s = owner(), task = new Task(7,new Steps([]));
    let cleanups = 0;
    const valid = s.registerHost(task,()=>{},()=>{cleanups++;});
    const foreign = new HostToken(s.mail,valid.operation,task.id+1);
    foreign.complete(['foreign'],new HostFault('foreign task'));
    foreign.acknowledgeCleanup();
    s.drainHost();
    eq([s.runq.length,cleanups,s.operations.size,s.mail.queue.length,s.mail.cleaned.size],[0,0,1,0,0],'foreign result/fault/ACK leaves canonical pending');
    valid.complete(['canonical']);
    foreign.acknowledgeCleanup();
    s.drainHost();
    eq([s.runq.length,cleanups,s.operations.size,s.mail.queue.length,s.mail.cleaned.size],[0,0,1,1,0],'foreign ACK cannot release canonical result');
    valid.acknowledgeCleanup();
    s.drainHost();
    eq(task.rv,['canonical'],'canonical result after genuine cleanup');
    eq([s.runq.length,cleanups,s.operations.size,s.mail.queue.length,s.mail.cleaned.size],[1,1,0,0,0],'canonical cleanup and completion exactly once');
    foreign.complete(['late']); foreign.acknowledgeCleanup(); valid.complete(['duplicate']);valid.acknowledgeCleanup();
    s.drainHost();eq([s.runq.length,cleanups,s.mail.queue.length,s.mail.cleaned.size],[1,1,0,0],'late identity records remain bounded');
    await s.shutdown();
    passed.push('task identity on result/fault/cleanup ACK before canonical completion');
  }
  {
    let token!: HostToken, submitted=false, resumed=false, released=false;
    // A test-only disposable key resource proves acquisition retention; no crypto mapping.
    const key={material:new Uint8Array([7]),closed:false};let keySnapshot:Uint8Array|null=null;let acquisitions=0;
    const input=new Uint8Array([4,5]);let copied: Uint8Array | null=null;
    let settle!: ()=>void; const promise = new Promise<void>(r=>settle=r);
    let cleanup!: ()=>void;const cleanupGate=new Promise<void>(r=>cleanup=r);
    const p=runMainHost(new Steps([
      t=>{copied=input.slice();keySnapshot=key.material.slice();acquisitions++;token=sched.registerHost(t,()=>{},()=>{check(released,'cleanup acknowledged');});submitted=true;
        promise.then(()=>{check(copied?.[0]===4,'adapter retains copied input');check(keySnapshot?.[0]===7&&acquisitions===1,'retained test key snapshot');token.complete([copied]);return cleanupGate;}).then(()=>{copied=null;keySnapshot=null;acquisitions--;released=true;token.acknowledgeCleanup();});},
      t=>{resumed=true;eq(Array.from(t.rv[0] as Uint8Array),[4,5],'copied adapter input');},
    ]),host());
    await new Promise<void>(r=>setTimeout(r,0));check(submitted,'submitted');input[0]=99;key.closed=true;key.material[0]=99;settle();await new Promise<void>(r=>setTimeout(r,0));check(!resumed&&!released&&acquisitions===1,'wait for cleanup ack retaining key acquisition');cleanup();await p;check(resumed&&Number(acquisitions)===0&&keySnapshot===null,'pending wake after key release');
    passed.push('promise/wake/copied input/delayed cleanup');
  }
  {
    let canceledSignal!:()=>void; const canceledGate=new Promise<void>(r=>canceledSignal=r);
    let canceled=false, release!:()=>void, token!:HostToken, s!:Scheduler;
    const gate=new Promise<void>(r=>release=r);const events:string[]=[];
    const p=runMainHost(new Steps([()=>{
      s=sched;spawn(new Steps([t=>{token=sched.registerHost(t,()=>{canceled=true;canceledSignal();gate.then(()=>token.acknowledgeCleanup());});},()=>{throw new Error('background resumed after shutdown');}]));yieldTask(sched.cur);
    }]),host(events));
    await canceledGate;check(canceled,'shutdown cancels');let finished=false;p.then(()=>finished=true);await Promise.resolve();check(!finished,'await cleanup');release();await p;
    check(s.tasks.size===0&&s.runq.length===0&&s.timers.length===0&&s.operations.size===0,'shutdown drops roots');
    token.complete(['late']);token.acknowledgeCleanup();resetScheduler();check(sched!==s,'new generation');passed.push('shutdown/reset/late callbacks');
  }
  {
    let cancelSignal!:()=>void;const cancelGate=new Promise<void>(r=>cancelSignal=r);
    let token!:HostToken, release!:()=>void, cancel=false;const gate=new Promise<void>(r=>release=r);const events:string[]=[];
    const p=runMainHost(new Steps([()=>{spawn(new Steps([t=>{token=sched.registerHost(t,()=>{cancel=true;cancelSignal();gate.then(()=>token.acknowledgeCleanup());});}]));yieldTask(sched.cur);},()=>{throw plainPanic('fatal');}]),host(events));
    await cancelGate;check(cancel&&events.length===0,'fatal awaits cleanup before report');release();let failed=false;try{await p;}catch{failed=true;}check(failed&&events.at(-1)==='exit:2','fatal report after cleanup');passed.push('fatal cleanup before failure');
  }
  {
    let cancellation!:()=>void, release!:()=>void;const canceled=new Promise<void>(r=>cancellation=r),cleanup=new Promise<void>(r=>release=r);const events:string[]=[];
    const p=runMainHost(new Steps([
      t=>{spawn(new Steps([u=>{let token:HostToken;token=sched.registerHost(u,()=>{cancellation();cleanup.then(()=>token.acknowledgeCleanup());});}]));yieldTask(t);},
      ()=>stdSyncMutexUnlock(new Mutex()),
    ]),host(events));
    await canceled;check(events.length===0,'source fatal primitive waits for native cleanup');release();let failed=false;try{await p;}catch{failed=true;}check(failed&&events.at(-1)==='exit:2','source fatal primitive reports after cleanup');
    passed.push('source runtime fatal primitive cleanup before failure');
  }
  {
    const a:string[]=[], b:string[]=[];await runMainHost(sync(()=>{writeStdout('\x00\xff');return [];}),host(a));await runMainHost(sync(()=>{writeStdout('B');return [];}),host(b));eq(a,['out:0,255'],'first host output');eq(b,['out:66'],'second host output');
    let token!:HostToken;const p=runMainHost(new Steps([t=>{token=sched.registerHost(t,()=>{token.acknowledgeCleanup();});}]),host());await new Promise<void>(r=>setTimeout(r,0));const active=sched;
    let overlap=false;try{await runMainHost(sync(()=>[]),host());}catch(e){overlap=e instanceof HostFault;}check(overlap&&sched===active,'overlap reject before binding');let reset=false;try{resetScheduler();}catch(e){reset=e instanceof HostFault;}check(reset&&sched===active,'reset reject before binding');token.complete([]);token.acknowledgeCleanup();await p;passed.push('explicit host binding/overlap rejection');
  }
  {
    const start=performance.now();let ticks=0, ownerRef!:Scheduler;
    let parent: ReturnType<typeof stdContextWithTimeout>[0] | null=null, child: ReturnType<typeof stdContextWithTimeout>[0] | null=null;
    await runMainHost(new Steps([
      t=>{ownerRef=sched;
        spawn(new Steps([(u,f)=>{if(parent===null || parent.err===null){ticks++;f.pc=0;yieldTask(u);}else check(child?.err===CONTEXT_DEADLINE_EXCEEDED,'inherited cancellation');}]));yieldTask(t);},
      t=>{check(ticks>0,'worker progress barrier before deadline');[parent]=stdContextWithTimeout(BACKGROUND,8_000_000n);[child]=stdContextWithTimeout(parent,1_000_000_000n);check(child.deadline===parent.deadline,'earliest parent deadline');stdTimeSleep(t,20_000_000n);},
      ()=>{check(performance.now()-start>=15,'real sleep no fast-forward');check(ticks>0,'unrelated runnable progress');
        const [expired]=stdContextWithTimeout(BACKGROUND,0n);check(expired.err===CONTEXT_DEADLINE_EXCEEDED,'immediate nonpositive');
        const [p,stop]=stdContextWithCancel(BACKGROUND);
        for(let i=0;i<1000;i++){const [c,cc]=stdContextWithTimeout(p,1000000000n);const off=onContextCancel(c,()=>{});off();cc();}
        check(p.children.size===0&&sched.timers.length===0&&sched.disposers.size===1,'parent/timer/hook pruning');stop();
        const [huge, cancel]=stdContextWithTimeout(BACKGROUND,1n<<100n);check(huge.deadline===9223372036854775807n,'overflow saturation');cancel();},
    ]),host());check(ownerRef.tasks.size===0,'task roots cleared');passed.push('real sleep/runnable deadlines/inheritance/pruning/overflow');
  }
  {
    let clock=0;
    await runMainHost(sync(()=>{
      const [parent]=stdContextWithTimeout(BACKGROUND,100_000_000n);
      const [child]=stdContextWithTimeout(parent,1_000_000_000n);
      check(sched.timers.length===2&&sched.timers.every(t=>t.at===parent.deadline)&&child.deadline===parent.deadline,'absolute inherited timer anchoring without resampling drift');
      cancelContext(parent,CONTEXT_CANCELED);return [];
    }),{...host(),now:()=>clock++});
    passed.push('absolute timer registration controlled-clock anchoring');
  }
  {
    let now=0;
    await runMainHost(sync(()=>{
      const [p]=stdContextWithTimeout(BACKGROUND,10_000_000n);now=20;
      const [child]=stdContextWithCancel(p);check(child.err===CONTEXT_DEADLINE_EXCEEDED && stdContextContextErr(p)===CONTEXT_DEADLINE_EXCEEDED,'expired parent observed at creation/Err');
      let nil=false;try{stdContextWithCancel(null as any);}catch(e){nil=e instanceof GoPanic;}check(nil,'nil context source panic');
      onContextCancel(BACKGROUND,()=>{throw new Error('Background canceled');});check(BACKGROUND.hooks.size===0,'never-canceled Background retains no hook');
      const [c]=stdContextWithCancel(BACKGROUND);let called=0;onContextCancel(c,()=>{throw new Error('hook');});onContextCancel(c,()=>{called++;});
      let fault=false;try{cancelContext(c,CONTEXT_CANCELED);}catch(e){fault=e instanceof HostFault;}check(fault&&called===1&&c.hooks.size===0&&sched.disposers.size===0,'throwing hooks prune exhaustively');return [];
    }),{...host(),now:()=>now});
    const s=owner();let cleaned=0;const a=new Task(1,new Steps([])),b=new Task(2,new Steps([]));
    const ta=s.registerHost(a,()=>ta.acknowledgeCleanup(),()=>{throw new Error("operation cleanup");}),tb=s.registerHost(b,()=>tb.acknowledgeCleanup());
    a.cleanup=()=>{throw new Error('task cleanup');};b.cleanup=()=>{cleaned++;};
    let failed=false;try{await s.shutdown();}catch{failed=true;}check(failed&&cleaned===1&&s.tasks.size===0&&a.frame===null&&b.frame===null,'throwing cleanup releases all roots');
    let rejected=false;try{await runMainHost(new Steps([t=>{const token=sched.registerHost(t,()=>{});sched.launchHost(token,()=>Promise.reject(new Error('rejection')));}]),host());}catch(e){rejected=e instanceof HostFault;}check(rejected,'consumed Promise rejection surfaces host fault');
    await runMainHost(sync(()=>[]),host());passed.push('nil/expired contexts/hook faults/exhaustive cleanup/consumed rejection');
  }
  {
    let parent!:ReturnType<typeof stdContextWithCancel>[0], request!:typeof parent, canceled=false;
    await runMainHost(new Steps([
      t=>{[parent]=stdContextWithCancel(BACKGROUND);[request]=stdContextWithTimeout(parent,5_000_000n);stdTimeSleep(t,15_000_000n);},
      t=>{check(request.err===CONTEXT_DEADLINE_EXCEEDED&&parent.err===null,'request-only timeout anchored before submission');
        const off=onContextCancel(request,()=>{canceled=true;});const token=sched.registerHost(t,()=>{},off,()=>request.err?["deadline"]:null);
        check(canceled,'expired request cancels before native submission');token.complete(['ordinary']);token.acknowledgeCleanup();},
      t=>{eq(t.rv,['deadline'],'anchored deadline result');cancelContext(parent,CONTEXT_CANCELED);},
    ]),host());passed.push('request-only absolute timeout before submission');
  }
  {
    const sending=makeChan(0), receiving=makeChan(0), chosen=makeChan(0), m=new Mutex(), wg=new WaitGroup();
    m.locked=true;wg.n=1n;
    let stale:Waiter[]=[];let blockedTasks:Task[]=[];let old!:Scheduler;
    await runMainHost(new Steps([
      t=>{old=sched;
        const blockers:((t:Task)=>void)[]=[
          u=>chanSend(u,sending,new Uint8Array(1024*1024)),u=>chanRecv(u,receiving),
          u=>select(u,[{ch:chosen,send:true,val:new Uint8Array(1024*1024)},{ch:receiving,send:false}],false),
          u=>stdSyncMutexLock(u,m),u=>stdSyncWaitgroupWait(u,wg),u=>chanRecv(u,null),
          u=>select(u,[],false),u=>stdTimeSleep(u,1_000_000_000n),
        ];
        for(const block of blockers)spawn(new Steps([block,()=>{throw new Error('retired source task resumed');}]));yieldTask(t);},
      ()=>{check(sending.sendq.length===1&&receiving.recvq.length===2&&chosen.sendq.length===1&&m.waiters.length===1&&wg.waiters.length===1,'all source blocker registrations installed');stale=[...sending.sendq.toArray(),...receiving.recvq.toArray(),...chosen.sendq.toArray()];blockedTasks=stale.map(w=>w.task!);},
    ]),host());
    check(sending.sendq.length===0&&receiving.recvq.length===0&&chosen.sendq.length===0&&m.waiters.length===0&&wg.waiters.length===0,'blocked channel/select/mutex/WaitGroup roots removed');
    check(stale.every(w=>w.task===null&&w.val===undefined)&&old.tasks.size===0&&old.timers.length===0,'retained payload/tasks/nil/select/sleep roots released');
    await runMainHost(sync(()=>{
      for(const w of stale){w.recvDone(new Uint8Array(1024),true);w.sendDone(false);}
      chanClose(receiving);stdSyncMutexUnlock(m);stdSyncWaitgroupAdd(wg,-1n);
      check(blockedTasks.every(t=>t.frame===null&&t.done&&t.rv.length===0&&t.resumePanic===null),'stale callbacks do not refill result/panic roots');
      check(sched.runq.length===0&&old.runq.length===0,'stale waiters cannot ready another generation');return [];
    }),host());
    passed.push('blocked channel/select/nil/sleep/mutex/WaitGroup roots and stale source callbacks');
  }
  {
    let resumed=0,cleanups=0, s!:Scheduler;
    await runMainHost(new Steps([
      ()=>{s=sched;for(let i=0;i<256;i++)spawn(sync(()=>[]));yieldTask(sched.cur);},
      ()=>{check(sched.tasks.size===1,'completed tasks removed');
        for(let i=0;i<256;i++)spawn(new Steps([t=>{const tok=sched.registerHost(t,()=>{},()=>{cleanups++;});Promise.resolve().then(()=>{tok.complete([1]);tok.acknowledgeCleanup();});},t=>{check(t.rv[0]===1,'stress result');resumed++;}]));yieldTask(sched.cur);},
      (t,f)=>{if(resumed!==256){f.pc--;yieldTask(t);}else check(cleanups===256&&sched.operations.size===0&&sched.tasks.size===1,'256 actual worker resumes and cleanup');},
    ]),host());check(resumed===256&&cleanups===256&&s.runq.length===0&&s.tasks.size===0,'stress complete');passed.push('bounded task and operation stress: 256 resumed/cleaned');
  }
  return passed;
}

export async function runTestOnlyFetch(url: string): Promise<void> {
  let progress=false;
  await runMainHost(new Steps([t=>{
    spawn(sync(()=>{progress=true;return [];}));
    testOnlyFetch(t,url,new Uint8Array([0,255,128,3]));
  }, t=>{check(progress,'source progressed during genuine I/O');eq(Array.from(t.rv[0] as Uint8Array),[0,255,128,3],'genuine independent fetch echo');}]),host());
}
