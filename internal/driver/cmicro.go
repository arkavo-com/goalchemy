package driver

import (
	"sort"
	"strings"

	"github.com/eugenioenko/goalchemy/internal/contracts"
	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	cemit "github.com/eugenioenko/goalchemy/internal/emit/c"
	"github.com/eugenioenko/goalchemy/internal/ir"
	"github.com/eugenioenko/goalchemy/internal/link"
)

func init() { Register("c-micro", emitMicroC) }

// A private C catalog keeps the mature value lowering and pure contracts,
// without pulling desktop collectors, schedulers or native capabilities in.
func microCCatalog(cat *contracts.Catalog) *contracts.Catalog {
	c := *cat
	c.Targets = make(map[string]*contracts.Target, len(cat.Targets)+1)
	for name, target := range cat.Targets {
		c.Targets[name] = target
	}
	t := *cat.Targets["c"]
	t.Target, t.Version, t.Harness = "c-micro", "0.1.0", nil
	t.SupportFiles = []string{"types/gx.h", "types/value.c", "types/float.c", "types/ints.c", "types/map.c", "types/func.c", "types/panic.c", "types/program.c", "types/bounds.c", "types/utf8.c", "micro/micro.h", "micro/micro.c", "micro/host.c", "micro/esp32.c"}
	t.Functions = nil
	for _, f := range cat.Targets["c"].Functions {
		if strings.HasPrefix(f.ID, "core.") && !strings.HasPrefix(f.ID, "core.task.") && !strings.HasPrefix(f.ID, "core.chan.") && f.ID != "core.select" || strings.HasPrefix(f.ID, "std.errors.") {
			t.Functions = append(t.Functions, f)
		}
	}
	c.Targets[t.Target] = &t
	return &c
}

func emitMicroC(res *Result, out string) []diagnostics.Diagnostic {
	unsupported := func(message string) []diagnostics.Diagnostic {
		return []diagnostics.Diagnostic{{Code: "GCE007", Severity: diagnostics.Error, Feature: "Micro C profile", Message: message,
			Remedy: "Use a bounded sequential package main without host capabilities, or select -target c."}}
	}
	if res.IR.Cooperative {
		return unsupported("Micro C does not implement cooperative tasks or channels")
	}
	for _, typ := range res.IR.Types.All {
		if typ.U().Kind == ir.KFloat {
			return unsupported("Micro C does not yet guarantee source floating-point edge semantics on 32-bit processors")
		}
		if typ.U().Kind == ir.KArray && typ.U().Len > (1<<31)/24 {
			return unsupported("array exceeds 32-bit storage limits")
		}
		if typ.U().Kind == ir.KChan || typ.U().Kind == ir.KOpaque {
			return unsupported("Micro C does not implement channel or opaque host values")
		}
	}
	o, err := cemit.EmitMicro(res.IR, symbols(res, "c-micro"))
	if err != nil {
		return emitErr("GCE004", err.Error())
	}
	refs, files, ds := link.Plan(res.Catalog, "c-micro", o.Contracts)
	if len(ds) > 0 {
		return ds
	}
	rt, err := link.CopyRuntime(res.Catalog, "c-micro", files, out, "rt", false)
	if err != nil {
		return emitErr("GCE005", err.Error())
	}
	names, err := writeSourceArtifacts(out, o.Files)
	if err != nil {
		return emitErr("GCE005", err.Error())
	}
	var shell, cmake strings.Builder
	shell.WriteString("set --")
	cmake.WriteString("idf_component_register(SRCS\n")
	for _, name := range append(append([]string{}, names...), rt...) {
		if !strings.HasSuffix(name, ".c") || name == "rt/micro/host.c" || name == "rt/micro/esp32.c" {
			continue
		}
		shell.WriteString(" \\\n '" + strings.ReplaceAll(name, "'", "'\\''") + "'")
		cmake.WriteString("  \"../../" + name + "\"\n")
	}
	cmake.WriteString(`  "../../rt/micro/esp32.c"
  INCLUDE_DIRS "../../rt/types" "../../rt/micro" "../.."
  PRIV_REQUIRES esp_timer heap freertos)
target_compile_definitions(${COMPONENT_LIB} PRIVATE GX_MICRO=1)
target_compile_options(${COMPONENT_LIB} PRIVATE -std=gnu17 -Wno-unused-variable -Wno-unused-function -Wno-unused-label -Wno-unused-but-set-variable)
`)
	gen := map[string][]byte{
		"build.sh":                    []byte(strings.Replace(microCBuild, "@SOURCES@", shell.String(), 1)),
		"run.sh":                      []byte("#!/bin/sh\nset -eu\ncd \"$(dirname \"$0\")\"\nsh build.sh\nexec ./main\n"),
		"README.md":                   []byte(microCReadme),
		"esp-idf/main/CMakeLists.txt": []byte(cmake.String()),
		"esp-idf/CMakeLists.txt":      []byte("cmake_minimum_required(VERSION 3.16)\ninclude($ENV{IDF_PATH}/tools/cmake/project.cmake)\nidf_build_set_property(MINIMAL_BUILD ON)\nproject(goalchemy_micro)\n"),
		"esp-idf/sdkconfig.defaults":  []byte("CONFIG_ESP_MAIN_TASK_STACK_SIZE=16384\nCONFIG_COMPILER_OPTIMIZATION_SIZE=y\nCONFIG_FREERTOS_CHECK_STACKOVERFLOW_CANARY=y\n"),
	}
	for name, data := range gen {
		if err := link.WriteFile(out, name, data); err != nil {
			return emitErr("GCE005", err.Error())
		}
		names = append(names, name)
	}
	sort.Strings(names)
	if err := link.WriteManifest(out, res.Catalog, "c-micro", refs, rt, names, res.Program, o.Packages...); err != nil {
		return emitErr("GCE005", err.Error())
	}
	return nil
}

