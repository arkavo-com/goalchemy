import { readHostHandle } from '../types/files.ts';
import { byteSlice } from '../types/native.ts';
import type { Slice } from '../types/slice.ts';
export function libOsReadAt(file:bigint,offset:bigint,max:bigint):[Slice<number>,boolean,bigint]{const [d,eof,s]=readHostHandle(file,offset<0n?-1:Number(offset),max>BigInt(1<<21)?1<<21:Number(max));return [byteSlice(d),eof,BigInt(s)];}
