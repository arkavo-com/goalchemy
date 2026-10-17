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

func TestGeneratedGoPackageLibrary(t *testing.T) {
	fixture, err := filepath.Abs("testdata/package_library/api")
	if err != nil {
		t.Fatal(err)
	}
	res, ds := driver.Build(context.Background(), driver.Options{Dir: fixture, Gate: subset.Cooperative})
	if diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	for _, compact := range []bool{false, true} {
		t.Run(fmt.Sprintf("compact=%v", compact), func(t *testing.T) {
			out, consumer := t.TempDir(), t.TempDir()
			if ds := driver.EmitWithOptions("go", res, out, driver.EmitOptions{Names: testutil.Names(compact)}); diagnostics.HasErrors(ds) {
				t.Fatal(ds)
			}
			mod := fmt.Sprintf("module independent\n\ngo 1.25\nrequire goalchemyout v0.0.0\nreplace goalchemyout => %s\n", out)
			for name, data := range map[string]string{"go.mod": mod, "consumer_test.go": packageLibraryConsumer} {
				if err := os.WriteFile(filepath.Join(consumer, name), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, "go", "test", "-race", "-count=1", "./...")
			cmd.Dir, cmd.Env = consumer, append(driver.ToolEnv(), "GOTOOLCHAIN=go1.25.14", "GOFLAGS=")
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("independent package library: %v\n%s", err, output)
			}
		})
	}
}

const packageLibraryConsumer = `package independent
import (
 "bytes"
 "context"
 "errors"
 "sync"
 "testing"
 g "goalchemyout"
)
const order="order-var/order-init/state-var/state-init/api-var/api-init"
func TestFreshPackageStateAndOwnedResults(t *testing.T) {
 input:=g.Value{Count:2,Bytes:[]byte{7},Order:"caller"}
 first,err:=g.Export(context.Background(),input)
 if err!=nil || first.Count!=29 || first.Order!=order || !bytes.Equal(first.Bytes,[]byte{3,255,128}) {t.Fatal("package initialization/dispatch",first,err)}
 if input.Bytes[0]!=7 || input.Count!=2 || input.Order!="caller" {t.Fatal("caller input changed")}
 _,err=g.ErrorGlobal(context.Background())
 var failure *g.Failure
 if !errors.As(err,&failure) || failure.Code!="saved" || !bytes.Equal(failure.Bytes,[]byte{9}) {t.Fatal("owned source error",err)}
 var wg sync.WaitGroup
 for i:=0;i<12;i++ {wg.Add(1);go func(){defer wg.Done();next,err:=g.Export(context.Background(),g.Value{Count:2});if err!=nil || next.Count!=29 || next.Order!=order || !bytes.Equal(next.Bytes,[]byte{3,255,128}) {t.Error("fresh overlapping call",next,err)}}()}
 wg.Wait()
 if !bytes.Equal(first.Bytes,[]byte{3,255,128}) || !bytes.Equal(failure.Bytes,[]byte{9}) {t.Fatal("retained result/error changed during reset")}
 first.Bytes[0]=99;failure.Bytes[0]=88
 next,err:=g.Export(context.Background(),g.Value{Count:2})
 if err!=nil || next.Bytes[0]!=3 {t.Fatal("output aliases source globals",next,err)}
 _,err=g.ErrorGlobal(context.Background());var fresh *g.Failure
 if !errors.As(err,&fresh) || fresh.Bytes[0]!=9 {t.Fatal("error aliases source globals",err)}
}
`
