import { sched,type Task } from './task_spawn.ts';
import { observeContext,onContextCancel,type Context } from './std_context_err.ts';
import { errorBox,strings,DeclaredFailure,MAX_BYTES } from '../types/native.ts';
import type { Slice } from '../types/slice.ts';
import { installedHttpHost,responseHeaders,ChunkQueue,type HttpOpen,type HttpStream,type HttpTLS } from '../types/http.ts';
import { fetchURL,forbiddenHeader,tlsOptions } from './lib_http_do.ts';
import { Exchange } from './lib_http_exchange_close.ts';
const MAX_EXCHANGE_MILLIS=24*60*60*1000;
/** Browser exchanges on fetch: browsers cannot stream request bodies over
 * HTTP/1.1, so the request body is collected (at most 64MiB) and sent by
 * response(); the response body streams. */
function fetchStream(request:HttpOpen):HttpStream{
 const parts:Uint8Array[]=[];let size=0;let reader:ReadableStreamDefaultReader<Uint8Array>|null=null;const queue=new ChunkQueue();let failure:string|null=null;
 let head:ReturnType<HttpStream['response']>|null=null;
 return {
  write:async data=>{size+=data.length;if(size>MAX_BYTES)return 'http: browser request bodies are limited to 64MiB';parts.push(data);return null;},
  response:()=>head??=(async()=>{
   try{
    const body=new Uint8Array(size);let p=0;for(const part of parts){body.set(part,p);p+=part.length;}parts.length=0;
    const response=await fetch(request.url,{method:request.method,headers:request.headers,body:request.method==='POST'?body:undefined,signal:request.signal,redirect:'manual',credentials:'omit'});
    if(response.type==='opaqueredirect'||response.type==='opaque'||response.status===0)return {error:'http: opaque response or redirect'};
    const headers=responseHeaders(response.headers);if(headers===null)return {error:'http: response headers exceed limit'};
    reader=response.body?.getReader()??null;return {status:response.status,headers};
   }catch(e){if(e instanceof TypeError||e instanceof DOMException)return {error:'http: transport failed'};throw e;}
  })(),
  read:async max=>{
   for(;;){
    const data=queue.take(max);if(data!==null)return {data};
    if(failure!==null)return {error:failure};if(reader===null)return {eof:true};
    try{const {value,done}=await reader.read();if(done)return {eof:true};queue.push(new Uint8Array(value));}
    catch(e){if(e instanceof TypeError||e instanceof DOMException){failure='http: transport failed';continue;}throw e;}
   }
  },
  close:()=>{failure??='http: exchange closed';reader?.cancel().catch(()=>{});},
 };
}
export function libHttpOpen(t:Task,ctx:Context|null,method:string,url:string,headers:Slice<string>,length:bigint,timeout:bigint,options:Slice<string>):void{
 const fail=(message:string):void=>{t.rv=[null,errorBox(message)];};
 if(ctx===null){fail('http: invalid request or limit');return;}observeContext(ctx);if(ctx.err!==null){t.rv=[null,ctx.err];return;}
 const host=installedHttpHost();let destination:string,tls:HttpTLS|null;const pairs:[string,string][]=[];
 try{
  if((method!=='GET'&&method!=='POST')||length< -1n||(method==='GET'&&length!==0n)||length>BigInt(Number.MAX_SAFE_INTEGER)||timeout<1n||timeout>BigInt(MAX_EXCHANGE_MILLIS)||headers.l%2||headers.l>32768||url.length>8192)throw new DeclaredFailure('invalid');
  destination=fetchURL(url);const hs=strings(headers);let total=0;
  for(let i=0;i<hs.length;i+=2){const n=hs[i],v=hs[i+1];total+=n.length+v.length+4;if(total>65536||!n||! /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/.test(n)||/[\x00-\x08\x0a-\x1f\x7f]/.test(v)||forbiddenHeader(n,v))throw new DeclaredFailure('invalid');pairs.push([n,v]);}
  tls=tlsOptions(strings(options));
 }catch(e){if(e instanceof DeclaredFailure||e instanceof TypeError){fail('http: invalid request or limit');return;}throw e;}
 if(host===undefined&&tls!==null){fail('http: transport options are not supported in browsers');return;}
 const controller=new AbortController();const request:HttpOpen={method,url:destination,headers:pairs,length:Number(length),tls,signal:controller.signal};
 const stream=host!==undefined?host.open(request):fetchStream(request);
 if(typeof stream==='string'){fail(stream);return;}
 const owner=sched;let ex:Exchange|null=null;
 const unlinkContext=onContextCancel(ctx,()=>controller.abort());
 const cancelAlarm=owner.host.alarm(Number(timeout),()=>{if(ex!==null)ex.expired=true;controller.abort();stream.close();});
 ex=new Exchange(stream,Number(length),ctx,controller,owner,()=>{unlinkContext();cancelAlarm();});
 controller.signal.addEventListener('abort',()=>stream.close(),{once:true});
 owner.resources.add(ex.releaser);
 t.rv=[ex,null];
}
