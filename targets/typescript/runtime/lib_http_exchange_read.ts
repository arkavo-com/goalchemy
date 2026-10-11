import type { Task } from './task_spawn.ts';
import { byteSlice,errorBox } from '../types/native.ts';
import { BYTE_NIL } from '../types/slice.ts';
import type { HttpChunk } from '../types/http.ts';
import { exchangeOp,EXCHANGE_CLOSED,EXCHANGE_STATE,type Exchange } from './lib_http_exchange_close.ts';
export function libHttpExchangeRead(t:Task,ex:Exchange|null,max:bigint):void{
 const fail=(e:unknown):void=>{t.rv=[BYTE_NIL,false,e];};
 if(ex===null||max<1n||max>BigInt(1<<20)){fail(errorBox('http: invalid request or limit'));return;}
 if(ex.closed){fail(errorBox(EXCHANGE_CLOSED));return;}
 if(!ex.responded){fail(errorBox(EXCHANGE_STATE));return;}
 exchangeOp(t,ex,()=>ex.stream.read(Number(max)),(chunk:HttpChunk)=>'error' in chunk?[BYTE_NIL,false,ex.error(chunk.error)]:'eof' in chunk?[BYTE_NIL,true,null]:[byteSlice(chunk.data),false,null]);
}
