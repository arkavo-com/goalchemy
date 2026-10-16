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
)

func buildHandles(t *testing.T) *driver.Result {
	t.Helper()
	fixture, err := filepath.Abs("testdata/handles")
	if err != nil {
		t.Fatal(err)
	}
	res, ds := driver.Build(context.Background(), driver.Options{Dir: fixture, Gate: subset.Cooperative})
	if diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	return res
}

func TestGeneratedGoHandles(t *testing.T) {
	res := buildHandles(t)
	out, consumer := t.TempDir(), t.TempDir()
	if ds := driver.Emit("go", res, out); diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	mod := fmt.Sprintf("module independent\n\ngo 1.25\nrequire goalchemyout v0.0.0\nreplace goalchemyout => %s\n", out)
	for name, data := range map[string]string{"go.mod": mod, "consumer_test.go": goHandlesConsumer} {
		if err := os.WriteFile(filepath.Join(consumer, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", "-race", "-count=1", "./...")
	cmd.Dir, cmd.Env = consumer, append(driver.ToolEnv(), "GOTOOLCHAIN=go1.25.14", "GOFLAGS=")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("handles consumer: %v\n%s", err, output)
	}
}

const goHandlesConsumer = `package independent

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	g "goalchemyout"
	"goalchemyout/rt"
)

var bg = context.Background()

func kind(err error) string {
	var le *g.LibraryError
	if errors.As(err, &le) {
		return le.Kind
	}
	return ""
}

func get[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func capture() chan string {
	ch := make(chan string, 16)
	rt.LibraryWarn = func(m string) { ch <- m }
	return ch
}

func TestInstanceState(t *testing.T) {
	c := get(g.New(bg, "a"))
	defer c.Close()
	for i := 1; i <= 3; i++ {
		if n := get(c.Add(bg, 2)); n != 2*i {
			t.Fatal("persistent field", n)
		}
	}
	if _, err := c.Add(bg, -1); err == nil || err.Error() != "negative" {
		t.Fatal("source error", err)
	}
	s := get(c.Snap(bg))
	if s.Name != "a" || s.N != 6 || s.Inits != 1 || s.Created != 1 || !s.LastNeg {
		t.Fatal("instance globals, init once and sentinel identity", s)
	}
	if pem := get(c.PublicKey(bg)); !strings.HasPrefix(pem, "-----BEGIN PUBLIC KEY-----") {
		t.Fatal("native key retired after its creating call", pem)
	}
	for i := 0; i < 3; i++ {
		if n := get(g.Inits(bg)); n != 1 {
			t.Fatal("free functions keep fresh globals", n)
		}
	}
	if get(c.Self(bg)) != c {
		t.Fatal("identity")
	}
	child := get(c.Child(bg))
	if get(child.Bump(bg)) != 7 || get(child.Parent(bg)) != c || get(c.Snap(bg)).N != 7 {
		t.Fatal("derived handle shares instance state")
	}
	if err := child.Close(); err != nil {
		t.Fatal(err)
	}
	if kind(must2(child.Bump(bg))) != "closed" {
		t.Fatal("closed derived handle")
	}
	if get(c.Add(bg, 1)) != 8 {
		t.Fatal("closing a derived handle keeps the instance")
	}
	if n := get(g.Nil(bg)); n != nil {
		t.Fatal("nil handle", n)
	}
}

func must2[T any](_ T, err error) error { return err }

func TestHandleParameters(t *testing.T) {
	a := get(g.New(bg, "a"))
	defer a.Close()
	b := get(a.Sibling(bg, "b"))
	defer b.Close()
	a.Add(bg, 2)
	b.Add(bg, 3)
	if get(g.Sum(bg, a, b)) != 5 {
		t.Fatal("handles of one instance")
	}
	if s := get(b.Snap(bg)); s.Created != 2 || s.Inits != 1 {
		t.Fatal("sibling joins the instance", s)
	}
	other := get(g.New(bg, "other"))
	defer other.Close()
	if _, err := g.Sum(bg, a, other); kind(err) != "instance_mismatch" {
		t.Fatal("mixed instances", err)
	}
	if get(g.Sum(bg, nil, nil)) != -1 {
		t.Fatal("nil handle arguments")
	}
	var zero *g.Counter
	if _, err := zero.Add(bg, 1); kind(err) != "invalid_handle" {
		t.Fatal("nil receiver", err)
	}
}

func TestValueReceivers(t *testing.T) {
	c := get(g.New(bg, "a"))
	defer c.Close()
	if get(c.Name(bg)) != "a" {
		t.Fatal("value receiver on a handle with private state")
	}
	p := get(g.NewCoord(bg, 1, 2))
	if get(p.Total(bg)) != 3 || get(p.Shift(bg, 5)) != 6 || get(p.Total(bg)) != 3 {
		t.Fatal("value receiver must not change the handle's object")
	}
	m := get(p.Moved(bg, 10))
	if m.X != 11 || m.Y != 2 || get(p.Total(bg)) != 3 {
		t.Fatal("value result is a copy", m)
	}
	if !get(p.Less(bg, m)) || get(p.Less(bg, get(p.Moved(bg, -1)))) {
		t.Fatal("value parameter of the handle's type")
	}
	if get(p.Move(bg, 1)) != 2 || get(p.Total(bg)) != 4 {
		t.Fatal("pointer receiver changes the handle's object")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := get(g.NewCoord(bg, -1, 0)).Close(); err == nil || err.Error() != "negative coord" {
		t.Fatal("value receiver Close", err)
	}
}

func TestConcurrentCalls(t *testing.T) {
	a := get(g.New(bg, "a"))
	b := get(g.New(bg, "b"))
	defer a.Close()
	defer b.Close()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			h := a
			if i%2 == 1 {
				h = b
			}
			for j := 0; j < 100; j++ {
				if _, err := h.Add(bg, 1); err != nil {
					t.Error(err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	if sa, sb := get(a.Snap(bg)), get(b.Snap(bg)); sa.N != 400 || sb.N != 400 || sa.Created != 1 || sb.Created != 1 {
		t.Fatal("serialized calls and independent instances", sa, sb)
	}
}

func TestClose(t *testing.T) {
	c := get(g.New(bg, "a"))
	sib := get(c.Sibling(bg, "bad-close"))
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal("idempotent close", err)
	}
	if _, err := c.Add(bg, 1); kind(err) != "closed" {
		t.Fatal("call after close", err)
	}
	if s := get(sib.Snap(bg)); s.Closes != 1 {
		t.Fatal("source Close ran once", s)
	}
	if err := sib.Close(); err == nil || err.Error() != "close failed" {
		t.Fatal("source Close error", err)
	}
	if _, err := sib.Snap(bg); kind(err) != "closed" {
		t.Fatal("failed Close still releases", err)
	}
}

func TestCancellation(t *testing.T) {
	c := get(g.New(bg, "a"))
	defer c.Close()
	ctx, cancel := context.WithTimeout(bg, 20*time.Millisecond)
	defer cancel()
	if err := c.Wait(ctx, 100000); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("active cancellation", err)
	}
	if get(c.Add(bg, 1)) != 1 {
		t.Fatal("instance usable after cancellation")
	}
}

func TestCloseWhilePending(t *testing.T) {
	c := get(g.New(bg, "a"))
	waited := make(chan error, 1)
	go func() { waited <- c.Wait(bg, 100000) }()
	time.Sleep(100 * time.Millisecond)
	queued := make(chan error, 1)
	go func() { _, err := c.Add(bg, 1); queued <- err }()
	time.Sleep(50 * time.Millisecond)
	done := make(chan error, 1)
	go func() { done <- c.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not cancel the active call")
	}
	if err := <-waited; kind(err) != "canceled" {
		t.Fatal("active call", err)
	}
	if err := <-queued; kind(err) != "closed" {
		t.Fatal("queued call", err)
	}
}

func TestPoison(t *testing.T) {
	c := get(g.New(bg, "a"))
	if err := c.Boom(bg); kind(err) != "source_panic" {
		t.Fatal("panic", err)
	}
	if _, err := c.Add(bg, 1); kind(err) != "poisoned" {
		t.Fatal("poisoned instance", err)
	}
	if err := c.Close(); err != nil {
		t.Fatal("close poisoned", err)
	}
	if _, err := c.Add(bg, 1); kind(err) != "closed" {
		t.Fatal("closed poisoned", err)
	}
}

func TestWarnings(t *testing.T) {
	warned := capture()
	defer func() { rt.LibraryWarn = func(string) {} }()
	c := get(g.New(bg, "a"))
	if err := c.Leak(bg); err != nil {
		t.Fatal(err)
	}
	if m := <-warned; !strings.Contains(m, "goroutine") {
		t.Fatal("abandoned goroutine warning", m)
	}
	if get(c.Add(bg, 1)) != 1 {
		t.Fatal("instance usable after abandoning goroutines")
	}
	c = nil
	for i := 0; i < 5; i++ {
		runtime.GC()
		select {
		case m := <-warned:
			if !strings.Contains(m, "Counter handle was not closed") {
				t.Fatal(m)
			}
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
	t.Fatal("no finalizer warning")
}
`
