// Reusable serialized, cancellable value-library boundary.
import { Frame, call, ret, driveLibrary, FatalPanic, HostFault, NativeCanceled, sync, type Task, type Scheduler } from './task_spawn.ts';
import { stdContextWithCancel } from './std_context_with_cancel.ts';
import { BACKGROUND, type Context } from './std_context_err.ts';
import { installCallbacks, type Callback } from '../types/callback.ts';
export type { Callback, Settlement } from '../types/callback.ts';
export interface CallOptions { signal?:AbortSignal; callbacks?:Readonly<Record<string,Callback>>; }
export class LibraryError extends Error {
 readonly kind:string;
 readonly fields:Readonly<Record<string,unknown>>;
 constructor(kind:string,fields:Record<string,unknown>={},cause?:unknown){super('goalchemy library: '+kind,cause===undefined?undefined:{cause});this.name='LibraryError';this.kind=kind;this.fields=fields;}
}
export function invalidBoundary():never{throw new LibraryError('invalid_argument');}
export function boundaryInt(v:unknown,bits:number,signed:boolean):bigint|number{
 const wide=bits===64;if(v===undefined)return wide?0n:0;
 if(wide?typeof v!=='bigint':typeof v!=='number'||!Number.isInteger(v))invalidBoundary();
 const n=wide?v as bigint:BigInt(v as number),lo=signed?-(1n<<BigInt(bits-1)):0n,hi=(1n<<BigInt(signed?bits-1:bits))-1n;if(n<lo||n>hi)invalidBoundary();return wide?n:Number(n);
}
export function boundaryString(v:unknown):string{if(v===undefined)return '';if(typeof v!=='string'||v.length>64<<20)invalidBoundary();for(let i=0;i<v.length;i++)if(v.charCodeAt(i)>255)invalidBoundary();return v;}
export function boundaryBool(v:unknown):boolean{if(v===undefined)return false;if(typeof v!=='boolean')invalidBoundary();return v;}
export function boundaryObject(v:unknown):Record<string,unknown>{if(v===undefined)return {};if(v===null||typeof v!=='object'||Array.isArray(v))invalidBoundary();return v as Record<string,unknown>;}
export function boundaryArray(v:unknown):unknown[]|null{if(v===undefined||v===null)return null;if(!Array.isArray(v)||v.length>64<<20)invalidBoundary();return v;}
export function boundaryBytes(v:unknown):Uint8Array|null{if(v===undefined||v===null)return null;if(!(v instanceof Uint8Array)||v.length>64<<20)invalidBoundary();return new Uint8Array(v);}
interface Waiter { resolve:(release:()=>void)=>void;reject:(e:unknown)=>void;signal?:AbortSignal;abort:()=>void; }
let reserved=false;const queue:Waiter[]=[];
function release():void {for(;;){const w=queue.shift();if(!w){reserved=false;return;}w.signal?.removeEventListener('abort',w.abort);if(w.signal?.aborted){w.reject(new LibraryError('canceled',{},w.signal.reason));continue;}w.resolve(release);return;}}
function acquire(signal?:AbortSignal):Promise<()=>void>{if(signal?.aborted)return Promise.reject(new LibraryError('canceled',{},signal.reason));if(!reserved){reserved=true;return Promise.resolve(release);}return new Promise((resolve,reject)=>{const w:Waiter={resolve,reject,signal,abort:()=>{const i=queue.indexOf(w);if(i>=0){queue.splice(i,1);reject(new LibraryError('canceled',{},signal?.reason));}}};queue.push(w);signal?.addEventListener('abort',w.abort,{once:true});});}
export function snapshotOptions(options:CallOptions):CallOptions{
 if(options===null||typeof options!=='object'||(options.signal!==undefined&&!(options.signal instanceof AbortSignal)))invalidBoundary();const callbacks:Record<string,Callback>=Object.create(null);
 if(options.callbacks!==undefined){for(const [name,fn] of Object.entries(options.callbacks)){if(!name||name.length>128||typeof fn!=='function')invalidBoundary();callbacks[name]=fn;}}
 return {signal:options.signal,callbacks:Object.freeze(callbacks)};
}
class Sequence extends Frame {
 readonly init:Frame;readonly operation:()=>Frame;res:unknown[]=[];
 constructor(init:Frame,operation:()=>Frame){super();this.init=init;this.operation=operation;}
 step(t:Task):void{if(this.pc===0){this.pc=1;call(t,this.init);return;}if(this.pc===1){this.pc=2;call(t,this.operation());return;}this.res=t.rv;ret(t,this);}
 results():unknown[]{return this.res;}
}
export function librarySequence(init:Frame,operation:()=>Frame):Frame{return new Sequence(init,operation);}
export async function runLibrary(options:CallOptions,entry:(ctx:Context)=>Frame,reset:()=>void,capture:(rv:unknown[])=>unknown[]):Promise<unknown[]>{
 return runLibraryCall(options,{reset},[],ctx=>entry(ctx),(_inst,rv)=>capture(rv));
}

