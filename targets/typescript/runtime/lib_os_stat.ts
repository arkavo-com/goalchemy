import { statHostFile } from '../types/files.ts';
export function libOsStat(name:string):[bigint,bigint,boolean,bigint]{const [st,s]=statHostFile(name);return st===null?[0n,0n,false,BigInt(s)]:[BigInt(st.size),BigInt(st.mode),st.dir,BigInt(s)];}
