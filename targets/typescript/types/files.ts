// Host file system for lib/os. Node entries install an adapter; portable and
// browser entries leave it unset, so every call reports Unsupported.
export const MAX_FILE_BYTES = 1 << 30;
export const MAX_CHUNK = 1 << 20;
export const FileStatus = { OK: 0, NotExist: 1, Exist: 2, Permission: 3, IsDir: 4, NotDir: 5, TooLarge: 6, Unsupported: 7, Invalid: 8, IO: 9 } as const;
export type FileStatus = (typeof FileStatus)[keyof typeof FileStatus];
/** An open host file. Methods throw host errors (with a `code`); an empty read is the end of the file. */
export interface HostFile {
  read(max: number): Uint8Array;
  readAt(offset: number, max: number): Uint8Array;
  write(data: Uint8Array): number;
  seek(offset: number, whence: number): number;
  sync(): void;
  close(): void;
}
export interface FileStat { size: number; mode: number; dir: boolean }
export interface FileHost {
  readFile(path: Uint8Array): Uint8Array | FileStatus;
  writeFile(path: Uint8Array, data: Uint8Array, mode: number): FileStatus;
  open(path: Uint8Array, flag: number, mode: number): HostFile;
  stat(path: Uint8Array): FileStat;
  /** dir null selects the host temporary directory; a failure's error carries the attempted `path`. */
  createTemp(dir: Uint8Array | null, prefix: Uint8Array, suffix: Uint8Array): { file: HostFile; name: Uint8Array };
  remove(path: Uint8Array): void;
  rename(from: Uint8Array, to: Uint8Array): void;
}
let fileHost: FileHost | undefined;
export function installFileHost(host: FileHost): void { fileHost = host; }
export function fileStatus(e: unknown): FileStatus {
  switch ((e as { code?: unknown } | null)?.code) {
    case "ENOENT": return FileStatus.NotExist;
    case "EEXIST": return FileStatus.Exist;
    case "EACCES": case "EPERM": return FileStatus.Permission;
    case "EISDIR": return FileStatus.IsDir;
    case "ENOTDIR": return FileStatus.NotDir;
    case "EFBIG": case "ERR_FS_FILE_TOO_LARGE": return FileStatus.TooLarge;
    case "ENOSYS": case "ENOTSUP": case "EOPNOTSUPP": return FileStatus.Unsupported;
    case "EINVAL": case "ERR_INVALID_ARG_VALUE": case "ERR_INVALID_ARG_TYPE": case "ERR_OUT_OF_RANGE": return FileStatus.Invalid;
  }
  return FileStatus.IO;
}
function path(name: string): Uint8Array | FileStatus {
  if (name.length === 0) return FileStatus.NotExist;
  return nameBytes(name);
}
function nameBytes(name: string): Uint8Array | FileStatus {
  const b = new Uint8Array(name.length);
  for (let i = 0; i < name.length; i++) {
    const c = name.charCodeAt(i);
    if (c === 0 || c > 255) return FileStatus.Invalid;
    b[i] = c;
  }
  return b;
}
export function byteString(b: Uint8Array): string {
  let s = "";
  for (let i = 0; i < b.length; i += 8192) s += String.fromCharCode(...b.subarray(i, i + 8192));
  return s;
}
export function readHostFile(name: string): Uint8Array | FileStatus {
  const p = path(name);
  if (typeof p === "number") return p;
  if (fileHost === undefined) return FileStatus.Unsupported;
  try { return fileHost.readFile(p); } catch (e) { return fileStatus(e); }
}
export function writeHostFile(name: string, data: Uint8Array, mode: number): FileStatus {
  const p = path(name);
  if (typeof p === "number") return p;
  if (data.length > MAX_FILE_BYTES) return FileStatus.TooLarge;
  if (fileHost === undefined) return FileStatus.Unsupported;
  try { return fileHost.writeFile(p, data, mode & 0o777); } catch (e) { return fileStatus(e); }
}

