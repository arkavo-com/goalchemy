// Node HTTP adapter for lib/http, excluded from every portable runtime index.
import { request as plainRequest } from "node:http";
import { request as tlsRequest } from "node:https";
import { rootCertificates, createSecureContext } from "node:tls";
import { createGunzip } from "node:zlib";
import { bodyless, canonicalHeader, installHttpHost, type HttpOutcome } from "./http.ts";

function first(raw: string[], name: string): string | null {
  for (let i = 0; i < raw.length; i += 2) if (raw[i].toLowerCase() === name) return raw[i + 1];
  return null;
}

installHttpHost({
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
    if (request.tls !== null) {
      const { cert, key, ca, insecure } = request.tls;
      try {
        const extra = ca === undefined ? undefined : [...rootCertificates, ca];
        createSecureContext({ cert, key, ca: extra });
        if (secure) Object.assign(options, { cert, key, ca: extra, rejectUnauthorized: !insecure });
      } catch {
        finish({ error: "http: invalid TLS option" });
        return;
      }
    }
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
