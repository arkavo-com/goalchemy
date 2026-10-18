import { createHostTemp } from '../types/files.ts';
export function libOsCreateTemp(dir:string,prefix:string,suffix:string):[bigint,string,bigint]{const [h,name,s]=createHostTemp(dir,prefix,suffix);return [h,name,BigInt(s)];}
