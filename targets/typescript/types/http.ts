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
// Transport options of lib/http.DoWith; ca adds to the system roots.
export interface HttpTLS {
  cert?: string;
  key?: string;
  ca?: string;
  insecure: boolean;
}
export type HttpOutcome = { status: number; headers: string[]; body: Uint8Array } | { error: string };
export interface HttpHost {
  exchange(request: HttpExchange): Promise<HttpOutcome>;
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
