import { sched,type Task } from './task_spawn.ts';
import { observeContext,onContextCancel,type Context } from './std_context_err.ts';
import { bytes,byteSlice,stringSlice,strings,errorBox,DeclaredFailure,MAX_BYTES,binaryInput } from '../types/native.ts';
import { BYTE_NIL,NIL,type Slice } from '../types/slice.ts';
import { bodyless,installedHttpHost,type HttpTLS } from '../types/http.ts';
function fail(t:Task,message:string):void{t.rv=[0n,NIL,BYTE_NIL,errorBox(message)];}
// Fetch forbids these request fields in browsers. Reject them in both targets
// before submission instead of allowing native fetch to silently discard them.
const forbidden=new Set(['accept-charset','access-control-request-headers','access-control-request-method','connection','content-length','cookie','cookie2','date','dnt','expect','host','keep-alive','origin','referer','set-cookie','te','trailer','transfer-encoding','upgrade','via','proxy-authorization','proxy-connection']);
function forbiddenHeader(name:string,value:string):boolean{
 const n=name.toLowerCase();return forbidden.has(n)||n.startsWith('proxy-')||n.startsWith('sec-')||(['x-http-method','x-http-method-override','x-method-override'].includes(n)&&value.split(',').some(v=>['CONNECT','TRACE','TRACK'].includes(v.trim().toUpperCase())));
}
function fetchURL(raw:string):string{
 const text=new TextDecoder('utf-8',{fatal:true,ignoreBOM:true}).decode(binaryInput(raw,8192));
 if(!/^https?:\/\//i.test(text)||/[\x00-\x20\x7f\\#]/.test(text)||/%(?![0-9a-f]{2})/i.test(text))throw new DeclaredFailure('invalid');
 const authority=text.split('://')[1].split(/[/?]/)[0];
 if(!authority||/[^\x21-\x7e]|[@%]/.test(authority))throw new DeclaredFailure('invalid');
 const parsed=new URL(text),canonical=parsed.host.toLowerCase();
 const original=authority.toLowerCase().replace(parsed.protocol==='http:'?/:80$/:/:443$/,'');
 if(!parsed.hostname||parsed.username||parsed.password||canonical!==original)throw new DeclaredFailure('invalid');
 // WHATWG URL removes dot segments (including encoded dots); reject ambiguous
 // source paths instead of changing the resource requested by the source.
 const path=text.slice(text.indexOf(authority)+authority.length).split('?')[0];
 if(path.split('/').some(part=>{const dots=part.replace(/%2e/ig,'.');return dots==='.'||dots==='..';}))throw new DeclaredFailure('invalid');
 return parsed.href;
}
const optionNames=new Set(['client-cert','client-key','root-cas','insecure-skip-verify','unix-socket']);
function tlsOptions(options:string[]):HttpTLS|null{
 if(options.length===0)return null;
 if(options.length%2)throw new DeclaredFailure('invalid');
 const values=new Map<string,string>();let total=0;
 for(let i=0;i<options.length;i+=2){const n=options[i],v=options[i+1];total+=n.length+v.length;if(total>1<<20||values.has(n)||!optionNames.has(n))throw new DeclaredFailure('invalid');values.set(n,v);}
 const skip=values.get('insecure-skip-verify'),socket=values.get('unix-socket');
 if(values.has('client-cert')!==values.has('client-key')||(skip!==undefined&&skip!=='true'&&skip!=='false')||(socket!==undefined&&(socket===''||socket.length>4096||socket.includes('\0'))))throw new DeclaredFailure('invalid');
 return {cert:values.get('client-cert'),key:values.get('client-key'),ca:values.get('root-cas'),insecure:skip==='true',socket};
}
export function libHttpDo(t:Task,ctx:Context|null,method:string,url:string,headers:Slice<string>,body:Slice<number>,max:bigint,timeout:bigint):void{
 httpDo(t,ctx,method,url,headers,body,max,timeout,[]);
}
export function httpDo(t:Task,ctx:Context|null,method:string,url:string,headers:Slice<string>,body:Slice<number>,max:bigint,timeout:bigint,options:string[]):void{
 if(ctx===null){fail(t,'http: invalid request or limit');return;}observeContext(ctx);if(ctx.err!==null){t.rv=[0n,NIL,BYTE_NIL,ctx.err];return;}
 let payload:Uint8Array<ArrayBuffer>,input:Headers,destination:string,tls:HttpTLS|null;const pairs:[string,string][]=[];const host=installedHttpHost();
 try{
  if((method!=='GET'&&method!=='POST')||(method==='GET'&&body.l!==0)||body.l>MAX_BYTES||max<0n||max>BigInt(MAX_BYTES)||timeout<1n||timeout>300000n||headers.l%2||headers.l>32768||url.length>8192)throw new DeclaredFailure('invalid');
  destination=fetchURL(url);
  input=new Headers();const hs=strings(headers);let total=0;
  for(let i=0;i<hs.length;i+=2){const n=hs[i],v=hs[i+1];total+=n.length+v.length+4;if(total>65536||!n||! /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/.test(n)||/[\x00-\x08\x0a-\x1f\x7f]/.test(v)||forbiddenHeader(n,v))throw new DeclaredFailure('invalid');if(host===undefined&&n.toLowerCase()==='accept-encoding'){fail(t,'http: Accept-Encoding is not supported in browsers');return;}input.append(n,v);pairs.push([n,v]);}
  payload=bytes(body);
  tls=tlsOptions(options);
 }catch(e){if(e instanceof DeclaredFailure||e instanceof TypeError){fail(t,'http: invalid request or limit');return;}throw e;}
 if(host===undefined&&tls!==null){fail(t,'http: transport options are not supported in browsers');return;}
 const controller=new AbortController(),owner=sched;let expired=false;
 const stop=()=>controller.abort();const unlink=onContextCancel(ctx,stop);
 const alarm=owner.host.alarm(Number(timeout),()=>{expired=true;stop();});
 const token=owner.registerHost(t,stop,()=>{unlink();alarm();},()=>{observeContext(ctx);return ctx.err===null?null:[0n,NIL,BYTE_NIL,ctx.err];},rv=>[BigInt(rv[0] as number),stringSlice(rv[1] as string[]|null),byteSlice(rv[2] as Uint8Array|null),errorBox(rv[3] as string|null)]);
 owner.launchHost(token,async()=>{
  if(host!==undefined){
   try{const out=await host.exchange({method,url:destination,headers:pairs,body:payload,max:Number(max),signal:controller.signal,tls});
    if('error' in out)return [0,null,null,expired?'http: deadline exceeded':out.error];return [out.status,out.headers,out.body,null];}
   finally{alarm();}
  }
  let reader:ReadableStreamDefaultReader<Uint8Array>|null=null;let response:Response|null=null;let transportFailure:TypeError|DOMException|null=null;
  try{
   response=await fetch(destination,{method,headers:input,body:method==='POST'?payload:undefined,signal:controller.signal,redirect:'manual',credentials:'omit'});
   if(response.type==='opaqueredirect'||response.type==='opaque'||response.status===0)return [0,null,null,'http: opaque response or redirect'];
   const decoded=!bodyless(response.status,response.headers.get('content-length'))&&(response.headers.get('content-encoding')??'').toLowerCase()==='gzip';
   const hs:string[]=[];let total=0;for(const [n,v]of response.headers){if(decoded&&(n==='content-encoding'||n==='content-length'))continue;total+=n.length+v.length+4;if(total>65536)return [0,null,null,'http: response headers exceed limit'];hs.push(n.replace(/(^|-)([a-z])/g,(_,a:string,b:string)=>a+b.toUpperCase()),v);}
   const chunks:Uint8Array[]=[];let len=0;
   if(response.body!==null){reader=response.body.getReader();for(;;){const {value,done}=await reader.read();if(done)break;len+=value.length;if(len>Number(max))return [0,null,null,'http: response body exceeds limit'];chunks.push(new Uint8Array(value));}}
   const output=new Uint8Array(len);let p=0;for(const c of chunks){output.set(c,p);p+=c.length;}return [response.status,hs,output,null];
  }catch(e){if(e instanceof TypeError||e instanceof DOMException){transportFailure=e;return [0,null,null,expired?'http: deadline exceeded':'http: transport failed'];}throw e;}
  // Errored streams reject cancel with the stored read error. Preserve its
  // declared transport classification; independent cleanup faults stay faults.
  finally{try{if(reader!==null){try{await reader.cancel();}finally{reader.releaseLock();}}else if(response?.body){await response.body.cancel();}}catch(e){if(!(e instanceof DOMException&&e.name==='AbortError')&&!(transportFailure!==null&&e===transportFailure))throw e;}finally{alarm();}}
 });
}
