// Node HTTP adapter for lib/http, excluded from every portable runtime index.
import { request as plainRequest } from "node:http";
import { request as tlsRequest } from "node:https";
import { rootCertificates, createSecureContext } from "node:tls";
import { createGunzip } from "node:zlib";
import { bodyless, canonicalHeader, installHttpHost, responseHeaders, ChunkQueue, type HttpOutcome, type HttpTLS, type HttpOpen, type HttpStream, type HttpHead, type HttpChunk } from "./http.ts";

function first(raw: string[], name: string): string | null {
  for (let i = 0; i < raw.length; i += 2) if (raw[i].toLowerCase() === name) return raw[i + 1];
  return null;
}

// Adds lib/http.DoWith options to request options; returns an error message.
function transport(options: Record<string, unknown>, tls: HttpTLS | null, secure: boolean): string | null {
  if (tls === null) return null;
  const { cert, key, ca, insecure, socket } = tls;
  if (socket !== undefined) options.socketPath = new TextDecoder().decode(Uint8Array.from(socket, c => c.charCodeAt(0)));
  try {
    const extra = ca === undefined ? undefined : [...rootCertificates, ca];
    createSecureContext({ cert, key, ca: extra });
    if (secure) Object.assign(options, { cert, key, ca: extra, rejectUnauthorized: !insecure });
  } catch {
    return "http: invalid TLS option";
  }
  return null;
}

const FAILED = "http: transport failed";

// A streamed exchange on node:http. Writes resolve once flushed, so the
// request body is never queued whole; the response is read in paused mode.
function open(request: HttpOpen): HttpStream | string {
  const headers: string[] = ["Host", new URL(request.url).host];
  for (const [n, v] of request.headers) headers.push(n, v);
  if (request.length >= 0 && request.method === "POST") headers.push("Content-Length", String(request.length));
  if (request.length < 0) headers.push("Transfer-Encoding", "chunked");
  const secure = request.url.startsWith("https:");
  const options: Record<string, unknown> = { method: request.method, headers, agent: false, maxHeaderSize: 81920, signal: request.signal };
  const invalid = transport(options, request.tls, secure);
  if (invalid !== null) return invalid;
  let failure: string | null = null, res: any = null, ended = false;
  let wake: () => void = () => {};
  const changed = (): void => { const w = wake; wake = () => {}; w(); };
  const writes = new Set<(v: string | null) => void>();
  const fail = (): void => { failure ??= FAILED; for (const w of [...writes]) w(failure); changed(); };
  const queue = new ChunkQueue();
  const req = (secure ? tlsRequest : plainRequest)(request.url, options, (r: any) => {
    res = r;
    r.on("data", (chunk: Uint8Array) => { queue.push(new Uint8Array(chunk)); if (queue.size >= 1 << 20) r.pause(); changed(); });
    r.on("end", () => { ended = true; changed(); });
    r.on("error", fail);
    r.on("aborted", fail);
    r.pause();
    changed();
  });
  req.on("error", fail);
  const settle = <T>(check: () => T | null): Promise<T> => new Promise<T>(resolve => {
    const attempt = (): void => { const v = check(); if (v !== null) resolve(v); else wake = attempt; };
    attempt();
  });
  return {
    write: data => new Promise<string | null>(resolve => {
      if (failure !== null) { resolve(failure); return; }
      const finish = (v: string | null): void => { if (writes.delete(finish)) resolve(v); };
      writes.add(finish);
      req.write(data, (e: unknown) => finish(e ? failure ?? FAILED : null));
    }),
    response: () => {
      req.end();
      return settle<HttpHead>(() => {
        if (res !== null) {
          const headers = responseHeaders(pairsOf(res.rawHeaders));
          return headers === null ? { error: "http: response headers exceed limit" } : { status: res.statusCode ?? 0, headers };
        }
        return failure !== null ? { error: failure } : null;
      });
    },
    read: max => settle<HttpChunk>(() => {
      const data = queue.take(max);
      if (queue.size < 1 << 20) res.resume();
      if (data !== null) return { data };
      if (failure !== null) return { error: failure };
      if (ended) return { eof: true };
      return null;
    }),
    close: () => { failure ??= "http: exchange closed"; req.destroy(); res?.destroy(); fail(); },
  };
}

