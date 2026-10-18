package contracts

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/testutil"
)

const handlesProbe = `package main

import (
	"github.com/eugenioenko/goalchemy/std/errors"
	"github.com/eugenioenko/goalchemy/std/fmt"
	"github.com/eugenioenko/goalchemy/std/io"
	"github.com/eugenioenko/goalchemy/std/os"
)

func check(err error) {
	if err != nil {
		panic(err.Error())
	}
}

func expect(cond bool, what string) {
	if !cond {
		panic("unexpected: " + what)
	}
}

func main() {
	f, err := os.Create("handle.txt")
	check(err)
	n, err := f.WriteString("hello world")
	check(err)
	expect(n == 11, "write count")
	pos, err := f.Seek(6, io.SeekStart)
	check(err)
	expect(pos == 6, "seek start")
	rest, err := io.ReadAll(f)
	check(err)
	expect(string(rest) == "world", "read after seek: "+string(rest))
	buf := make([]byte, 4)
	n, err = f.Read(buf)
	expect(n == 0 && err == io.EOF, "eof")
	n, err = f.ReadAt(buf, 1)
	check(err)
	expect(n == 4 && string(buf) == "ello", "readat")
	n, err = f.ReadAt(buf, 9)
	expect(n == 2 && err == io.EOF && string(buf[:2]) == "ld", "readat tail")
	pos, err = f.Seek(-5, io.SeekEnd)
	check(err)
	expect(pos == 6, "seek end")
	pos, err = f.Seek(2, io.SeekCurrent)
	check(err)
	expect(pos == 8, "seek current")
	_, err = f.Seek(-1, io.SeekStart)
	expect(err != nil && err.Error() == "seek handle.txt: invalid argument", "negative seek: "+fmt.Sprint(err))
	big := make([]byte, 3<<20)
	for i := range big {
		big[i] = byte(i)
	}
	pos, err = f.Seek(0, io.SeekEnd)
	check(err)
	n, err = f.Write(big)
	check(err)
	expect(n == len(big), "large write")
	back := make([]byte, len(big))
	n, err = f.ReadAt(back, pos)
	check(err)
	expect(n == len(big) && string(back) == string(big), "large readat")
	st, err := f.Stat()
	check(err)
	expect(st.Name() == "handle.txt" && st.Size() == int64(11+len(big)) && !st.IsDir() && st.Mode().IsRegular(), "stat "+fmt.Sprint(st.Size()))
	check(f.Sync())
	check(f.Close())
	err = f.Close()
	expect(errors.Is(err, os.ErrClosed) && err.Error() == "close handle.txt: file already closed", "double close: "+fmt.Sprint(err))
	_, err = f.Read(buf)
	expect(errors.Is(err, os.ErrClosed), "read closed")

	a, err := os.OpenFile("handle.txt", os.O_WRONLY|os.O_APPEND, 0)
	check(err)
	_, err = a.Write([]byte("!"))
	check(err)
	check(a.Close())
	data, err := os.ReadFile("handle.txt")
	check(err)
	expect(len(data) == 12+len(big) && data[len(data)-1] == '!', "append")

	r, err := os.Open("handle.txt")
	check(err)
	_, err = r.Write([]byte("x"))
	expect(err != nil, "write read-only")
	head := make([]byte, 5)
	_, err = io.ReadFull(r, head)
	check(err)
	expect(string(head) == "hello", "read-only read")
	check(r.Close())

	_, err = os.Open("missing.txt")
	expect(errors.Is(err, os.ErrNotExist) && err.Error() == "open missing.txt: no such file or directory", "missing: "+fmt.Sprint(err))
	_, err = os.OpenFile("handle.txt", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	expect(errors.Is(err, os.ErrExist) && err.Error() == "open handle.txt: file exists", "excl: "+fmt.Sprint(err))
	_, err = os.Stat("missing.txt")
	expect(errors.Is(err, os.ErrNotExist) && err.Error() == "stat missing.txt: no such file or directory", "stat missing: "+fmt.Sprint(err))
	dst, err := os.Stat(".")
	check(err)
	expect(dst.IsDir() && dst.Mode().IsDir(), "stat dir")
	var pe *os.PathError
	expect(errors.As(err, &pe) == false, "no path error on success")

	t, err := os.CreateTemp(".", ".decrypt-*.tmp")
	check(err)
	name := t.Name()
	expect(len(name) > len("./.decrypt-.tmp") && name[:11] == "./.decrypt-" && name[len(name)-4:] == ".tmp", "temp name "+name)
	tst, err := t.Stat()
	check(err)
	expect(tst.Mode().Perm()&0o077 == 0 && tst.Size() == 0, "temp mode "+fmt.Sprint(uint32(tst.Mode())))
	_, err = t.Write([]byte("plain"))
	check(err)
	check(t.Close())
	check(os.WriteFile("final.txt", []byte("stale"), 0o600))
	check(os.Rename(name, "final.txt"))
	data, err = os.ReadFile("final.txt")
	check(err)
	expect(string(data) == "plain", "renamed content")
	_, err = os.Stat(name)
	expect(errors.Is(err, os.ErrNotExist), "old temp gone")
	err = os.Rename("missing.txt", "other.txt")
	var le *os.LinkError
	expect(errors.As(err, &le) && errors.Is(err, os.ErrNotExist) && err.Error() == "rename missing.txt other.txt: no such file or directory", "rename missing: "+fmt.Sprint(err))
	u, err := os.CreateTemp("", "goalchemy-probe-")
	check(err)
	check(u.Close())
	check(os.Remove(u.Name()))
	err = os.Remove(u.Name())
	expect(errors.Is(err, os.ErrNotExist), "remove twice")
	_, err = os.CreateTemp(".", "a/b")
	expect(err != nil && err.Error() == "createtemp a/b: pattern contains path separator", "separator: "+fmt.Sprint(err))
	_, err = os.CreateTemp("nodir", "x")
	expect(errors.Is(err, os.ErrNotExist), "temp in missing dir: "+fmt.Sprint(err))
	check(os.Remove("handle.txt"))
	check(os.WriteFile("size.txt", []byte(fmt.Sprint(st.Size())), 0o600))
}
`

