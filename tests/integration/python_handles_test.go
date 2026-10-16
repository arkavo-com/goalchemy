package integration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
)

func TestGeneratedPythonHandles(t *testing.T) {
	res := buildHandles(t)
	parent, consumer := t.TempDir(), t.TempDir()
	if ds := driver.Emit("python", res, filepath.Join(parent, "handles")); diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	if err := os.WriteFile(filepath.Join(consumer, "consumer.py"), []byte(pythonHandlesConsumer), 0600); err != nil {
		t.Fatal(err)
	}
	runTool(t, consumer, "python3", "consumer.py", parent)
}

const pythonHandlesConsumer = `import sys, gc, time, threading, asyncio
sys.path.insert(0, sys.argv[1])
import handles as g
rt = g.main.rt

def get(op): return op.result(30)
def kind(op):
    try:
        op.result(30)
        return ''
    except rt.LibraryFailure as e:
        return e.kind

# Instance state, init once, sentinel identity, native key and identity.
c = get(g.New(b'a'))
for i in range(1, 4):
    assert get(c.Add(2)) == 2 * i, 'persistent field'
assert kind(c.Add(-1)) == 'source', 'source error'
s = get(c.Snap())
assert s == {'Name': b'a', 'N': 6, 'Inits': 1, 'Created': 1, 'Closes': 0, 'LastNeg': True}, s
assert get(c.PublicKey()).startswith(b'-----BEGIN PUBLIC KEY-----'), 'native key survives its creating call'
for _ in range(3):
    assert get(g.Inits()) == 1, 'free functions keep fresh globals'
assert get(c.Self()) is c, 'identity'
child = get(c.Child())
assert get(child.Bump()) == 7 and get(child.Parent()) is c and get(c.Snap())['N'] == 7, 'derived handle'
get(child.close())
assert kind(child.Bump()) == 'closed', 'closed derived handle'
assert get(c.Add(1)) == 8, 'closing a derived handle keeps the instance'
assert get(g.Nil()) is None, 'nil handle'
get(c.close())

# Handle parameters.
a = get(g.New(b'a')); b = get(a.Sibling(b'b')); other = get(g.New(b'other'))
get(a.Add(2)); get(b.Add(3))
assert get(g.Sum(a, b)) == 5, 'handles of one instance'
s = get(b.Snap())
assert s['Created'] == 2 and s['Inits'] == 1, 'sibling joins the instance'
assert kind(g.Sum(a, other)) == 'instance_mismatch', 'mixed instances'
assert get(g.Sum(None, None)) == -1, 'nil handle arguments'
assert kind(g.Sum(object(), None)) == 'invalid_argument', 'foreign object'
for h in (a, b, other): get(h.close())

# Value receivers run on a copy of the handle's object.
c = get(g.New(b'a'))
assert get(c.Name()) == b'a', 'value receiver on a handle with private state'
get(c.close())
p = get(g.NewCoord(1, 2))
assert get(p.Total()) == 3 and get(p.Shift(5)) == 6 and get(p.Total()) == 3, 'value receiver must not change the handle'
m = get(p.Moved(10))
assert m == {'X': 11, 'Y': 2} and get(p.Total()) == 3, m
assert get(p.Less(m)) is True and get(p.Less(get(p.Moved(-1)))) is False, 'value parameter of the handle type'
assert get(p.Move(1)) == 2 and get(p.Total()) == 4, 'pointer receiver changes the handle'
get(p.close())
assert kind(get(g.NewCoord(-1, 0)).close()) == 'source', 'value receiver Close'

# Calls from several host threads are serialized; instances are independent.
a = get(g.New(b'a')); b = get(g.New(b'b'))
errors = []
def worker(h):
    try:
        for _ in range(50): get(h.Add(1))
    except BaseException as e: errors.append(e)
threads = [threading.Thread(target=worker, args=(a if i % 2 == 0 else b,)) for i in range(8)]
for t in threads: t.start()
for t in threads: t.join(120)
assert not errors, errors
sa, sb = get(a.Snap()), get(b.Snap())
assert sa['N'] == 200 and sb['N'] == 200 and sa['Created'] == 1 and sb['Created'] == 1, (sa, sb)
get(a.close()); get(b.close())

# Close and context managers.
c = get(g.New(b'a')); sib = get(c.Sibling(b'bad-close'))
with c: pass
get(c.close())
assert kind(c.Add(1)) == 'closed', 'call after close'
assert get(sib.Snap())['Closes'] == 1, 'source Close ran once'
assert kind(sib.close()) == 'source', 'source Close error'
assert kind(sib.Snap()) == 'closed', 'failed Close still releases'
async def scoped():
    async with get(g.New(b'async')) as h:
        assert (await h.Add(4)) == 4
asyncio.run(scoped())

# Cancellation and Close while pending.
c = get(g.New(b'a'))
op = c.Wait(100000); time.sleep(0.1); assert op.cancel()
assert kind(op) == 'canceled', 'active cancellation'
assert get(c.Add(1)) == 1, 'usable after cancellation'
waited = c.Wait(100000); time.sleep(0.1)
queued = c.Add(1); time.sleep(0.05)
get(c.close())
assert kind(waited) == 'canceled', 'active call canceled by close'
assert kind(queued) == 'closed', 'queued call failed by close'

# Poisoning.
c = get(g.New(b'a'))
assert kind(c.Boom()) == 'source_panic', 'panic'
assert kind(c.Add(1)) == 'poisoned', 'poisoned instance'
get(c.close())
assert kind(c.Add(1)) == 'closed', 'closed poisoned'

# Warnings: abandoned goroutines and unclosed handles.
warnings = []
rt.set_library_warn(warnings.append)
c = get(g.New(b'a'))
get(c.Leak())
assert any('goroutine' in m for m in warnings), warnings
assert get(c.Add(1)) == 1, 'usable after abandoning goroutines'
c = None
for _ in range(100):
    gc.collect()
    if any('Counter handle was not closed' in m for m in warnings): break
    time.sleep(0.02)
assert any('Counter handle was not closed' in m for m in warnings), warnings
print('PASS handles')
`
