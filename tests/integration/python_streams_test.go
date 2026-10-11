package integration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
)

func TestGeneratedPythonStreams(t *testing.T) {
	res := buildStreams(t)
	parent, consumer := t.TempDir(), t.TempDir()
	if ds := driver.Emit("python", res, filepath.Join(parent, "streams")); diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	if err := os.WriteFile(filepath.Join(consumer, "consumer.py"), []byte(pythonStreamsConsumer), 0600); err != nil {
		t.Fatal(err)
	}
	runTool(t, consumer, "python3", "consumer.py", parent)
}

const pythonStreamsConsumer = `import sys, io
sys.path.insert(0, sys.argv[1])
import streams as g
rt = g.main.rt

def get(op): return op.result(60)
def kind(op):
    try:
        op.result(60)
        return ''
    except rt.LibraryFailure as e:
        return e.kind

big = bytes(i % 251 for i in range(3 << 20))
out = io.BytesIO()
assert get(g.Copy(out, io.BytesIO(big))) == len(big) and out.getvalue() == big, 'chunked copy'
assert get(g.Sum(b'\x01\x02\x03')) == 6, 'bytes reader'
assert get(g.Section(io.BytesIO(b'hello, world'), 7, 5)) == b'world', 'seek'
assert kind(g.Section(b'short', 3, 5)) == 'source', 'io.ErrUnexpectedEOF'

class Closing(io.BytesIO):
    closes = 0
    def close(self):
        Closing.closes += 1
        super().close()
assert get(g.Drain(Closing(b'abc'))) == 3 and Closing.closes == 1, 'close'
assert get(g.AtEOF(b'')) is True, 'host EOF is io.EOF'

class Failing:
    def read(self, n): raise OSError('disk on fire')
assert get(g.FirstError(Failing())) == b'disk on fire', 'host error'
assert get(g.Nil(None)) is True, 'nil stream'

class Reentrant:
    def read(self, n):
        get(g.Sum(b'\x01'))
        return b''
assert b'reentrant' in get(g.FirstError(Reentrant())), 'reentrant host stream'

class Keeper:
    def __init__(self): self.kept = []
    def write(self, b):
        self.kept.append(b)
        return len(b)
k = Keeper()
get(g.Copy(k, b'copied'))
assert k.kept == [b'copied'], k.kept
assert kind(g.Sum(object())) == 'invalid_argument', 'object without read'

doc = get(g.Open(io.BytesIO(b'persistent document')))
assert get(doc.Size()) == 19, 'size'
assert get(doc.ReadAt(11, 8)) == b'document', 'stream kept by the instance'
out = io.BytesIO()
assert get(doc.WriteTo(out)) == 19 and out.getvalue() == b'persistent document', 'WriteTo'
get(doc.close())
print('PASS streams')
`