function* pairsOf(raw: string[]): Iterable<[string, string]> {
  for (let i = 0; i + 1 < raw.length; i += 2) yield [raw[i], raw[i + 1]];
}

installHttpHost({
  open,
  exchange: request => new Promise<HttpOutcome>(resolve => {
    let settled = false;
    const finish = (outcome: HttpOutcome): void => { if (!settled) { settled = true; resolve(outcome); } };
    const automatic = !request.headers.some(([n, v], i) =>
      (n.toLowerCase() === "accept-encoding" || n.toLowerCase() === "range") && v !== "" &&
      request.headers.findIndex(([m]) => m.toLowerCase() === n.toLowerCase()) === i);
    const headers: string[] = ["Host", new URL(request.url).host];
    for (const [n, v] of request.headers) if (!automatic || n.toLowerCase() !== "accept-encoding") headers.push(n, v);
    if (automatic) headers.push("Accept-Encoding", "gzip");
    if (request.method === "POST") headers.push("Content-Length", String(request.body.length));
    const secure = request.url.startsWith("https:");
    const options: Record<string, unknown> = { method: request.method, headers, agent: false, maxHeaderSize: 81920, signal: request.signal };
    const failure = transport(options, request.tls, secure);
    if (failure !== null) { finish({ error: failure }); return; }
    const req = (secure ? tlsRequest : plainRequest)(request.url, options, (res: any) => {
      const raw = res.rawHeaders;
      const status = res.statusCode ?? 0;
      const decoded = automatic && !bodyless(status, first(raw, "content-length")) && (first(raw, "content-encoding") ?? "").toLowerCase() === "gzip";
      const pairs: [string, string, number][] = [];
      let total = 0;
      for (let i = 0; i < raw.length; i += 2) {
        const name = canonicalHeader(raw[i]);
        if (decoded && (name === "Content-Encoding" || name === "Content-Length")) continue;
        total += name.length + raw[i + 1].length + 4;
        pairs.push([name, raw[i + 1], i]);
      }
      if (total > 65536) { res.destroy(); finish({ error: "http: response headers exceed limit" }); return; }
      pairs.sort((a, b) => a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : a[2] - b[2]);
      const flat = pairs.flatMap(([n, v]) => [n, v]);
      const chunks: Uint8Array[] = [];
      let length = 0;
      const accept = (chunk: Uint8Array): boolean => {
        length += chunk.length;
        if (length > request.max) { res.destroy(); finish({ error: "http: response body exceeds limit" }); return false; }
        chunks.push(new Uint8Array(chunk));
        return true;
      };
      const done = (): void => {
        const body = new Uint8Array(length);
        let p = 0;
        for (const c of chunks) { body.set(c, p); p += c.length; }
        finish({ status, headers: flat, body });
      };
      res.on("error", () => finish({ error: "http: transport failed" }));
      if (!decoded) {
        res.on("data", (chunk: Uint8Array) => { accept(chunk); });
        res.on("end", done);
        return;
      }
      const gunzip = createGunzip();
      let compressed = false;
      gunzip.on("data", (chunk: Uint8Array) => { if (!accept(chunk)) gunzip.destroy(); });
      gunzip.on("error", () => { res.destroy(); finish({ error: "http: transport failed" }); });
      gunzip.on("end", done);
      res.on("data", (chunk: Uint8Array) => { compressed = true; gunzip.write(chunk); });
      res.on("end", () => { if (compressed) gunzip.end(); else done(); });
    });
    req.on("error", () => finish({ error: request.signal.aborted ? "http: canceled" : "http: transport failed" }));
    req.end(request.method === "POST" ? request.body : undefined);
  }),
});