/** Boundary warnings such as leaked handles and abandoned goroutines. */
export let libraryWarn:(message:string)=>void=message=>{console.warn('goalchemy: warning: '+message);};
export function setLibraryWarn(fn:(message:string)=>void):void{libraryWarn=fn;}

/** Saves, restores and zeroes the generated source globals. */
export interface LibraryState { reset:()=>void; save?:()=>unknown; load?:(saved:unknown)=>void; }

/** Persistent source state behind a family of handles; only touched while
 * holding the library reservation. */
export class Instance {
 globals:unknown=undefined;
 readonly domain:object={};
 readonly objects=new Map<unknown,{handle:Handle;ref:WeakRef<object>}>();
 readonly retain:(()=>void)[]=[];
 streams:Record<string,Callback>={};nextStream=0n;
 live=0;poisoned=false;retired=false;
 retireNow():void{if(this.retired)return;this.retired=true;this.globals=undefined;this.objects.clear();this.streams={};for(const r of this.retain.splice(0)){try{r();}catch{}}}
}

/** The host's reference to one source object of an instance. */
export class Handle {
 readonly inst:Instance;obj:unknown;readonly name:string;
 closed=false;released=false;
 readonly closing=new AbortController();
 active:AbortController|null=null;
 constructor(inst:Instance,obj:unknown,name:string){this.inst=inst;this.obj=obj;this.name=name;}
 release():void{if(this.released)return;this.released=true;const e=this.inst.objects.get(this.obj);if(e?.handle===this)this.inst.objects.delete(this.obj);this.inst.live--;this.obj=null;if(this.inst.live===0)this.inst.retireNow();}
}

const finalizers=new FinalizationRegistry<Handle>(h=>{
 if(h.closed)return;h.closed=true;
 void acquire().then(unlock=>{try{if(!h.released){libraryWarn(h.name+' handle was not closed; releasing it without running Close');h.release();}}finally{unlock();}});
});

/** Returns the host wrapper for obj, reusing the live wrapper of the same
 * source object so host identity follows source identity. */
export function wrap<W extends object>(inst:Instance,obj:unknown,name:string,make:(h:Handle)=>W):W|null{
 if(obj===null||obj===undefined)return null;
 const e=inst.objects.get(obj);if(e&&!e.handle.closed){const w=e.ref.deref();if(w)return w as W;}
 const h=new Handle(inst,obj,name),w=make(h);
 inst.objects.set(obj,{handle:h,ref:new WeakRef(w)});inst.live++;finalizers.register(w,h,h);return w;
}
export function obj<T>(h:Handle|null):T{return (h===null?null:h.obj) as T;}