// Every target implements std/os file handles: seekable reads and writes in
// chunks, positional reads, append, stat, exclusive temporary files, rename
// and remove, with Go's error texts.
func TestFileHandles(t *testing.T) {
	root, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	files := map[string]string{
		"main.go": handlesProbe,
		"go.mod":  fmt.Sprintf("module handlesprobe\n\ngo 1.25\nrequire github.com/eugenioenko/goalchemy v0.0.0\nreplace github.com/eugenioenko/goalchemy => %s\n", strconv.Quote(root)),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(source, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	targets := []string{"go", "typescript", "python", "java", "csharp", "rust", "c", "swift"}
	if only := os.Getenv("GOALCHEMY_TEST_TARGETS"); only != "" {
		targets = strings.Split(only, ",")
	}
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			out := t.TempDir()
			if ds := testutil.Compile(source, target, out); len(ds) != 0 {
				t.Fatal(ds)
			}
			observation, err := testutil.Runners[target](out)
			if err != nil || observation.Exit != 0 {
				t.Fatalf("%s probe: %v\n%s", target, err, observation)
			}
			if size, _ := os.ReadFile(filepath.Join(out, "size.txt")); string(size) != "3145739" {
				t.Fatalf("size.txt: %q", size)
			}
			if data, _ := os.ReadFile(filepath.Join(out, "final.txt")); string(data) != "plain" {
				t.Fatalf("final.txt: %q", data)
			}
			if _, err := os.Stat(filepath.Join(out, "handle.txt")); !os.IsNotExist(err) {
				t.Fatalf("handle.txt should be removed: %v", err)
			}
		})
	}
}
