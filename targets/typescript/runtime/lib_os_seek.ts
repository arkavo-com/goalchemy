import { seekHostHandle } from '../types/files.ts';
export function libOsSeek(file:bigint,offset:bigint,whence:bigint):[bigint,bigint]{const [p,s]=seekHostHandle(file,Number(offset),Number(whence));return [BigInt(p),BigInt(s)];}
