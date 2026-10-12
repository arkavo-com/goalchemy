package swift_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
	swiftemit "github.com/eugenioenko/goalchemy/internal/emit/swift"
	"github.com/eugenioenko/goalchemy/internal/subset"
	"github.com/eugenioenko/goalchemy/internal/testutil"
)

func buildSwift(t *testing.T, source string) *driver.Result {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, text := range map[string]string{"go.mod": fmt.Sprintf("module swiftprobe\n\ngo 1.25\nrequire github.com/eugenioenko/goalchemy v0.0.0\nreplace github.com/eugenioenko/goalchemy => %q\n", root), "probe.go": source} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	result, ds := driver.Build(context.Background(), driver.Options{Dir: dir, Gate: subset.Cooperative})
	if diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	return result
}

func TestNamingModes(t *testing.T) {
	result := buildSwift(t, `package main
 import "github.com/eugenioenko/goalchemy/lib/checksum"
 type State struct { value, clone, café int }
 var counter = 2
 func (s State) Amount() int { return s.value+s.clone+s.café }
 func calculate(input int) int {
   class := State{value:input,clone:3,café:5};address:=&class.value;*address+=counter
   fn:=func()int{class:=1;return class};var reader interface{Amount()int}=class;bound:=class.Amount
   queue:=make(chan int,1);queue<-reader.Amount()+bound()+fn();return <-queue
 }
 func main(){println(calculate(1),checksum.CRC32IEEE([]byte("123456789")))}
 `)
	symbols := make([]string, len(result.IR.Funcs))
	for i, f := range result.IR.Funcs {
		symbols[i] = f.Sym
	}
	for _, compact := range []bool{false, true} {
		t.Run(fmt.Sprintf("compact=%v", compact), func(t *testing.T) {
			result.IR.CompactNames = compact
			first, err := swiftemit.Emit(result.IR, map[string]string{"lib.checksum.crc32_ieee": "Custom.crc32"})
			if err != nil {
				t.Fatal(err)
			}
			second, err := swiftemit.Emit(result.IR, map[string]string{"lib.checksum.crc32_ieee": "Custom.crc32"})
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(generatedSource(first), generatedSource(second)) {
				t.Fatal("nondeterministic emission")
			}
			if !strings.Contains(string(generatedSource(first)), "Custom.crc32(") {
				t.Fatal("declared runtime symbol ignored")
			}
			if !strings.Contains(string(generatedSource(first)), "task.owner.safepoint()") {
				t.Fatal("source frames lack loop collection safepoints")
			}
			mapped := false
			for _, file := range first.Files {
				mapped = mapped || len(file.Lines) > 0
			}
			if !mapped {
				t.Fatal("missing source position map")
			}
			for i, f := range result.IR.Funcs {
				if f.Sym != symbols[i] {
					t.Fatal("emission mutated IR function identity")
				}
			}
			source := string(generatedSource(first))
			if !compact && (!strings.Contains(source, "v_input") || !strings.Contains(source, "_calculate_")) {
				t.Fatal("readable names absent")
			}
			if compact && (strings.Contains(source, "v_input") || strings.Contains(source, "_calculate_")) {
				t.Fatal("compact names ignored")
			}
			dir := t.TempDir()
			if ds := driver.EmitWithOptions("swift", result, dir, driver.EmitOptions{CompactNames: compact}); diagnostics.HasErrors(ds) {
				t.Fatal(ds)
			}
			got, err := testutil.Runners["swift"](dir)
			if err != nil {
				t.Fatal(err)
			}
			if got.Exit != 0 || got.Stderr != "23 3421780262\n" || got.Stdout != "" {
				t.Fatalf("source behavior changed: %s", got)
			}
		})
	}
}

func TestReservedPublicNamesRenamed(t *testing.T) {
	for _, compact := range []bool{false, true} {
		for _, name := range []string{"Error", "String", "Array", "Result", "Data", "Task", "GoalchemyKey", "GBuffer", "GCryptoKey", "DispatchTime"} {
			t.Run(fmt.Sprintf("compact=%v/%s", compact, name), func(t *testing.T) {
				result := buildSwift(t, "package boundary\ntype "+name+" struct { Value int };func Export(v "+name+")("+name+",error){return v,nil}")
				result.IR.CompactNames = compact
				o, err := swiftemit.Emit(result.IR, nil)
				if err != nil {
					t.Fatalf("reserved public name must be renamed, not rejected: %v", err)
				}
				library := generatedSource(o)
				if !bytes.Contains(library, []byte("public struct `"+name+"_` {")) || !bytes.Contains(library, []byte("public func `Export`(_ a0: `"+name+"_`")) {
					t.Fatalf("missing renamed public struct %s_ in generated library", name)
				}
				if ds := driver.EmitWithOptions("swift", result, t.TempDir(), driver.EmitOptions{CompactNames: compact}); diagnostics.HasErrors(ds) {
					t.Fatalf("unexpected diagnostics: %v", ds)
				}
			})
		}
	}
}

func TestReservedHandleNameRenamed(t *testing.T) {
	result := buildSwift(t, "package boundary\ntype Task struct { n int64 }\nfunc New() *Task { return &Task{} }\nfunc (c *Task) Add(d int64) int64 { c.n += d; return c.n }")
	o, err := swiftemit.Emit(result.IR, nil)
	if err != nil {
		t.Fatalf("reserved handle name must be renamed, not rejected: %v", err)
	}
	library := generatedSource(o)
	for _, want := range []string{"public final class `Task_` {", "GoalchemyGenerated.`Task_`?", "\"Task\", GoalchemyGenerated.`Task_`.init"} {
		if !bytes.Contains(library, []byte(want)) {
			t.Fatalf("missing %q in generated library", want)
		}
	}
}
func TestBulkByteEmission(t *testing.T) {
	result := buildSwift(t, `package bytes
 type Octet uint8
 func Join(a,b []Octet) []Octet { return append(a,b...) }
 func Fixed(a [3]Octet) [3]Octet { return a }
 func Integers(v []int) []int { return v }
 `)
	for _, compact := range []bool{false, true} {
		t.Run(fmt.Sprintf("compact=%v", compact), func(t *testing.T) {
			result.IR.CompactNames = compact
			output, err := swiftemit.Emit(result.IR, nil)
			if err != nil {
				t.Fatal(err)
			}
			source := string(generatedSource(output))
			foundAppend := false
			for _, line := range strings.Split(source, "\n") {
				_, call, ok := strings.Cut(line, "GSlice.append(")
				if !ok {
					continue
				}
				foundAppend = true
				_, spread, ok := strings.Cut(call, ", ")
				if !ok || strings.Contains(spread, "GElements(") {
					t.Fatalf("spread argument expands bytes to GValue elements: %s", line)
				}
			}
			if !foundAppend || !strings.Contains(source, "return try GElements(v).map") {
				t.Fatal("fixture must exercise byte spread append and unrelated generic conversion")
			}
			if !strings.Contains(source, "GBuffer(bytes: v, elem:") || !strings.Contains(source, "GSlice.byteSource(v)") {
				t.Fatal("byte public conversion lacks native buffer path")
			}
		})
	}
}

func generatedSource(output *swiftemit.Output) []byte {
	var source []byte
	for _, file := range output.Files {
		source = append(source, file.Source...)
	}
	return source
}