// Open files by handle. Handles are never reused.
const handles = new Map<bigint, HostFile>();
let last = 0n;
function register(file: HostFile): bigint { handles.set(++last, file); return last; }
export const FILE_FLAGS = { RDONLY: 0, WRONLY: 1, RDWR: 2, APPEND: 4, CREATE: 8, EXCL: 16, TRUNC: 32 } as const;
export function openHostFile(name: string, flag: number, mode: number): [bigint, FileStatus] {
  const p = path(name);
  if (typeof p === "number") return [0n, p];
  if ((flag & ~63) !== 0 || (flag & 3) === 3) return [0n, FileStatus.Invalid];
  if (fileHost === undefined) return [0n, FileStatus.Unsupported];
  try { return [register(fileHost.open(p, flag, mode & 0o777)), FileStatus.OK]; } catch (e) { return [0n, fileStatus(e)]; }
}
export function readHostHandle(handle: bigint, offset: number | null, max: number): [Uint8Array | null, boolean, FileStatus] {
  const f = handles.get(handle);
  if (f === undefined) return [null, false, FileStatus.Invalid];
  if (max < 1 || max > MAX_CHUNK || (offset !== null && offset < 0)) return [null, false, FileStatus.Invalid];
  try {
    const data = offset === null ? f.read(max) : f.readAt(offset, max);
    return [data, data.length === 0, FileStatus.OK];
  } catch (e) { return [null, false, fileStatus(e)]; }
}
export function writeHostHandle(handle: bigint, data: Uint8Array): [number, FileStatus] {
  const f = handles.get(handle);
  if (f === undefined) return [0, FileStatus.Invalid];
  if (data.length > MAX_CHUNK) return [0, FileStatus.Invalid];
  let n = 0;
  try {
    while (n < data.length) {
      const w = f.write(data.subarray(n));
      if (w <= 0) return [n, FileStatus.IO];
      n += w;
    }
    return [n, FileStatus.OK];
  } catch (e) { return [n, fileStatus(e)]; }
}
export function seekHostHandle(handle: bigint, offset: number, whence: number): [number, FileStatus] {
  const f = handles.get(handle);
  if (f === undefined) return [0, FileStatus.Invalid];
  if (whence < 0 || whence > 2) return [0, FileStatus.Invalid];
  try { return [f.seek(offset, whence), FileStatus.OK]; } catch (e) { return [0, fileStatus(e)]; }
}
export function syncHostHandle(handle: bigint): FileStatus {
  const f = handles.get(handle);
  if (f === undefined) return FileStatus.Invalid;
  try { f.sync(); return FileStatus.OK; } catch (e) { return fileStatus(e); }
}
export function closeHostHandle(handle: bigint): FileStatus {
  const f = handles.get(handle);
  if (f === undefined) return FileStatus.Invalid;
  handles.delete(handle);
  try { f.close(); return FileStatus.OK; } catch (e) { return fileStatus(e); }
}
export function statHostFile(name: string): [FileStat | null, FileStatus] {
  const p = path(name);
  if (typeof p === "number") return [null, p];
  if (fileHost === undefined) return [null, FileStatus.Unsupported];
  try { return [fileHost.stat(p), FileStatus.OK]; } catch (e) { return [null, fileStatus(e)]; }
}
export function createHostTemp(dir: string, prefix: string, suffix: string): [bigint, string, FileStatus] {
  const d = dir.length === 0 ? null : nameBytes(dir), pre = nameBytes(prefix), suf = nameBytes(suffix);
  if (typeof d === "number" || typeof pre === "number" || typeof suf === "number") return [0n, "", FileStatus.Invalid];
  if ((prefix + suffix).includes("/")) return [0n, "", FileStatus.Invalid];
  if (fileHost === undefined) return [0n, "", FileStatus.Unsupported];
  try {
    const { file, name } = fileHost.createTemp(d, pre, suf);
    return [register(file), byteString(name), FileStatus.OK];
  } catch (e) {
    const attempted = (e as { path?: unknown } | null)?.path;
    return [0n, attempted instanceof Uint8Array ? byteString(attempted) : typeof attempted === "string" ? attempted : "", fileStatus(e)];
  }
}
export function removeHostFile(name: string): FileStatus {
  const p = path(name);
  if (typeof p === "number") return p;
  if (fileHost === undefined) return FileStatus.Unsupported;
  try { fileHost.remove(p); return FileStatus.OK; } catch (e) { return fileStatus(e); }
}
export function renameHostFile(from: string, to: string): FileStatus {
  const a = path(from), b = path(to);
  if (typeof a === "number") return a;
  if (typeof b === "number") return b;
  if (fileHost === undefined) return FileStatus.Unsupported;
  try { fileHost.rename(a, b); return FileStatus.OK; } catch (e) { return fileStatus(e); }
}
