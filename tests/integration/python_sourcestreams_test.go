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

func TestGeneratedPythonSourceStreams(t *testing.T) {
	res := buildSourceStreams(t)
	parent, consumer := t.TempDir(), t.TempDir()
	if ds := driver.Emit("python", res, filepath.Join(parent, "sourcestreams")); diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	if err := os.WriteFile(filepath.Join(consumer, "consumer.py"), []byte(pythonSourceStreamsConsumer), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", "consumer.py", parent)
	cmd.Dir, cmd.Env = consumer, append(driver.ToolEnv(), fmt.Sprintf("THROUGHPUT_MIB=%d", throughputMiB(256)))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("python3: %v\n%s", err, output)
	}
	t.Logf("%s", output)
}

const pythonSourceStreamsConsumer = `import io, os, shutil, sys, threading, time, zlib
sys.path.insert(0, sys.argv[1])
import sourcestreams as g
rt = g.main.rt

def get(op): return op.result(120)
def kind(fn):
    try:
        fn()
        return ''
    except rt.LibraryFailure as e:
        return e.kind

def rss():
    with open('/proc/self/status') as f: return int(f.read().split('VmRSS:')[1].split()[0]) >> 10

def pattern(n, start=0): return bytes((start + i) % 251 for i in range(n))
big = pattern(3 << 20)

# Returned readers are file-like objects.
with get(g.Pattern(3 << 20)) as r:
    assert isinstance(r, io.RawIOBase) and r.readable() and not r.writable() and not r.seekable()
    assert r.read() == big, 'readall'
    assert r.read(10) == b'', 'EOF'
assert r.closed

with get(g.Pattern(1000)) as r:
    buf = bytearray(300)
    assert r.readinto(buf) == 300 and bytes(buf) == pattern(300), 'readinto'
    out = io.BytesIO()
    shutil.copyfileobj(r, out)
    assert out.getvalue() == pattern(700, 300), 'copyfileobj'

with get(g.Pattern(5000)) as r:
    assert io.BufferedReader(r).read() == pattern(5000), 'BufferedReader'

with get(g.Text(b'hello, world')) as rs:
    assert rs.seekable() and rs.seek(7) == 7 and rs.tell() == 7, 'seek'
    assert rs.read() == b'world', 'read after seek'
    assert rs.seek(-5, io.SEEK_END) == 7, 'seek from end'

with get(g.Failing(10)) as f:
    assert len(f.read(64)) == 10, 'data before the error'
    assert kind(lambda: f.read(64)) == 'source', 'source error'

assert get(g.Nil()) is None, 'nil reader'

# Host → source → host, then Close semantics.
rc = get(g.Through(io.BytesIO(big)))
assert rc.read() == big, 'host to source to host'
rc.close()
assert kind(lambda: rc.readinto(bytearray(4))) == 'closed', 'read after close'
rc.close()

# A returned writer and handle views.
with get(g.NewSink(True)) as sink:
    w = get(sink.Writer())
    assert w.writable() and not w.readable()
    data = pattern(5 << 20)
    shutil.copyfileobj(io.BytesIO(data), w, 1 << 20)
    w.close()
    assert get(sink.Closes()) == 1, 'writer close runs the source Close'
    with sink.as_file() as view:
        assert view.write(b'tail') == 4, 'handle view write'
    assert kind(lambda: get(sink.Count())) == 'closed', 'closing the view closes the handle'

with get(g.NewSink(True)) as sink:
    sink.as_file().write(b'abc')
    with get(sink.Contents()) as contents:
        assert contents.read() == b'abc', 'contents'

doc = get(g.Open(b'persistent document'))
view = doc.as_file()
assert view.seek(11) == 11 and view.read() == b'document', 'handle view read'
assert get(doc.Reads()) >= 2, 'reads'
view.close()
get(doc.close())
assert kind(lambda: get(doc.Reads())) == 'closed', 'handle closed by its view'

# Concurrent reads on one stream are serialized.
n = 1 << 20
with get(g.Pattern(n)) as r:
    total = [0]
    lock = threading.Lock()
    def worker():
        while True:
            c = r.read(4096)
            with lock: total[0] += len(c)
            if not c: return
    threads = [threading.Thread(target=worker) for _ in range(8)]
    for t in threads: t.start()
    for t in threads: t.join()
    assert total[0] == n, 'concurrent reads %d' % total[0]

# Throughput: host → handle → host with bounded memory.
class Source(io.RawIOBase):
    def __init__(self, size):
        self.left = size; self.at = 0
        self.block = pattern(251 * 1024)
    def readable(self): return True
    def readinto(self, b):
        n = min(len(b), self.left)
        off = self.at % 251
        b[:n] = self.block[off:off + n]
        self.left -= n; self.at += n
        return n

mib = int(os.environ.get('THROUGHPUT_MIB', '256'))
size = mib << 20
start = time.time()
with get(g.Through(Source(size))) as r:
    count = crc = 0
    base = peak = rss()
    while True:
        c = r.read(1 << 20)
        if not c: break
        crc = zlib.crc32(c, crc)
        if (count >> 24) != ((count + len(c)) >> 24): peak = max(peak, rss())
        count += len(c)
elapsed = time.time() - start
want = 0
src = Source(size)
buf = bytearray(64 << 10)
while True:
    k = src.readinto(buf)
    if not k: break
    want = zlib.crc32(memoryview(buf)[:k], want)
assert count == size and crc == want, 'throughput copy %d' % count
assert peak - base < 256, 'rss grew from %d to %d MiB' % (base, peak)
print('throughput: %d MiB in %.1fs (%.1f MiB/s), rss %d MiB at start, peak %d MiB' % (mib, elapsed, mib / elapsed, base, peak))
print('PASS source streams')
`
