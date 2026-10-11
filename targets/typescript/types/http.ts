// Host HTTP transport for lib/http. Node entries install an adapter with Go's
// transport semantics; portable and browser entries leave it unset and use fetch.
export interface HttpExchange {
  method: string;
  url: string;
  headers: [string, string][];
  body: Uint8Array;
  max: number;
  signal: AbortSignal;
  tls: HttpTLS | null;
}
// Transport options of lib/http.DoWith; ca adds to the system roots and
// socket is a Unix domain socket path that replaces the URL host connection.
export interface HttpTLS {
  cert?: string;
  key?: string;
  ca?: string;
  insecure: boolean;
  socket?: string;
}
export type HttpOutcome = { status: number; headers: string[]; body: Uint8Array } | { error: string };
export interface HttpHost {
  exchange(request: HttpExchange): Promise<HttpOutcome>;
  open(request: HttpOpen): HttpStream | string;
}
// One streamed lib/http exchange: method, URL and headers are validated,
// length is the request body length or -1 for chunked.
export interface HttpOpen {
  method: string;
  url: string;
  headers: [string, string][];
  length: number;
  tls: HttpTLS | null;
  signal: AbortSignal;
}
export type HttpHead = { status: number; headers: string[] } | { error: string };
export type HttpChunk = { data: Uint8Array } | { eof: true } | { error: string };
// Operations are sequential; failures are messages, and an aborted signal
// fails pending and later operations.
export interface HttpStream {
  write(data: Uint8Array): Promise<string | null>;
  response(): Promise<HttpHead>;
  read(max: number): Promise<HttpChunk>;
  close(): void;
}
// Sorted, canonical response headers within the 64KiB bound, or null. Like
// Go's transport, Transfer-Encoding and Connection: close are framing, not headers.
export function responseHeaders(raw: Iterable<[string, string]>): string[] | null {
  const pairs: [string, string, number][] = [];
  let total = 0, order = 0;
  for (const [n, v] of raw) {
    const name = canonicalHeader(n);
    if (name === "Transfer-Encoding" || name === "Connection" && v.split(",").some(t => t.trim().toLowerCase() === "close")) continue;
    total += name.length + v.length + 4;
    if (total > 65536) return null;
    pairs.push([name, v, order++]);
  }
  pairs.sort((a, b) => a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : a[2] - b[2]);
  return pairs.flatMap(([n, v]) => [n, v]);
}
// Serves read(max) from a queue of received chunks, joining small ones.
export class ChunkQueue {
  private chunks: Uint8Array[] = [];
  size = 0;
  take(max: number): Uint8Array | null {
    if (this.size === 0) return null;
    const n = Math.min(max, this.size);
    const head = this.chunks[0];
    if (head.length >= n) {
      this.chunks[0] = head.subarray(n);
      if (this.chunks[0].length === 0) this.chunks.shift();
      this.size -= n;
      return head.length === n ? head : head.slice(0, n);
    }
    const out = new Uint8Array(n);
    let p = 0;
    while (p < n) {
      const c = this.chunks[0], k = Math.min(c.length, n - p);
      out.set(c.subarray(0, k), p);
      p += k;
      if (k === c.length) this.chunks.shift(); else this.chunks[0] = c.subarray(k);
    }
    this.size -= n;
    return out;
  }
  push(chunk: Uint8Array): void { if (chunk.length) { this.chunks.push(chunk); this.size += chunk.length; } }
}
let httpHost: HttpHost | undefined;
export function installHttpHost(host: HttpHost): void { httpHost = host; }
export function installedHttpHost(): HttpHost | undefined { return httpHost; }
export function canonicalHeader(name: string): string {
  return name.toLowerCase().replace(/(^|-)([a-z])/g, (_, a: string, b: string) => a + b.toUpperCase());
}
// Go's transport decodes only gzip it requested itself, and only for responses
// that carry a body.
export function bodyless(status: number, contentLength: string | null): boolean {
  return status === 204 || status === 304 || (status >= 100 && status < 200) || contentLength?.trim() === "0";
}
