// The Node file-system API used by lib/os and the executable adapter. Inside
// a module declaration these names shadow @types/node's re-exports, so they
// stay as permissive as Node's own overloads.
declare module "node:fs" {
  export function openSync(path: any, flags?: any, mode?: any): number;
  export function closeSync(fd: number): void;
  export function fstatSync(fd: number, options?: any): any;
  export function statSync(path: any, options?: any): any;
  export function readFileSync(path: any, options?: any): any;
  export function writeFileSync(path: any, data: any, options?: any): void;
  export function readSync(fd: number, buffer: any, ...args: any[]): number;
  export function writeSync(fd: number, buffer: any, ...args: any[]): number;
  export function fsyncSync(fd: number): void;
  export function renameSync(oldPath: any, newPath: any): void;
  export function unlinkSync(path: any): void;
  export function rmdirSync(path: any, options?: any): void;
  export const constants: any;
}
declare module "node:os" {
  export function tmpdir(): string;
}
