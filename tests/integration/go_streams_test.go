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

func buildStreams(t *testing.T) *driver.Result {
	t.Helper()
	fixture, err := filepath.Abs("testdata/streams")
	if err != nil {
		t.Fatal(err)
	}
	res, ds := driver.Build(context.Background(), driver.Options{Dir: fixture, Gate: subset.Cooperative})
	if diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	return res
}

func TestGeneratedGoStreams(t *testing.T) {
	res := buildStreams(t)
	out, consumer := t.TempDir(), t.TempDir()
	if ds := driver.Emit("go", res, out); diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	mod := fmt.Sprintf("module independent\n\ngo 1.25\nrequire goalchemyout v0.0.0\nreplace goalchemyout => %s\n", out)
	for name, data := range map[string]string{"go.mod": mod, "consumer_test.go": goStreamsConsumer} {
		if err := os.WriteFile(filepath.Join(consumer, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", "-race", "-count=1", "./...")
	cmd.Dir, cmd.Env = consumer, append(driver.ToolEnv(), "GOTOOLCHAIN=go1.25.14", "GOFLAGS=")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("streams consumer: %v\n%s", err, output)
	}
}

const goStreamsConsumer = `package independent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	g "goalchemyout"
)

var bg = context.Background()

type closer struct {
	io.Reader
	closed int
}

func (c *closer) Close() error { c.closed++; return nil }

type failing struct{}

func (failing) Read([]byte) (int, error) { return 0, errors.New("disk on fire") }

type reentrant struct{}

func (reentrant) Read([]byte) (int, error) {
	_, err := g.Sum(bg, strings.NewReader("x"))
	return 0, err
}

// mutating keeps writing into the buffer it was given after returning.
type keeper struct{ kept [][]byte }

func (k *keeper) Write(p []byte) (int, error) { k.kept = append(k.kept, p); return len(p), nil }

func TestStreams(t *testing.T) {
	big := bytes.Repeat([]byte("0123456789abcdef"), 3<<16)
	var out bytes.Buffer
	n, err := g.Copy(bg, &out, bytes.NewReader(big))
	if err != nil || n != int64(len(big)) || !bytes.Equal(out.Bytes(), big) {
		t.Fatal("chunked copy", n, err)
	}
	sum, err := g.Sum(bg, strings.NewReader("\x01\x02\x03"))
	if err != nil || sum != 6 {
		t.Fatal("sum", sum, err)
	}
	part, err := g.Section(bg, strings.NewReader("hello, world"), 7, 5)
	if err != nil || string(part) != "world" {
		t.Fatal("seek", string(part), err)
	}
	part, err = g.Section(bg, strings.NewReader("short"), 3, 5)
	if string(part) != "rt" || err == nil || err.Error() != "unexpected EOF" {
		t.Fatal("io.EOF mapping through ReadFull", string(part), err)
	}
	c := &closer{Reader: strings.NewReader("abc")}
	if k, err := g.Drain(bg, c); err != nil || k != 3 || c.closed != 1 {
		t.Fatal("close", k, err, c.closed)
	}
	if eof, err := g.AtEOF(bg, strings.NewReader("")); err != nil || !eof {
		t.Fatal("host EOF is io.EOF", eof, err)
	}
	if msg, err := g.FirstError(bg, failing{}); err != nil || msg != "disk on fire" {
		t.Fatal("host error", msg, err)
	}
	if isNil, err := g.Nil(bg, nil); err != nil || !isNil {
		t.Fatal("nil stream", isNil, err)
	}
	if msg, err := g.FirstError(bg, reentrant{}); err != nil || !strings.Contains(msg, "reentrant") {
		t.Fatal("reentrant host stream", msg, err)
	}
	k := &keeper{}
	if _, err := g.Copy(bg, k, strings.NewReader("copied")); err != nil {
		t.Fatal(err)
	}
	k.kept[0][0] = 'X'
	if _, err := g.Sum(bg, strings.NewReader("still works")); err != nil {
		t.Fatal(err)
	}
	doc, err := g.Open(bg, bytes.NewReader([]byte("persistent document")))
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()
	if size, _ := doc.Size(bg); size != 19 {
		t.Fatal("size", size)
	}
	if at, err := doc.ReadAt(bg, 11, 8); err != nil || string(at) != "document" {
		t.Fatal("stream kept by the instance", string(at), err)
	}
	var whole bytes.Buffer
	if n, err := doc.WriteTo(bg, &whole); err != nil || n != 19 || whole.String() != "persistent document" {
		t.Fatal("WriteTo", n, err, whole.String())
	}
}
`
