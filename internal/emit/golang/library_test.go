package golang_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
)

// Value-receiver methods of a handle type receive a copy of the handle's
// object, including Close and methods taking or returning the type by value.
func TestLibraryHandleValueReceivers(t *testing.T) {
	res := buildNamingFixture(t, `package boundary
 import "github.com/eugenioenko/goalchemy/std/errors"
 type Locator struct { url string }
 func NewLocator(url string) *Locator { return &Locator{url: url} }
 func (l Locator) URL() string { return l.url }
 func (l *Locator) Set(url string) { l.url = url }
 type Coord struct { X, Y int }
 func NewCoord(x, y int) *Coord { return &Coord{x, y} }
 func (c Coord) Shift(d int) int { c.X += d; return c.X }
 func (c Coord) Moved(d int) Coord { c.X += d; return c }
 func (c Coord) Less(o Coord) bool { return c.X < o.X }
 func (c *Coord) Move(d int) int { c.X += d; return c.X }
 func (c Coord) Close() error { if c.X < 0 { return errors.New("negative") }; return nil }
 `)
	for _, compact := range []bool{false, true} {
		dir := t.TempDir()
		if ds := driver.EmitWithOptions("go", res, dir, driver.EmitOptions{CompactNames: compact}); diagnostics.HasErrors(ds) {
			t.Fatal(ds)
		}
		cmdDir := filepath.Join(dir, "cmd", "check")
		if err := os.MkdirAll(cmdDir, 0700); err != nil {
			t.Fatal(err)
		}
		consumer := `package main
 import("context"; generated "goalchemyout")
 func must[T any](v T, err error) T { if err != nil { panic(err) }; return v }
 func main(){
  ctx:=context.Background()
  l:=must(generated.NewLocator(ctx,"a"))
  if err:=l.Set(ctx,"b");err!=nil||must(l.URL(ctx))!="b" {panic("value receiver reads the handle")}
  c:=must(generated.NewCoord(ctx,1,2))
  if must(c.Shift(ctx,5))!=6||must(c.Move(ctx,1))!=2 {panic("value receiver changed the handle")}
  m:=must(c.Moved(ctx,10))
  if m.X!=12||m.Y!=2||!must(c.Less(ctx,m)) {panic("value parameter and result")}
  if err:=c.Close();err!=nil {panic(err)}
  if err:=must(generated.NewCoord(ctx,-1,0)).Close();err==nil||err.Error()!="negative" {panic("value receiver Close")}
 }
 `
		if err := os.WriteFile(filepath.Join(cmdDir, "main.go"), []byte(consumer), 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		cmd := exec.CommandContext(ctx, "go", "run", "./cmd/check")
		cmd.Dir = dir
		cmd.Env = append(driver.ToolEnv(), "GOTOOLCHAIN=go1.25.14")
		result, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("compact=%v consumer: %v\n%s", compact, err, result)
		}
	}
}