export async function runLibraryCall(options:CallOptions,state:LibraryState,handles:(Handle|null)[],entry:(ctx:Context,fresh:boolean)=>Frame,capture:(inst:Instance,rv:unknown[])=>unknown[],closing:Handle|null=null,streams:(Callback|null)[]=[],ids:bigint[]=[]):Promise<unknown[]>{
 if(streamContext?.getStore()===true)throw new LibraryError('reentrant');
 const self=handles.find(h=>h!==null)??null;
 if(self!==null&&self!==closing&&self.closed)throw new LibraryError('closed');
 let signal=options.signal;let callAbort:AbortController|null=null;
 if(self!==null&&self!==closing){callAbort=new AbortController();const parts=[self.closing.signal,callAbort.signal];if(signal)parts.push(signal);signal=AbortSignal.any(parts);}
 let unlock:()=>void;
 try{unlock=await acquire(signal);}catch(e){if(self!==null&&self!==closing&&self.closing.signal.aborted)throw new LibraryError('closed');throw e;}
 let inst:Instance|null=null;
 try{
  for(const h of handles){if(h===null)continue;if(h.released||(h!==closing&&h.closed))throw new LibraryError('closed');if(inst!==null&&h.inst!==inst)throw new LibraryError('instance_mismatch');inst=h.inst;}
 }catch(e){unlock();throw e;}
 const fresh=inst===null;const owner=inst??new Instance();
 if(!fresh&&owner.poisoned){unlock();throw new LibraryError('poisoned');}
 if(self!==null&&self!==closing)self.active=callAbort;
 let cancel=()=>{};let keep=!fresh;
 try{
  streams.forEach((cb,i)=>{if(cb===null)return;owner.nextStream++;ids[i]=owner.nextStream;owner.streams['goalchemy.stream.'+owner.nextStream]=cb;});
  installCallbacks(Object.freeze({...(options.callbacks??{}),...owner.streams}));
  if(!fresh)state.load?.(owner.globals);
  return await driveLibrary(()=>{if(fresh)state.reset();const[ctx,stop]=stdContextWithCancel(BACKGROUND);cancel=stop;return entry(ctx,fresh);},rv=>{const out=capture(owner,rv);if(fresh&&owner.live>0)keep=true;return out;},signal,()=>cancel(),{domain:owner.domain,retire:(s:Scheduler,failure:unknown)=>{
   if(failure instanceof FatalPanic||(failure!==undefined&&!(failure instanceof NativeCanceled)&&!(failure instanceof LibraryError))){if(!fresh)owner.poisoned=true;return;}
   if(!keep)return;
   let left=0;for(const t of s.tasks)if(t!==s.main&&!t.done)left++;
   if(left>0)libraryWarn(left+' goroutine(s) still running when a handle call returned were abandoned');
   if(state.save)owner.globals=state.save();
   owner.retain.push(...s.retained);s.retained.clear();
  }});
 }catch(e){
  if(e instanceof NativeCanceled){if(self!==null&&self!==closing&&self.closing.signal.aborted)throw new LibraryError('canceled',{},'closed');throw new LibraryError('canceled',{},options.signal?.reason);}
  if(e instanceof LibraryError)throw e;if(e instanceof FatalPanic)throw new LibraryError('source_panic');if(e instanceof HostFault)throw new LibraryError('host_fault');throw new LibraryError('host_fault');
 }
 finally{
  state.reset();installCallbacks(Object.freeze({}));
  if(self!==null&&self!==closing)self.active=null;
  if(closing!==null)closing.release();
  else if(keep&&owner.live===0)owner.retireNow();
  unlock();
 }
}

/** Fails calls queued on h, cancels its active call, runs the source Close
 * when run is given and the instance is healthy, then releases h. */
