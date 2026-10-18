import { openHostFile } from '../types/files.ts';
export function libOsOpen(name:string,flag:bigint,perm:number|bigint):[bigint,bigint]{const [h,s]=openHostFile(name,Number(flag),Number(perm));return [h,BigInt(s)];}
