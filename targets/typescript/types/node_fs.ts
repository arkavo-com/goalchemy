// Node file-system adapter for lib/os, excluded from every portable runtime index.
import { closeSync, constants, fstatSync, fsyncSync, openSync, readFileSync, readSync, renameSync, rmdirSync, statSync, unlinkSync, writeFileSync, writeSync } from "node:fs";
import { tmpdir } from "node:os";
import { FILE_FLAGS, FileStatus, MAX_FILE_BYTES, installFileHost, type HostFile } from "./files.ts";

function nodeFlags(flag: number): number {
  let out = (flag & 3) === FILE_FLAGS.RDWR ? constants.O_RDWR : (flag & 3) === FILE_FLAGS.WRONLY ? constants.O_WRONLY : constants.O_RDONLY;
  if (flag & FILE_FLAGS.APPEND) out |= constants.O_APPEND;
  if (flag & FILE_FLAGS.CREATE) out |= constants.O_CREAT;
  if (flag & FILE_FLAGS.EXCL) out |= constants.O_EXCL;
  if (flag & FILE_FLAGS.TRUNC) out |= constants.O_TRUNC;
  return out;
}
function invalid(): Error & { code: string } { return Object.assign(new Error("invalid argument"), { code: "EINVAL" }); }
/** Node has no lseek; the adapter tracks the offset and uses positional reads and writes. */
function nodeFile(fd: number, append: boolean): HostFile {
  let pos = 0;
  return {
    read: max => { const buf = new Uint8Array(max); const n = readSync(fd, buf, 0, max, pos); pos += n; return buf.subarray(0, n); },
    readAt: (offset, max) => { const buf = new Uint8Array(max); const n = readSync(fd, buf, 0, max, offset); return buf.subarray(0, n); },
    write: data => {
      const n = append ? writeSync(fd, data) : writeSync(fd, data, 0, data.length, pos);
      pos = append ? Number(fstatSync(fd).size) : pos + n;
      return n;
    },
    seek: (offset, whence) => {
      const next = whence === 0 ? offset : whence === 1 ? pos + offset : Number(fstatSync(fd).size) + offset;
      if (next < 0) throw invalid();
      pos = next;
      return pos;
    },
    sync: () => { fsyncSync(fd); },
    close: () => { closeSync(fd); },
  };
}
const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz";
function randomName(): Uint8Array {
  const r = new Uint8Array(10);
  crypto.getRandomValues(r);
  return Uint8Array.from(r, b => alphabet.charCodeAt(b % 36));
}
function concat(...parts: Uint8Array[]): Uint8Array {
  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
  let at = 0;
  for (const p of parts) { out.set(p, at); at += p.length; }
  return out;
}
installFileHost({
  readFile: path => {
    const fd = openSync(path, "r");
    try {
      if (fstatSync(fd).size > MAX_FILE_BYTES) return FileStatus.TooLarge;
      const data = readFileSync(fd);
      return data.length > MAX_FILE_BYTES ? FileStatus.TooLarge : new Uint8Array(data.buffer, data.byteOffset, data.length);
    } finally {
      closeSync(fd);
    }
  },
  writeFile: (path, data, mode) => {
    writeFileSync(path, data, { mode });
    return FileStatus.OK;
  },
  open: (path, flag, mode) => nodeFile(openSync(path, nodeFlags(flag), mode), (flag & FILE_FLAGS.APPEND) !== 0),
  stat: path => { const st = statSync(path); return { size: Number(st.size), mode: Number(st.mode) & 0o777, dir: st.isDirectory() }; },
  createTemp: (dir, prefix, suffix) => {
    const base = dir ?? new TextEncoder().encode(tmpdir());
    const sep = new Uint8Array([47]);
    for (let i = 0; ; i++) {
      const name = concat(base, sep, prefix, randomName(), suffix);
      try {
        return { file: nodeFile(openSync(name, constants.O_RDWR | constants.O_CREAT | constants.O_EXCL, 0o600), false), name };
      } catch (e) {
        if ((e as { code?: unknown }).code !== "EEXIST" || i >= 10000) {
          if (typeof e === "object" && e !== null) (e as { path?: unknown }).path = name;
          throw e;
        }
      }
    }
  },
  remove: path => {
    try { unlinkSync(path); return; } catch (e) {
      try { rmdirSync(path); return; } catch (e2) {
        throw (e2 as { code?: unknown }).code === "ENOTDIR" ? e : e2;
      }
    }
  },
  rename: (from, to) => { renameSync(from, to); },
});
