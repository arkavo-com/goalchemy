package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/eugenioenko/goalchemy/internal/driver"
	"github.com/eugenioenko/goalchemy/internal/testutil"
)

// TestGeneratedTypeScriptHTTPGzipBrowser runs lib/http gzip handling in a real
// browser, where fetch decodes and Accept-Encoding cannot be sent, and checks
// that DoWith transport options are rejected there.
func TestGeneratedTypeScriptHTTPGzipBrowser(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	run := func(dir, name string, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir, cmd.Env = dir, driver.ToolEnv()
		data, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", name, err, data)
		}
		return string(data)
	}
	source, out := t.TempDir(), t.TempDir()
	for name, text := range map[string]string{
		"go.mod": fmt.Sprintf("module gzipprobe\n\ngo 1.25\nrequire github.com/eugenioenko/goalchemy v0.0.0\nreplace github.com/eugenioenko/goalchemy => %s\n", root),
		"library.go": `package gzipprobe
import ("github.com/eugenioenko/goalchemy/lib/context";"github.com/eugenioenko/goalchemy/lib/http";"github.com/eugenioenko/goalchemy/std/strconv")
func Show(ctx context.Context, base, name string, accept bool) (string, error) {
	var h []string
	if accept {
		h = []string{"Accept-Encoding", "gzip"}
	}
	status, headers, body, err := http.Do(ctx, "GET", base+name, h, nil, 1024, 5000)
	if err != nil {
		return "error: " + err.Error(), nil
	}
	encoding := ""
	for i := 0; i < len(headers); i += 2 {
		if headers[i] == "Content-Encoding" {
			encoding = headers[i+1]
		}
	}
	return strconv.Itoa(status) + " " + string(body) + " encoding=" + encoding, nil
}
func ShowTLS(ctx context.Context, base string) (string, error) {
	status, _, _, err := http.DoWith(ctx, "GET", base+"/single", nil, nil, 1024, 5000, []string{http.InsecureSkipVerify, "true"})
	if err != nil {
		return "error: " + err.Error(), nil
	}
	return strconv.Itoa(status), nil
}
`,
	} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if ds := testutil.CompileGate(source, "typescript", out, "cooperative"); len(ds) > 0 {
		t.Fatal(ds)
	}
	run(out, "tsc", "-p", ".")
	t.Log(run(root, "node", "targets/typescript/tests/http_gzip_browser.mjs", out))
}
