import type { Task } from './task_spawn.ts';
import type { Context } from './std_context_err.ts';
import { strings } from '../types/native.ts';
import type { Slice } from '../types/slice.ts';
import { httpDo } from './lib_http_do.ts';
export function libHttpDoWith(t:Task,ctx:Context|null,method:string,url:string,headers:Slice<string>,body:Slice<number>,max:bigint,timeout:bigint,options:Slice<string>):void{
 httpDo(t,ctx,method,url,headers,body,max,timeout,strings(options));
}
