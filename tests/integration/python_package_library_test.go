package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
	"github.com/eugenioenko/goalchemy/internal/subset"
	"github.com/eugenioenko/goalchemy/internal/testutil"
)

func TestGeneratedPythonPackageLibrary(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	// Forward the common package_library API, adding only a real native callback
	// barrier so queued/active cancellation is deterministic across native threads.
	fixture := `package probe
import (
 api "packageconsumer/api"
 "github.com/eugenioenko/goalchemy/lib/context"
 "github.com/eugenioenko/goalchemy/lib/callback"
)
func Export(v api.Value)(api.Value,error){return api.Export(v)}
func ErrorGlobal()([]byte,error){return api.ErrorGlobal()}
func Hold(ctx context.Context)([]byte,error){return callback.Request(ctx,"hold",nil)}
`
	for name, data := range map[string]string{"go.mod": fmt.Sprintf("module packageconsumer\n\ngo 1.25\nrequire github.com/eugenioenko/goalchemy v0.0.0\nreplace github.com/eugenioenko/goalchemy => %s\n", root), "library.go": fixture} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Copy the common fixture into this source module, retaining its public
	// API while making all three dependencies included source for the wrapper.
	for _, pkg := range []string{"api", "state", "order"} {
		data, err := os.ReadFile(filepath.Join(root, "tests/integration/testdata/package_library", pkg, pkg+".go"))
		if err != nil {
			t.Fatal(err)
		}
		data = []byte(strings.ReplaceAll(string(data), "github.com/eugenioenko/goalchemy/tests/integration/testdata/package_library/", "packageconsumer/"))
		dir := filepath.Join(source, pkg)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, pkg+".go"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	res, ds := driver.Build(context.Background(), driver.Options{Dir: source, Gate: subset.Cooperative})
	if diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	for _, compact := range []bool{false, true} {
		t.Run(fmt.Sprintf("compact=%v", compact), func(t *testing.T) {
			parent, consumer := t.TempDir(), t.TempDir()
			out := filepath.Join(parent, "probe")
			if ds := driver.EmitWithOptions("python", res, out, driver.EmitOptions{Names: testutil.Names(compact)}); diagnostics.HasErrors(ds) {
				t.Fatal(ds)
			}
			if err := os.WriteFile(filepath.Join(consumer, "consumer.py"), []byte(pythonPackageLibraryConsumer), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, "python3", "consumer.py", parent)
			cmd.Dir, cmd.Env = consumer, driver.ToolEnv()
			if data, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("independent CPython package consumer: %v\n%s", err, data)
			} else {
				t.Log(string(data))
			}
		})
	}
}

const pythonPackageLibraryConsumer = `import sys,importlib,threading,asyncio,copy,json,pathlib
sys.path.insert(0,sys.argv[1])
p=importlib.import_module('probe');rt=p.main.rt
manifest=json.loads((pathlib.Path(sys.argv[1])/'probe'/'goalchemy.manifest.json').read_text())
modules=[importlib.import_module('probe.'+item['files'][0][:-3]) for item in manifest['source_packages']]
# Native imported modules share the same helper registry and runtime object.
assert len(modules)==4 and all(module.rt is rt for module in modules)
assert all(module._types is p.main._types for module in modules)
assert callable(p.Export) and callable(p.ErrorGlobal) and callable(p.Hold)
order=b'order-var/order-init/state-var/state-init/api-var/api-init'
def result(op):return op.result(10)
def failure(op,kind):
 try:result(op);raise AssertionError('missing '+kind)
 except rt.LibraryFailure as error:
  assert error.kind==kind,(error.kind,kind)
  return error
input={'Count':2,'Bytes':bytearray([7]),'Order':b'caller'}
first=result(p.Export(input))
assert first=={'Count':29,'Bytes':b'\x03\xff\x80','Order':order}
assert input=={'Count':2,'Bytes':bytearray([7]),'Order':b'caller'}
saved=failure(p.ErrorGlobal(),'source')
assert saved.fields=={'Code':b'saved','Bytes':b'\x09'}
previous=copy.deepcopy(saved.fields)
# Submit simultaneously from foreign caller threads, using real library owners.
barrier=threading.Barrier(13);errors=[]
def worker():
 try:
  barrier.wait(10)
  assert result(p.Export({'Count':2}))==first
 except BaseException as error:errors.append(error)
threads=[threading.Thread(target=worker) for _ in range(12)]
for thread in threads:thread.start()
barrier.wait(10)
for thread in threads:thread.join(15);assert not thread.is_alive()
assert not errors,errors
assert first=={'Count':29,'Bytes':b'\x03\xff\x80','Order':order} and saved.fields==previous
# Mutating returned containers/errors cannot change the next source lifecycle.
first['Bytes']=b'changed';saved.fields['Bytes']=b'changed'
assert result(p.Export({'Count':2}))['Bytes']==b'\x03\xff\x80'
assert failure(p.ErrorGlobal(),'source').fields['Bytes']==b'\x09'
started=threading.Event();stopped=threading.Event();release=threading.Event();released=threading.Event()
ProviderRejected=importlib.import_module('probe.rt.runtime.lib_callback_request').ProviderRejected
def provider(request):
 started.set()
 assert request.cancellation.wait(10)
 stopped.set();assert release.wait(10);released.set()
 raise ProviderRejected('stopped')
held=p.Hold({'callbacks':{'hold':provider}})
assert started.wait(10)
queued_input={'Count':2,'Bytes':bytearray([7])}
queued=p.Export(queued_input);queued_input['Count']=99;queued_input['Bytes'][0]=99
canceled=p.ErrorGlobal();assert canceled.cancel();failure(canceled,'canceled')
assert not held.done() and not queued.done()
assert held.cancel() and stopped.wait(10)
assert not held.done() and not released.is_set() and not queued.done()
release.set();failure(held,'source');assert released.is_set()
assert result(queued)=={'Count':29,'Bytes':b'\x03\xff\x80','Order':order}
async def native_await():
 assert (await p.Export({'Count':2}))['Count']==29
asyncio.run(native_await())
print('PASS independent native package modules/init/closures/interfaces/overlap/owned results/errors/cancel/ACK')
`
