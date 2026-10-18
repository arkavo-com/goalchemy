import { removeHostFile } from '../types/files.ts';
export function libOsRemove(name:string):bigint{return BigInt(removeHostFile(name));}