export async function closeHandle(h:Handle,state:LibraryState,run:((ctx:Context,fresh:boolean)=>Frame)|null,failure:(err:unknown)=>LibraryError|null):Promise<void>{
 if(h.closed)return;h.closed=true;h.closing.abort();h.active?.abort();
 const entry=run??(()=>sync(()=>[null]));
 let rv:unknown[];
 try{rv=await runLibraryCall({},state,[h],entry,(_inst,rv)=>[failure(rv[0])],h);}
 catch(e){
  if(e instanceof LibraryError&&e.kind==='poisoned'){const unlock=await acquire();try{h.release();}finally{unlock();}return;}
  throw e;
 }
 if(rv[0]!==null)throw rv[0];
}

/** A host stream passed for a std/io parameter. Plain objects implement the
 * operations they support; read returns null at end of stream. Web and Node
 * streams, Blobs and Uint8Arrays are adapted. */
export interface HostStream {
 read?(size:number):Promise<Uint8Array|null>|Uint8Array|null;
 write?(data:Uint8Array):Promise<number|void>|number|void;
 seek?(offset:bigint,whence:number):Promise<bigint>|bigint;
 close?():Promise<void>|void;
}
interface NodeReadable { read(size?:number):unknown; once(event:string,fn:(...a:unknown[])=>void):unknown; removeListener(event:string,fn:(...a:unknown[])=>void):unknown; readableEnded?:boolean; }
interface NodeWritable { write(chunk:Uint8Array,cb:(err?:Error|null)=>void):boolean; end(cb?:()=>void):unknown; on(event:string,fn:(...a:unknown[])=>void):unknown; }
export type HostStreamInput=HostStream|ReadableStream<Uint8Array>|WritableStream<Uint8Array>|AsyncIterable<Uint8Array>|Blob|Uint8Array|NodeReadable|NodeWritable;
interface StreamContext { run<T>(store:boolean,fn:()=>T):T; getStore():boolean|undefined; }
let streamContext:StreamContext|null=null;
/** Node installs an AsyncLocalStorage so a host stream that calls back into
 * the library fails fast; portable hosts cannot detect re-entry. */
