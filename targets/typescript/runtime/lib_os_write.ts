import { writeHostHandle, MAX_CHUNK, FileStatus } from '../types/files.ts';
import type { Slice } from '../types/slice.ts';
export function libOsWrite(file:bigint,data:Slice<number>):[bigint,bigint]{if(data.l>MAX_CHUNK)return [0n,BigInt(FileStatus.Invalid)];const b=new Uint8Array(data.l);for(let i=0;i<data.l;i++)b[i]=data.a![data.o+i]!;const [n,s]=writeHostHandle(file,b);return [BigInt(n),BigInt(s)];}
