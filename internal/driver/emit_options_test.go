package driver

import (
	"sync"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/ir"
)

func TestEmissionNamingOptionsAreIndependent(t *testing.T) {
	const target = "test-naming-options"
	var mu sync.Mutex
	observed := map[string]string{}
	original := &ir.Program{Names: "compact"}
	res := &Result{IR: original}
	Register(target, func(actual *Result, out string) []diagnostics.Diagnostic {
		if actual == res || actual.IR == original {
			t.Error("emitter received shared program header")
		}
		mu.Lock()
		observed[out] = actual.IR.Names
		mu.Unlock()
		return nil
	})
	t.Cleanup(func() { delete(emitters, target) })
	var wg sync.WaitGroup
	for _, mode := range []string{"readable", "compact", "source"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ds := EmitWithOptions(target, res, mode, EmitOptions{Names: mode}); len(ds) != 0 {
				t.Errorf("emission diagnostics: %v", ds)
			}
		}()
	}
	wg.Wait()
	if ds := Emit(target, res, "default"); len(ds) != 0 {
		t.Fatalf("default emission diagnostics: %v", ds)
	}
	for out, want := range map[string]string{"readable": "readable", "compact": "compact", "source": "source", "default": "source"} {
		if observed[out] != want {
			t.Fatalf("wrong naming policies: %v", observed)
		}
	}
	if ds := EmitWithOptions(target, res, "bad", EmitOptions{Names: "short"}); len(ds) != 1 || ds[0].Code != "GCE009" {
		t.Fatalf("unknown names mode: %v", ds)
	}
	if res.IR != original || original.Names != "compact" {
		t.Fatal("emission changed shared program")
	}
}
