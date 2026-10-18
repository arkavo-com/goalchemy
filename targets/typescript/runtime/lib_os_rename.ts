import { renameHostFile } from '../types/files.ts';
export function libOsRename(oldpath:string,newpath:string):bigint{return BigInt(renameHostFile(oldpath,newpath));}