const microCBuild = `#!/bin/sh
set -eu
cd "$(dirname "$0")"
@SOURCES@
${CC:-cc} -std=c17 -D_POSIX_C_SOURCE=200809L -DGX_MICRO=1 ${CFLAGS:--Os} ${CPPFLAGS:-} -Irt/types -Irt/micro -I. "$@" rt/micro/host.c ${LDLIBS:-} -lm -o main >&2
`

const microCReadme = `# Goalchemy Micro C

Sequential C17 output with a bounded, zeroed arena; no Boehm GC or pthreads.
Source int/uint remain 64-bit on 32-bit ESP32. This is an ESP-IDF profile,
not the proprietary mikroC compiler. No floats, host capabilities or goroutines.

Host smoke test: sh run.sh (CC, CFLAGS and CPPFLAGS supported).
Set GOALCHEMY_MICRO_METRICS to a JSON output filename for time and arena usage.

ESP-IDF (tested with 5.5.1): source its export.sh, then:

    cd esp-idf
    idf.py set-target esp32c3
    idf.py build
    idf.py size
    idf.py -p PORT flash monitor

Other ESP32 families can be selected using set-target with their toolchains.
The adapter prints execution time, arena peak/capacity, allocations, source
depth, minimum free task stack and system heap. The system heap minimum is
since boot, not per invocation. UART printing is included in elapsed time.

The default arena is 64 KiB of static RAM and the main task stack is 16 KiB.
Change GX_MICRO_ARENA_BYTES/GX_MICRO_MAX_DEPTH compile definitions in the
component CMakeLists.txt and the stack size through idf.py menuconfig.
The default source call depth limit is 16; it does not bound C stack bytes.
Avoid large local arrays and measure stack headroom on your actual device.

Include rt/micro/micro.h to embed goalchemy_micro_run in your own task.
Pass max_align_t-aligned storage and an output callback. The call is globally
serialized and starts fresh source globals each time. No source values may
escape it. Every allocation remains live until return, including temporary
strings, map keys and call arguments. Repeated allocating loops exhaust the
arena; split work into bounded invocations. OOM/depth faults return status 3
without allocating, running defers, or terminating the firmware. Unrecovered
source panic returns 2; normal return is 0. See micro.h for the full contract.
`
