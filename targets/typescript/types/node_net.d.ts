// The Node HTTP and zlib APIs used by the lib/http adapter. Inside a module
// declaration these names shadow @types/node's re-exports, so they stay as
// permissive as Node's own overloads.
declare module "node:http" {
  export function request(url: string, options: any, callback?: (res: any) => void): any;
}
declare module "node:https" {
  export function request(url: string, options: any, callback?: (res: any) => void): any;
}
declare module "node:tls" {
  export const rootCertificates: readonly string[];
  export function createSecureContext(options?: any): any;
}
declare module "node:zlib" {
  export function createGunzip(options?: any): any;
}