export function installStreamContext(context:StreamContext):void{streamContext=context;}
function chunked(next:()=>Promise<Uint8Array|null>):HostStream{
 let pending:Uint8Array|null=null;
 return {async read(size){
  while(pending===null||pending.length===0){pending=await next();if(pending===null)return null;}
  const out=pending.subarray(0,size);pending=pending.subarray(out.length);return out;
 }};
}
function bytesStream(data:Uint8Array):HostStream{
 let pos=0;
 return {read(size){if(pos>=data.length)return null;const out=data.subarray(pos,pos+size);pos+=out.length;return out;},
  seek(offset,whence){const base=whence===0?0n:whence===1?BigInt(pos):BigInt(data.length);const next=base+offset;if(next<0n)throw new Error('seek: negative position');pos=Number(next);return next;}};
}
function blobStream(blob:Blob):HostStream{
 let pos=0;
 return {async read(size){if(pos>=blob.size)return null;const out=new Uint8Array(await blob.slice(pos,pos+size).arrayBuffer());pos+=out.length;return out;},
  seek(offset,whence){const base=whence===0?0n:whence===1?BigInt(pos):BigInt(blob.size);const next=base+offset;if(next<0n)throw new Error('seek: negative position');pos=Number(next);return next;}};
}
function nodeReadable(r:NodeReadable):HostStream{
 return chunked(()=>new Promise<Uint8Array|null>((resolve,reject)=>{
  const attempt=():void=>{
   const chunk=r.read();
   if(chunk!==null&&chunk!==undefined){resolve(chunk instanceof Uint8Array?chunk:new TextEncoder().encode(String(chunk)));return;}
   if(r.readableEnded){resolve(null);return;}
   const done=():void=>{r.removeListener('readable',retry);r.removeListener('end',ended);r.removeListener('error',failed);};
   const retry=():void=>{done();attempt();};const ended=():void=>{done();resolve(null);};const failed=(e:unknown):void=>{done();reject(e);};
   r.once('readable',retry);r.once('end',ended);r.once('error',failed);
  };
  attempt();
 }));
}
function hostStreamOf(v:unknown):HostStream{
 if(v instanceof Uint8Array)return bytesStream(v);
 if(typeof Blob!=='undefined'&&v instanceof Blob)return blobStream(v);
 if(typeof ReadableStream!=='undefined'&&v instanceof ReadableStream){const reader=(v as ReadableStream<Uint8Array>).getReader();return {...chunked(async()=>{const r=await reader.read();return r.done?null:r.value;}),close(){return reader.cancel();}};}
 if(typeof WritableStream!=='undefined'&&v instanceof WritableStream){const writer=(v as WritableStream<Uint8Array>).getWriter();return {async write(d){await writer.write(d);return d.length;},close(){return writer.close();}};}
 const o=v as Record<string,unknown>;
 if(typeof o.on==='function'&&typeof o.end==='function'&&typeof o.write==='function'){const w=v as NodeWritable;return {write(d){return new Promise<number>((resolve,reject)=>{w.write(d,e=>e?reject(e):resolve(d.length));});},close(){return new Promise<void>(resolve=>{w.end(()=>resolve());});}};}
 if(typeof o.on==='function'&&typeof o.read==='function'&&typeof o.once==='function')return nodeReadable(v as NodeReadable);
 if(typeof o.read==='function'||typeof o.write==='function'||typeof o.seek==='function'||typeof o.close==='function')return v as HostStream;
 if(typeof (o as {[Symbol.asyncIterator]?:unknown})[Symbol.asyncIterator]==='function'){const it=(v as AsyncIterable<Uint8Array>)[Symbol.asyncIterator]();return chunked(async()=>{const r=await it.next();return r.done?null:r.value;});}
 invalidBoundary();
}
function streamInt(b:Uint8Array,at:number):bigint{let v=0n;for(let i=0;i<8;i++)v=(v<<8n)|BigInt(b[at+i]);return BigInt.asIntN(64,v);}
function streamBytes(v:bigint):Uint8Array{const b=new Uint8Array(9);let x=BigInt.asUintN(64,v);for(let i=8;i>=1;i--){b[i]=Number(x&255n);x>>=8n;}return b;}
async function serveStream(s:HostStream,req:Uint8Array):Promise<Uint8Array>{
 switch(req[0]){
 case 114:{const n=Number(streamInt(req,1));const data=await s.read!(n);if(data===null||data===undefined)return new Uint8Array([1]);if(!(data instanceof Uint8Array)||data.length>n)throw new Error('hostio: invalid read result');const out=new Uint8Array(1+data.length);out.set(data,1);return out;}
 case 119:{const data=req.slice(1);const n=await s.write!(data);return streamBytes(BigInt(typeof n==='number'?n:data.length));}
 case 115:return streamBytes(BigInt(await s.seek!(streamInt(req,1),req[9])));
 case 99:await s.close?.();return new Uint8Array([0]);
 }
 throw new Error('hostio: malformed stream request');
}
/** Adapts a host stream to the provider serving one std/io parameter; reads
 * and writes copy their buffers. Host end of stream becomes io.EOF. */
export function hostStream(v:unknown,kind:string):Callback|null{
 if(v===null||v===undefined)return null;
 const s=hostStreamOf(v);
 if((kind.includes('Read')&&typeof s.read!=='function')||(kind.includes('Write')&&typeof s.write!=='function')||(kind.includes('Seek')&&typeof s.seek!=='function'))invalidBoundary();
 return (_signal,request)=>{
  const run=():Promise<Uint8Array>=>serveStream(s,request??new Uint8Array(0)).catch(e=>{const m=new TextEncoder().encode(e instanceof Error?e.message:String(e));const out=new Uint8Array(1+m.length);out[0]=2;out.set(m,1);return out;});
  return streamContext?streamContext.run(true,run):run();
 };
}

