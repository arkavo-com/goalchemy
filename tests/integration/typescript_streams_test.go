package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
)

func TestGeneratedTypeScriptStreams(t *testing.T) {
	res := buildStreams(t)
	out, consumer := t.TempDir(), t.TempDir()
	if ds := driver.Emit("typescript", res, out); diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	runTool(t, out, "tsc", "-p", ".")
	source := fmt.Sprintf("import * as g from %q;\n", filepath.ToSlash(filepath.Join(out, "dist/node.js"))) + typeScriptStreamsConsumer
	if err := os.WriteFile(filepath.Join(consumer, "consumer.mts"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	runTool(t, consumer, "node", "consumer.mts")
}

const typeScriptStreamsConsumer = `
import { Readable, Writable } from 'node:stream';
function assert(ok: unknown, message: string): asserts ok { if (!ok) throw new Error(message); }
const text = (b: Uint8Array) => new TextDecoder().decode(b);
const big = new Uint8Array(3 << 20).map((_, i) => i % 251);
// Node streams in both directions, chunked over the 1 MiB host operation limit.
{
  const chunks: Uint8Array[] = [];
  const sink = new Writable({ write(chunk, _enc, cb) { chunks.push(new Uint8Array(chunk)); cb(); } });
  const n = await g.Copy(sink, Readable.from([big.subarray(0, 1000), big.subarray(1000)]));
  const joined = Buffer.concat(chunks);
  assert(n === BigInt(big.length) && joined.length === big.length && joined.every((b, i) => b === big[i]), 'node stream copy');
}
// Web streams.
{
  const parts: Uint8Array[] = [];
  const sink = new WritableStream<Uint8Array>({ write(c) { parts.push(c); } });
  const src = new ReadableStream<Uint8Array>({ start(c) { c.enqueue(new TextEncoder().encode('web ')); c.enqueue(new TextEncoder().encode('streams')); c.close(); } });
  assert(await g.Copy(sink, src) === 11n && parts.map(text).join('') === 'web streams', 'web stream copy');
}
assert(await g.Sum(new Uint8Array([1, 2, 3])) === 6n, 'bytes reader');
assert(text(await g.Section(new TextEncoder().encode('hello, world'), 7n, 5n) ?? new Uint8Array()) === 'world', 'seek on bytes');
assert(text(await g.Section(new Blob(['hello, blob']), 7n, 4n) ?? new Uint8Array()) === 'blob', 'seek on Blob');
try { await g.Section(new TextEncoder().encode('short'), 3n, 5n); throw new Error('missing EOF'); }
catch (e) { assert(e instanceof g.LibraryError && e.kind === 'source', 'io.ErrUnexpectedEOF'); }
let closed = 0;
assert(await g.Drain({ read: () => null, close: () => { closed++; } }) === 0n && closed === 1, 'close');
assert(await g.AtEOF(new Uint8Array()) === true, 'host EOF is io.EOF');
assert(await g.FirstError({ read: () => { throw new Error('disk on fire'); } }) === 'disk on fire', 'host error');
assert(await g.Nil(null) === true, 'nil stream');
const reentrant = await g.FirstError({ read: async () => { await g.Sum(new Uint8Array([1])); return null; } });
assert(reentrant.includes('reentrant'), 'reentrant host stream: ' + reentrant);
// The written buffer is a copy.
{
  const kept: Uint8Array[] = [];
  await g.Copy({ write: (d: Uint8Array) => { kept.push(d); } }, new TextEncoder().encode('copied'));
  kept[0][0] = 88;
  assert(await g.Sum(new Uint8Array([2])) === 2n, 'still works');
}
// A stream kept by an instance is used by later calls.
{
  const doc = (await g.Open(new TextEncoder().encode('persistent document')))!;
  assert(await doc.Size() === 19n, 'size');
  assert(text((await doc.ReadAt(11n, 8n))!) === 'document', 'stream kept by the instance');
  const parts: Uint8Array[] = [];
  assert(await doc.WriteTo({ write: (d: Uint8Array) => { parts.push(d.slice()); return d.length; } }) === 19n && parts.map(text).join('') === 'persistent document', 'WriteTo');
  await doc.close();
}
console.log('PASS streams');
`
