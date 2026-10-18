import { syncHostHandle } from '../types/files.ts';
export function libOsSync(file:bigint):bigint{return BigInt(syncHostHandle(file));}
