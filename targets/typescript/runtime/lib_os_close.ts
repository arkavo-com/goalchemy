import { closeHostHandle } from '../types/files.ts';
export function libOsClose(file:bigint):bigint{return BigInt(closeHostHandle(file));}
