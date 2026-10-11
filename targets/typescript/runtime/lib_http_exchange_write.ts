import type { Task } from './task_spawn.ts';
import { bytes,errorBox } from '../types/native.ts';
import type { Slice } from '../types/slice.ts';
import { exchangeOp,EXCHANGE_CLOSED,EXCHANGE_STATE,type Exchange } from './lib_http_exchange_close.ts';
export function libHttpExchangeWrite(t:Task,ex:Exchange|null,data:Slice<number>):void{
 if(ex===null||data.l>1<<20){t.rv=[errorBox('http: invalid request or limit')];return;}
 if(ex.closed){t.rv=[errorBox(EXCHANGE_CLOSED)];return;}
 if(ex.responded||ex.length===0&&data.l!==0||ex.length>=0&&data.l>ex.length-ex.written){t.rv=[errorBox(EXCHANGE_STATE)];return;}
 ex.written+=data.l;if(data.l===0){t.rv=[null];return;}
 const chunk=bytes(data);
 exchangeOp(t,ex,()=>ex.stream.write(chunk),failure=>[failure===null?null:ex.error(failure)]);
}
