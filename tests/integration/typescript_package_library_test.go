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
	"github.com/eugenioenko/goalchemy/internal/subset"
	"github.com/eugenioenko/goalchemy/internal/testutil"
)

func TestGeneratedTypeScriptPackageLibrary(t *testing.T) {
	fixture, err := filepath.Abs("testdata/package_library/api")
	if err != nil {
		t.Fatal(err)
	}
	res, ds := driver.Build(context.Background(), driver.Options{Dir: fixture, Gate: subset.Cooperative})
	if diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	run := func(t *testing.T, dir, name string, args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir, cmd.Env = dir, driver.ToolEnv()
		if data, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", name, err, data)
		} else {
			t.Log(string(data))
		}
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, compact := range []bool{false, true} {
		t.Run(fmt.Sprintf("compact=%v", compact), func(t *testing.T) {
			out, consumer := t.TempDir(), t.TempDir()
			if ds := driver.EmitWithOptions("typescript", res, out, driver.EmitOptions{Names: testutil.Names(compact)}); diagnostics.HasErrors(ds) {
				t.Fatal(ds)
			}
			// Build the emitted configuration unchanged and type-check a separate
			// consumer against its public .d.ts boundary before native execution.
			run(t, out, "tsc", "-p", ".")
			source := fmt.Sprintf("import * as g from %q;\n", filepath.ToSlash(filepath.Join(out, "dist/main.js"))) + typeScriptPackageLibraryConsumer
			if err := os.WriteFile(filepath.Join(consumer, "consumer.mts"), []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			run(t, consumer, "tsc", "--strict", "--skipLibCheck", "--target", "ES2022", "--module", "NodeNext", "--moduleResolution", "NodeNext", "--noEmit", "consumer.mts")
			run(t, consumer, "node", "consumer.mts")
			browserSuite := "import * as g from './main.js';\nexport async function runLibrarySuite(): Promise<void> {\n" + typeScriptPackageLibraryConsumer + "\n}\n"
			if err := os.WriteFile(filepath.Join(out, "library-suite.ts"), []byte(browserSuite), 0600); err != nil {
				t.Fatal(err)
			}
			run(t, out, "tsc", "-p", ".")
			run(t, root, "node", "targets/typescript/tests/library_browser.mjs", out)
		})
	}
}

const typeScriptPackageLibraryConsumer = `
function assert(ok: unknown,message: string): asserts ok { if(!ok)throw new Error(message); }
function same(bytes:Uint8Array|null|undefined,want:number[]):boolean { return bytes!==null&&bytes!==undefined&&bytes.length===want.length&&want.every((n,i)=>bytes[i]===n); }
const order="order-var/order-init/state-var/state-init/api-var/api-init";
assert(Object.keys(g).sort().join(',')==='ErrorGlobal,Export,LibraryError,setLogHandler','stable public exports');
const input:g.Value={Count:2n,Bytes:new Uint8Array([7]),Order:'caller'};
const first=await g.Export(input);
assert(first.Count===29n&&first.Order===order&&same(first.Bytes,[3,255,128]),'package init/global closure/promoted method');
assert(input.Count===2n&&input.Order==='caller'&&input.Bytes![0]===7,'caller input retained');
let saved:g.LibraryError|undefined;
try{await g.ErrorGlobal();}catch(error){assert(error instanceof g.LibraryError&&error.kind==='source','typed source error');saved=error;}
assert(saved!==undefined&&saved.fields.Code==='saved'&&same(saved.fields.Bytes as Uint8Array,[9]),'source error fields');
await Promise.all(Array.from({length:12},async()=>{const next=await g.Export({Count:2n});assert(next.Count===29n&&next.Order===order&&same(next.Bytes,[3,255,128]),'fresh overlapping lifecycle');}));
assert(same(first.Bytes,[3,255,128])&&same(saved.fields.Bytes as Uint8Array,[9]),'retained result/error across resets');
first.Bytes![0]=99;(saved.fields.Bytes as Uint8Array)[0]=88;
const next=await g.Export({Count:2n});assert(same(next.Bytes,[3,255,128]),'result owns storage');
try{await g.ErrorGlobal();throw new Error('missing failure');}catch(error){assert(error instanceof g.LibraryError&&same(error.fields.Bytes as Uint8Array,[9]),'error owns storage');}
// A canceled queued call cannot reset a previous or following call's globals.
const aborted=new AbortController();aborted.abort('caller');
try{await g.Export({Count:2n},{signal:aborted.signal});throw new Error('missing cancellation');}catch(error){assert(error instanceof g.LibraryError&&error.kind==='canceled','pre-canceled lifecycle');}
assert((await g.Export({Count:2n})).Count===29n,'fresh lifecycle after cancellation');
console.log('PASS independent multipackage declarations/storage/reset/retained values');
`
