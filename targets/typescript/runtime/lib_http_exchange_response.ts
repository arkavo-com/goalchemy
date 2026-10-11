import type { Task } from './task_spawn.ts';
import { errorBox,stringSlice } from '../types/native.ts';
import { NIL } from '../types/slice.ts';
import type { HttpHead } from '../types/http.ts';
import { exchangeOp,EXCHANGE_CLOSED,EXCHANGE_STATE,type Exchange } from './lib_http_exchange_close.ts';
export function libHttpExchangeResponse(t:Task,ex:Exchange|null):void{
 const zero=(e:unknown):unknown[]=>[0n,NIL,e];
 if(ex===null){t.rv=zero(errorBox('http: invalid request or limit'));return;}
 if(ex.closed){t.rv=zero(errorBox(EXCHANGE_CLOSED));return;}
 ex.responded=true;
 if(ex.length>=0&&ex.written!==ex.length){ex.controller.abort();t.rv=zero(errorBox(EXCHANGE_STATE));return;}
 exchangeOp(t,ex,()=>ex.stream.response(),(head:HttpHead)=>'error' in head?zero(ex.error(head.error)):[BigInt(head.status),stringSlice(head.headers),null]);
}
