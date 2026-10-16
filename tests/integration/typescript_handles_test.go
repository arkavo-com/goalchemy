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

func runTool(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Env = dir, driver.ToolEnv()
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", name, err, data)
	}
}

func TestGeneratedTypeScriptHandles(t *testing.T) {
	res := buildHandles(t)
	out, consumer := t.TempDir(), t.TempDir()
	if ds := driver.Emit("typescript", res, out); diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	runTool(t, out, "tsc", "-p", ".")
	source := fmt.Sprintf("import * as g from %q;\nimport * as rt from %q;\n", filepath.ToSlash(filepath.Join(out, "dist/main.js")), filepath.ToSlash(filepath.Join(out, "dist/rt/runtime/library.js"))) + typeScriptHandlesConsumer
	if err := os.WriteFile(filepath.Join(consumer, "consumer.mts"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	runTool(t, consumer, "tsc", "--strict", "--skipLibCheck", "--target", "ES2022", "--lib", "ES2022,ESNext.Disposable,DOM", "--module", "NodeNext", "--moduleResolution", "NodeNext", "--noEmit", "consumer.mts")
	runTool(t, consumer, "node", "--expose-gc", "consumer.mts")
}

const typeScriptHandlesConsumer = `
declare const gc: () => void;
function assert(ok: unknown, message: string): asserts ok { if (!ok) throw new Error(message); }
async function kind(p: Promise<unknown>): Promise<string> {
  try { await p; return ''; } catch (e) { return e instanceof g.LibraryError ? e.kind : 'other:' + String(e); }
}
const warnings: string[] = [];

// Instance state, init once, sentinel identity, native key and identity.
{
  const c = (await g.New('a'))!;
  for (let i = 1n; i <= 3n; i++) assert(await c.Add(2n) === 2n * i, 'persistent field');
  assert(await kind(c.Add(-1n)) === 'source', 'source error');
  const s = await c.Snap();
  assert(s.Name === 'a' && s.N === 6n && s.Inits === 1n && s.Created === 1n && s.LastNeg === true, 'instance globals ' + JSON.stringify(s, (_k, v) => typeof v === 'bigint' ? String(v) : v));
  assert((await c.PublicKey()).startsWith('-----BEGIN PUBLIC KEY-----'), 'native key survives its creating call');
  for (let i = 0; i < 3; i++) assert(await g.Inits() === 1n, 'free functions keep fresh globals');
  assert(await c.Self() === c, 'identity');
  const child = (await c.Child())!;
  assert(await child.Bump() === 7n && await child.Parent() === c && (await c.Snap()).N === 7n, 'derived handle');
  await child.close();
  assert(await kind(child.Bump()) === 'closed', 'closed derived handle');
  assert(await c.Add(1n) === 8n, 'closing a derived handle keeps the instance');
  assert(await g.Nil() === null, 'nil handle');
  await c.close();
}

// Handle parameters.
{
  const a = (await g.New('a'))!, b = (await a.Sibling('b'))!, other = (await g.New('other'))!;
  await a.Add(2n); await b.Add(3n);
  assert(await g.Sum(a, b) === 5n, 'handles of one instance');
  const s = await b.Snap();
  assert(s.Created === 2n && s.Inits === 1n, 'sibling joins the instance');
  assert(await kind(g.Sum(a, other)) === 'instance_mismatch', 'mixed instances');
  assert(await g.Sum(null, null) === -1n, 'nil handle arguments');
  assert(await kind(g.Sum({} as g.Counter, null)) === 'invalid_argument', 'foreign object');
  await a.close(); await b.close(); await other.close();
}

// Value receivers run on a copy of the handle's object.
{
  const c = (await g.New('a'))!;
  assert(await c.Name() === 'a', 'value receiver on a handle with private state');
  await c.close();
  const p = (await g.NewCoord(1n, 2n))!;
  assert(await p.Total() === 3n && await p.Shift(5n) === 6n && await p.Total() === 3n, 'value receiver must not change the handle');
  const m = await p.Moved(10n);
  assert(m.X === 11n && m.Y === 2n && await p.Total() === 3n, 'value result is a copy');
  assert(await p.Less(m) === true && await p.Less(await p.Moved(-1n)) === false, 'value parameter of the handle type');
  assert(await p.Move(1n) === 2n && await p.Total() === 4n, 'pointer receiver changes the handle');
  await p.close();
  assert(await kind((await g.NewCoord(-1n, 0n))!.close()) === 'source', 'value receiver Close');
}

// Interleaved calls on one instance are serialized; instances are independent.
{
  const a = (await g.New('a'))!, b = (await g.New('b'))!;
  await Promise.all(Array.from({ length: 400 }, (_, i) => (i % 2 ? b : a).Add(1n)));
  const sa = await a.Snap(), sb = await b.Snap();
  assert(sa.N === 200n && sb.N === 200n && sa.Created === 1n && sb.Created === 1n, 'serialized calls');
  await a.close(); await b.close();
}

// Close.
{
  const c = (await g.New('a'))!, sib = (await c.Sibling('bad-close'))!;
  await c.close(); await c.close();
  assert(await kind(c.Add(1n)) === 'closed', 'call after close');
  assert((await sib.Snap()).Closes === 1n, 'source Close ran once');
  assert(await kind(sib.close()) === 'source', 'source Close error');
  assert(await kind(sib.Snap()) === 'closed', 'failed Close still releases');
}

// Cancellation and Close while pending.
{
  const c = (await g.New('a'))!;
  assert(await kind(c.Wait(100000n, { signal: AbortSignal.timeout(20) })) === 'canceled', 'active cancellation');
  assert(await c.Add(1n) === 1n, 'usable after cancellation');
  const waited = kind(c.Wait(100000n));
  await new Promise(r => setTimeout(r, 100));
  const queued = kind(c.Add(1n));
  await c.close();
  assert(await waited === 'canceled', 'active call canceled by close');
  assert(await queued === 'closed', 'queued call failed by close');
}

// Poisoning.
{
  const c = (await g.New('a'))!;
  assert(await kind(c.Boom()) === 'source_panic', 'panic');
  assert(await kind(c.Add(1n)) === 'poisoned', 'poisoned instance');
  await c.close();
  assert(await kind(c.Add(1n)) === 'closed', 'closed poisoned');
}

// Warnings: abandoned goroutines and unclosed handles.
{
  rt.setLibraryWarn((m: string) => { warnings.push(m); });
  let c: g.Counter | null = (await g.New('a'))!;
  await c.Leak();
  assert(warnings.some(m => m.includes('goroutine')), 'abandoned goroutine warning');
  assert(await c.Add(1n) === 1n, 'usable after abandoning goroutines');
  c = null;
  for (let i = 0; i < 50 && !warnings.some(m => m.includes('Counter handle was not closed')); i++) {
    gc();
    await new Promise(r => setTimeout(r, 20));
  }
  assert(warnings.some(m => m.includes('Counter handle was not closed')), 'finalizer warning ' + warnings.join('|'));
}
console.log('PASS handles');
`
