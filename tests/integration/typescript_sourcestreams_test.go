package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
)

func TestGeneratedTypeScriptSourceStreams(t *testing.T) {
	res := buildSourceStreams(t)
	out, consumer := t.TempDir(), t.TempDir()
	if ds := driver.Emit("typescript", res, out); diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	runTool(t, out, "tsc", "-p", ".")
	source := fmt.Sprintf("import * as g from %q;\n", filepath.ToSlash(filepath.Join(out, "dist/node.js"))) + typeScriptSourceStreamsConsumer
	if err := os.WriteFile(filepath.Join(consumer, "consumer.mts"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	runTool(t, consumer, "tsc", "--strict", "--skipLibCheck", "--target", "ES2022", "--lib", "ES2022,ESNext.Disposable,ESNext.AsyncIterable,DOM,DOM.AsyncIterable", "--module", "NodeNext", "--moduleResolution", "NodeNext", "--noEmit", "consumer.mts")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "consumer.mts")
	cmd.Dir, cmd.Env = consumer, append(driver.ToolEnv(), fmt.Sprintf("THROUGHPUT_MIB=%d", throughputMiB(256)))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, output)
	}
	t.Logf("%s", output)
}

const typeScriptSourceStreamsConsumer = `
// @ts-ignore: Node built-in without bundled declarations.
import { Readable } from 'node:stream';
declare const process: { env: Record<string, string | undefined>; memoryUsage(): { rss: number } };
function assert(ok: unknown, message: string): asserts ok { if (!ok) throw new Error(message); }
const text = (b: Uint8Array | null) => new TextDecoder().decode(b ?? new Uint8Array());
const pattern = (n: number, from = 0) => Uint8Array.from({ length: n }, (_, i) => (from + i) % 251);
const equal = (a: Uint8Array, b: Uint8Array) => a.length === b.length && a.every((x, i) => x === b[i]);
async function all(it: AsyncIterable<Uint8Array>): Promise<Uint8Array> {
  const parts: Uint8Array[] = [];
  let n = 0;
  for await (const c of it) { parts.push(new Uint8Array(c)); n += c.length; }
  const out = new Uint8Array(n);
  let at = 0;
  for (const p of parts) { out.set(p, at); at += p.length; }
  return out;
}
async function kind(p: Promise<unknown>): Promise<string> {
  try { await p; return ''; } catch (e) { return e instanceof g.LibraryError ? e.kind : String(e); }
}

// Returned readers: async iteration, web streams and Node streams.
{
  const r = (await g.Pattern(3n << 20n))!;
  // @ts-expect-error: an io.Reader result has no write.
  r.write;
  assert(equal(await all(r), pattern(3 << 20)), 'async iteration');
  assert(await r.read() === null, 'read at end of stream');
  await r.close();
}
{
  const r = (await g.Pattern(1000n))!;
  const parts: Uint8Array[] = [];
  await r.stream().pipeTo(new WritableStream<Uint8Array>({ write(c) { parts.push(c); } }));
  assert(equal(await all((async function* () { yield* parts; })()), pattern(1000)), 'ReadableStream');
  await r.close();
}
{
  const r = (await g.Pattern(5000n))!;
  assert(equal(await all(Readable.from(r)), pattern(5000)), 'Node Readable.from');
  await r[Symbol.asyncDispose]();
}
{
  const rs = (await g.Text('hello, world'))!;
  assert(await rs.seek(7n) === 7n, 'seek');
  assert(text(await rs.read()) === 'world', 'read after seek');
  assert(await rs.read() === null, 'EOF');
  assert(await rs.seek(-5n, 2) === 7n, 'seek from end');
  await rs.close();
}
{
  const f = (await g.Failing(10n))!;
  assert((await f.read())?.length === 10, 'data before the error');
  assert(await kind(f.read()) === 'source', 'source error');
  await f.close();
}
assert(await g.Nil() === null, 'nil reader');

// Host → source → host, then Close semantics.
{
  const host = pattern(3 << 20);
  const rc = (await g.Through(Readable.from([host.subarray(0, 1000), host.subarray(1000)])))!;
  assert(equal(await all(rc), host), 'host to source to host');
  await rc.close();
  assert(await kind(rc.read()) === 'closed', 'read after close');
  await rc.close();
}

// A returned writer and handle views.
{
  const sink = (await g.NewSink(true))!;
  const w = (await sink.Writer())!;
  const data = pattern(5 << 20);
  await new ReadableStream<Uint8Array>({ start(c) { c.enqueue(data.subarray(0, 7)); c.enqueue(data.subarray(7)); c.close(); } }).pipeTo(w.writable());
  assert(await sink.Closes() === 1n, 'closing the WritableStream closes the writer');
  assert(await kind(w.write(new Uint8Array([1]))) === 'closed', 'write after close');
  assert(await sink.io().write(new TextEncoder().encode('tail')) === 4, 'handle view write');
  assert(await sink.Count() === BigInt(data.length + 4), 'count');
  const contents = (await sink.Contents())!;
  const back = await all(contents);
  assert(equal(back.subarray(0, data.length), data) && text(back.subarray(data.length)) === 'tail', 'contents');
  await contents.close();
  await sink.close();
}
{
  const doc = (await g.Open(new TextEncoder().encode('persistent document')))!;
  const view = doc.io();
  assert(await view.seek(11n) === 11n, 'view seek');
  assert(text(await all(doc)) === 'document', 'handle async iteration');
  assert(await doc.Reads() >= 2n, 'reads');
  await view.close();
  await doc.close();
  assert(await kind(doc.Reads()) === 'closed', 'handle closed by its view');
}

// Concurrent reads on one stream are serialized.
{
  const n = 1 << 20;
  const r = (await g.Pattern(BigInt(n)))!;
  let total = 0;
  await Promise.all(Array.from({ length: 8 }, async () => { for (;;) { const c = await r.read(4096); if (c === null) return; total += c.length; } }));
  assert(total === n, 'concurrent reads ' + total);
  await r.close();
}

// Throughput: host → handle → host with bounded memory.
{
  const mib = Number(process.env.THROUGHPUT_MIB ?? '256');
  const size = mib << 20;
  const block = pattern(251 * 1024);
  async function* source() {
    for (let at = 0; at < size;) {
      const n = Math.min(128 << 10, size - at);
      const off = at % 251;
      yield block.slice(off, off + n);
      at += n;
    }
  }
  const r = (await g.Through(Readable.from(source())))!;
  let h = 2166136261, n = 0, peak = 0;
  const start = performance.now();
  for await (const c of r) {
    for (let i = 0; i < c.length; i++) h = Math.imul(h ^ c[i], 16777619) >>> 0;
    const before = n;
    n += c.length;
    if (before >> 24 !== n >> 24) peak = Math.max(peak, process.memoryUsage().rss);
  }
  const seconds = (performance.now() - start) / 1000;
  await r.close();
  assert(n === size, 'throughput size ' + n);
  let want = 2166136261;
  for (let i = 0; i < size; i++) want = Math.imul(want ^ (i % 251), 16777619) >>> 0;
  assert(h === want, 'throughput hash');
  assert(peak < 1024 << 20, 'peak rss ' + peak);
  console.log('throughput: ' + mib + ' MiB in ' + seconds.toFixed(1) + 's (' + (mib / seconds).toFixed(1) + ' MiB/s), peak rss ' + (peak >> 20) + ' MiB');
}
console.log('PASS source streams');
`
