import { sched,type Scheduler,type Task } from './task_spawn.ts';
import { observeContext,CONTEXT_DEADLINE_EXCEEDED,type Context } from './std_context_err.ts';
import { errorBox } from '../types/native.ts';
import type { Box } from '../types/iface.ts';
import type { HttpStream } from '../types/http.ts';
export const EXCHANGE_CLOSED='http: exchange closed';
export const EXCHANGE_STATE='http: invalid exchange operation';
/** A lib/http exchange owned by one scheduler; the controller aborts it on
 * close, the deadline, source context cancellation or scheduler shutdown. */
export class Exchange{
 closed=false;responded=false;written=0;expired=false;
 readonly stream:HttpStream;readonly length:number;readonly ctx:Context;readonly controller:AbortController;readonly owner:Scheduler;private readonly unlink:()=>void;
 constructor(stream:HttpStream,length:number,ctx:Context,controller:AbortController,owner:Scheduler,unlink:()=>void){this.stream=stream;this.length=length;this.ctx=ctx;this.controller=controller;this.owner=owner;this.unlink=unlink;}
 release():void{if(this.closed)return;this.closed=true;this.controller.abort();this.unlink();this.owner.resources.delete(this.releaser);this.stream.close();}
 readonly releaser=():void=>this.release();
 /** The error of a failed operation: the context or deadline error once the exchange is aborted. */
 error(message:string):Box{observeContext(this.ctx);if(this.ctx.err!==null)return this.ctx.err;if(this.expired)return CONTEXT_DEADLINE_EXCEEDED;return errorBox(message)!;}
}
/** Runs one exchange operation as a host operation; work returns null on
 * success or a failure message, and decode builds the results on the owner. */
export function exchangeOp<T>(t:Task,ex:Exchange,work:()=>Promise<T>,decode:(v:T)=>unknown[]):void{
 const owner=sched;if(ex.owner!==owner)throw new Error('HTTP exchange belongs to another scheduler');
 const token=owner.registerHost(t,()=>ex.controller.abort(),()=>{},()=>null,rv=>decode(rv[0] as T));
 owner.launchHost(token,async()=>[await work()]);
}
export function libHttpExchangeClose(t:Task,ex:Exchange|null):void{ex?.release();t.rv=[];}
