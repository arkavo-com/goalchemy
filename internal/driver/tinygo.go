package driver

import (
	"strings"

	"github.com/eugenioenko/goalchemy/internal/contracts"
	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/ir"
)

func init() { Register("tinygo", emitTinyGo) }

// TinyGo is a compiler profile of the Go backend, not a second lowering.
// Copy the catalog so emitting this profile cannot change later Go emissions.
func tinyGoCatalog(cat *contracts.Catalog) *contracts.Catalog {
	c := *cat
	c.Targets = make(map[string]*contracts.Target, len(cat.Targets)+1)
	for name, target := range cat.Targets {
		c.Targets[name] = target
	}
	t := *cat.Targets["go"]
	t.Target, t.Version, t.Harness = "tinygo", "0.1.0", nil
	t.Functions = nil
	for _, f := range cat.Targets["go"].Functions {
		if strings.HasPrefix(f.ID, "core.") || strings.HasPrefix(f.ID, "std.errors.") {
			t.Functions = append(t.Functions, f)
		}
	}
	c.Targets[t.Target] = &t
	return &c
}

func tinyGoUnsupported(message string) []diagnostics.Diagnostic {
	return []diagnostics.Diagnostic{{Code: "GCE007", Severity: diagnostics.Error, Feature: "TinyGo profile", Message: message,
		Remedy: "Use integer-only code without recover, or select -target go for reference Go semantics."}}
}

func emitTinyGo(res *Result, out string) []diagnostics.Diagnostic {
	for _, typ := range res.IR.Types.All {
		if typ.U().Kind == ir.KFloat {
			return tinyGoUnsupported("floating-point edge semantics are not yet supported by the TinyGo profile")
		}
	}
	for _, f := range res.IR.Funcs {
		if f.HasRecover {
			return tinyGoUnsupported("recover semantics differ in TinyGo, including indirect recover and panic(nil)")
		}
		for _, b := range f.Blocks {
			for _, instruction := range b.Instrs {
				message := ""
				switch i := instruction.(type) {
				case *ir.MakeInterface:
					if containsFunction(i.X.IRType(), map[*ir.Type]bool{}) {
						message = "boxing values containing functions requires function-type reflection unavailable in TinyGo"
					}
				}
				if message != "" {
					d := diagnostics.Diagnostic{Code: "GCE007", Severity: diagnostics.Error, Feature: "TinyGo profile", Message: message,
						Remedy: "Use the supported TinyGo profile or select -target go."}
					if res.IR.Fset != nil {
						p := res.IR.Fset.Position(instruction.Position())
						d.File, d.Line, d.Column = p.Filename, p.Line, p.Column
					}
					return []diagnostics.Diagnostic{d}
				}
			}
		}
	}
	return emitGoTarget(res, out, "tinygo")
}

func containsFunction(t *ir.Type, seen map[*ir.Type]bool) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true
	t = t.U()
	if t.Kind == ir.KFunc {
		return true
	}
	if containsFunction(t.Elem, seen) || containsFunction(t.Key, seen) {
		return true
	}
	for _, field := range t.Fields {
		if containsFunction(field.Type, seen) {
			return true
		}
	}
	return false
}

const tinyGoRun = `#!/bin/sh
set -eu
cd "$(dirname "$0")"
sh build.sh
case "${GOALCHEMY_TINYGO_TARGET:-wasip1}" in
 native) exec ./prog ;;
 wasip1) exec node --disable-warning=ExperimentalWarning run.mjs ;;
esac
`

const tinyGoBuild = `#!/bin/sh
set -eu
export BINARYEN_CORES="${BINARYEN_CORES:-1}"
cd "$(dirname "$0")"
case "${GOALCHEMY_TINYGO_TARGET:-wasip1}" in
 native) tinygo build -p "${GOALCHEMY_BUILD_JOBS:-1}" -opt=2 -o prog . >&2 ;;
 wasip1) tinygo build -p "${GOALCHEMY_BUILD_JOBS:-1}" -target wasip1 -opt=2 -o main.wasm . >&2 ;;
 *) echo 'GOALCHEMY_TINYGO_TARGET must be wasip1 or native' >&2; exit 2 ;;
esac
`

const tinyGoWASIRunner = `import { readFile, writeFile } from 'node:fs/promises';
import { WASI } from 'node:wasi';
import { performance } from 'node:perf_hooks';

const file = process.argv[2] ?? new URL('./main.wasm', import.meta.url);
const wasi = new WASI({ version: 'preview1', args: ['main.wasm'], env: {}, preopens: {}, returnOnExit: true });
const module = await WebAssembly.compile(await readFile(file));
const instance = await WebAssembly.instantiate(module, wasi.getImportObject());
const initial = instance.exports.memory.buffer.byteLength;
const started = performance.now();
process.exitCode = wasi.start(instance) ?? 0;
const metrics = { execution_seconds: (performance.now() - started) / 1000,
  initial_linear_memory_bytes: initial, peak_linear_memory_bytes: instance.exports.memory.buffer.byteLength };
// Wasm memory can grow but cannot shrink: the final size is its page high-water mark.
if (process.env.GOALCHEMY_WASM_METRICS) {
  await writeFile(process.env.GOALCHEMY_WASM_METRICS, JSON.stringify(metrics) + '\n');
}
`

// Named storage types preserve source int/int64 and uint/uint64 identity in
// interfaces while giving wasm32 the source profile's 64-bit arithmetic.
const tinyGoGuard = `package rt

import ("reflect"; "unicode/utf8")

type Int int64
type Uint uint64

func init() {
 RegisterType(reflect.TypeFor[Int](), "int")
 RegisterType(reflect.TypeFor[Uint](), "uint")
}

func NativeSize[N Integer](n N, message string) int {
 if n < 0 || uint64(n) > uint64(^uint(0) >> 1) { panic(PlainError(message)) }
 return int(n)
}

func DecodeRune64(s string, i Int) (int32, Int) {
 r, width := utf8.DecodeRuneInString(s[i:])
 return r, Int(width)
}
`

const tinyGoRequirements = `Requires TinyGo 0.43.0, Binaryen wasm-opt 133, a compatible Go installation,
and Node.js 22.6 or later. sh build.sh emits a WASI Preview 1 command (main.wasm);
sh run.sh builds and runs it with Node's WASI implementation. No host directories
or environment variables are exposed to the command. run.mjs can also run an
existing module: node --disable-warning=ExperimentalWarning run.mjs path.wasm.

Source int/uint use distinct 64-bit storage types on wasm32, preserving integer
width and interface identity. Allocation lengths are checked before conversion
to native sizes. GOALCHEMY_TINYGO_TARGET=native selects optional native output.
MCU targets, host capabilities, libraries, floating point, recover and boxed
function-containing values are outside this experimental profile. Runtime panic
messages can differ from Go. Cooperative programs use the frames pause model.

Build concurrency defaults to one TinyGo job and one Binaryen worker; override
GOALCHEMY_BUILD_JOBS and BINARYEN_CORES to change them. GC stays enabled. Set
GOALCHEMY_WASM_METRICS to a JSON file path to record execution time and initial/
peak linear memory bytes from the runner; linear memory is distinct from host RSS.
`