/** Source std/io operations behind a host stream view; each one is a
 * library call on the handle. read returns the data and whether the source
 * reached io.EOF. */
export interface StreamOps {
 read?(h:Handle,size:number):Promise<[Uint8Array,boolean]>;
 write?(h:Handle,data:Uint8Array):Promise<number>;
 seek?(h:Handle,offset:bigint,whence:number):Promise<bigint>;
 close(h:Handle):Promise<void>;
}
/** A source reader: read returns null at end of stream. */
export interface SourceReader extends AsyncIterable<Uint8Array> {
 read(size?:number):Promise<Uint8Array|null>;
 /** A web ReadableStream over the reader; use Readable.fromWeb or
  * Readable.from(reader) for a Node stream. Ending or canceling it does not
  * close the handle. */
 stream(chunkSize?:number):ReadableStream<Uint8Array>;
}
/** A source writer. */
export interface SourceWriter {
 write(data:Uint8Array):Promise<number>;
 /** A web WritableStream into the writer; closing it closes the handle. */
 writable():WritableStream<Uint8Array>;
}
/** A seekable source stream; whence is 0 (start), 1 (current) or 2 (end). */
export interface SourceSeeker { seek(offset:bigint,whence?:number):Promise<bigint>; }
/** Every source stream is a handle: close runs the source Close, if any,
 * and releases it. */
export interface SourceCloser extends AsyncDisposable { close():Promise<void>; }

const CHUNK=64<<10;
/** The host view of a source std/io reader or writer. */
export class SourceStream implements SourceReader,SourceWriter,SourceSeeker,SourceCloser {
 readonly #h:Handle;readonly #ops:StreamOps;
 /** @internal */
 constructor(h:Handle,ops:StreamOps){this.#h=h;this.#ops=ops;}
 async read(size:number=CHUNK):Promise<Uint8Array|null>{
  if(!this.#ops.read)throw new LibraryError('unsupported');
  if(!Number.isSafeInteger(size)||size<0)invalidBoundary();
  if(size===0)return new Uint8Array(0);
  const [data,eof]=await this.#ops.read(this.#h,size);
  return data.length===0&&eof?null:data;
 }
 async write(data:Uint8Array):Promise<number>{
  if(!this.#ops.write)throw new LibraryError('unsupported');
  if(!(data instanceof Uint8Array))invalidBoundary();
  let written=0;
  do{written+=await this.#ops.write(this.#h,data.slice(written,written+(1<<20)));}while(written<data.length);
  return written;
 }
 seek(offset:bigint,whence:number=0):Promise<bigint>{
  if(!this.#ops.seek)return Promise.reject(new LibraryError('unsupported'));
  if(typeof offset!=='bigint'||![0,1,2].includes(whence))invalidBoundary();
  return this.#ops.seek(this.#h,offset,whence);
 }
 close():Promise<void>{return this.#ops.close(this.#h);}
 [Symbol.asyncDispose]():Promise<void>{return this.close();}
 async *[Symbol.asyncIterator]():AsyncGenerator<Uint8Array>{
  for(;;){const chunk=await this.read(CHUNK);if(chunk===null)return;if(chunk.length>0)yield chunk;}
 }
 stream(chunkSize:number=CHUNK):ReadableStream<Uint8Array>{
  return new ReadableStream<Uint8Array>({pull:async c=>{const chunk=await this.read(chunkSize);if(chunk===null)c.close();else c.enqueue(chunk);}},{highWaterMark:0});
 }
 writable():WritableStream<Uint8Array>{
  return new WritableStream<Uint8Array>({write:async chunk=>{await this.write(chunk);},close:()=>this.close()});
 }
}

/** Source float boundary accepts numeric IEEE values, including NaN and infinities. */
export function boundaryFloat(v:unknown,bits:number):number {
 if(v===undefined)return 0;
 if(typeof v!=='number')invalidBoundary();
 return bits===32?Math.fround(v as number):v as number;
}
