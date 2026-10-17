package integration

import (
	"context"
	"fmt"
	"github.com/eugenioenko/goalchemy/internal/driver"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGeneratedCHostFatalCleanup(t *testing.T) {
	cases := []struct {
		name, decl, tail, stderr string
		status                   int
		release                  bool
	}{
		{"ordinary-runtime-fault", `func allocate(){n:=int(1<<40);b:=make([]byte,n);println(len(b))}`, "defer func(){println(\"unexpected recovery\",recover()!=nil)}();allocate()", "goalchemy host fault: allocation exceeds host limits\n", 3, false},
		{"deferred-runtime-fault", `func allocate(){n:=int(1<<40);b:=make([]byte,n);println(len(b))};func deferred(){time.Sleep(1);allocate()}`, "defer deferred();return", "goalchemy host fault: allocation exceeds host limits\n", 3, false},
		{"mutex-fatal", "", "var m sync.Mutex;m.Unlock()", "fatal error: sync: unlock of unlocked mutex\n", 2, false},
		{"panic", "", "panic(\"source panic\")", "panic: source panic\n", 2, false},
		{"custom-error", `type ownedError struct{};func(ownedError)Error()string{return "owned panic formatter"}`, "panic(ownedError{})", "panic: owned panic formatter\n", 2, false},
		{"binary-custom-error", `type ownedError struct{};func(ownedError)Error()string{return "owned\x00\xff panic formatter"}`, "panic(ownedError{})", "panic: owned\x00\xff panic formatter\n", 2, false},
		{"stack", `func recurse(n int)int{return recurse(n+1)+1}`, "defer func(){println(\"unexpected recovery\",recover()!=nil)}();println(recurse(0))", "fatal error: stack overflow\n", 2, false},
		{"cooperative-stack", `func recurse(n int){time.Sleep(1);recurse(n+1)}`, "recurse(0)", "fatal error: stack overflow\n", 2, false},
		{"deadlock", "", "time.Sleep(124);var blocked chan int;<-blocked", "fatal error: all goroutines are asleep - deadlock!\n", 2, true},
		{"main-return", "", "return", "", 0, false},
		{"native-fault", "", "time.Sleep(130)", "goalchemy host fault: test native worker fault after acquisition\n", 3, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var normal, started, released atomic.Int32
			gate := make(chan struct{})
			body := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/normal":
					normal.Add(1)
					select {
					case <-gate:
					default:
						close(gate)
					}
					w.Write([]byte{0, 255, 128, 3})
					w.(http.Flusher).Flush()
					select {
					case <-body:
					case <-r.Context().Done():
					}
				case "/started":
					started.Add(1)
					select {
					case <-gate:
					case <-r.Context().Done():
						return
					}
					w.Write([]byte("started"))
				case "/release":
					released.Add(1)
					close(body)
					w.Write([]byte("released"))
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			source := fmt.Sprintf("package main\nimport(\"github.com/eugenioenko/goalchemy/lib/time\";\"github.com/eugenioenko/goalchemy/lib/context\";\"github.com/eugenioenko/goalchemy/lib/sync\")\n%s\nfunc main(){var unused sync.Mutex;_ = unused;_,cancel:=context.WithCancel(context.Background());defer cancel();go func(){time.Sleep(131)}();time.Sleep(125);%s}\n", tc.decl, tc.tail)
			out := cHostFixture(t, source)
			cHostAdapter(t, out, srv.URL, "https://unused.invalid", "unused")
			cHostInject(t, filepath.Join(out, "main.c"), "    gx_run_main(_init_zero_globals, _entry_frame);", "    gx_host_main(_init_zero_globals, _entry_frame);")
			marker, ack := filepath.Join(out, "fresh-native-cleanup.txt"), filepath.Join(out, "release-resource-ack")
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "sh", "run.sh")
			cmd.Dir = out
			cmd.Env = append(driver.ToolEnv(), "C_HOST_CLEANUP_MARKER="+marker, "C_HOST_ACK_GATE="+ack)
			if tc.name == "deferred-runtime-fault" {
				cmd.Env = append(cmd.Env, "C_HOST_EXPECT_DEFER_GRAPH=1")
			}
			var output strings.Builder
			cmd.Stdout = &output
			cmd.Stderr = &output
			if e := cmd.Start(); e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for {
				if b, e := os.ReadFile(fmt.Sprintf("%s-%d", marker, map[bool]int{true: 130, false: 131}[tc.name == "native-fault"])); e == nil {
					if string(b) != "released native body/process/input\n" {
						t.Fatalf("cleanup marker %q", b)
					}
					break
				}
				select {
				case e := <-done:
					t.Fatalf("exited before resource cleanup barrier: %v\n%s", e, output.String())
				case <-ctx.Done():
					t.Fatal("cleanup barrier timeout")
				case <-ticker.C:
				}
			}
			select {
			case e := <-done:
				t.Fatalf("exit/return occurred while resource ACK withheld: %v\n%s", e, output.String())
			default:
			}
			if normal.Load() != (map[bool]int32{true: 2, false: 1}[tc.name == "native-fault"]) || started.Load() != 1 {
				t.Fatalf("independent contact counters normal=%d started=%d", normal.Load(), started.Load())
			}
			if e := os.WriteFile(ack, []byte("release"), 0600); e != nil {
				t.Fatal(e)
			}
			e := <-done
			status := 0
			if e != nil {
				exit, ok := e.(*exec.ExitError)
				if !ok {
					t.Fatal(e)
				}
				status = exit.ExitCode()
			}
			if status != tc.status || output.String() != tc.stderr {
				t.Fatalf("actual emitted cleanup+fatal status=%d want=%d output=%q want=%q", status, tc.status, output.String(), tc.stderr)
			}
			if b, e := os.ReadFile(marker + "-owner"); e != nil || string(b) != "owner cleanup complete\n" {
				t.Fatalf("owner did not await every native lease: %v %q", e, b)
			}
			if _, e := os.ReadFile(marker + "-131"); e != nil {
				t.Fatal("background operation did not finish exact resource cleanup", e)
			}
			if tc.release && released.Load() != 1 {
				t.Fatal("deadlock did not follow normal transport release")
			}
		})
	}
}
func TestGeneratedCHostRecoveredSourcePanic(t *testing.T) {
	out := cHostFixture(t, `package main
import("github.com/eugenioenko/goalchemy/lib/time")
func recur(n int)int{if n==0{panic("recovered")};return recur(n-1)}
func guarded(){defer func(){println(recover()=="recovered")}();recur(12)}
func cooperative(){time.Sleep(1)}
func main(){time.Sleep(1);for i:=0;i<512;i++{guarded();cooperative()};println("source recovered")}
`)
	baseline := cHostCommand(t, out, "sh", "run.sh")
	cHostInject(t, filepath.Join(out, "main.c"), "    gx_run_main(_init_zero_globals, _entry_frame);", "    gx_host_main(_init_zero_globals, _entry_frame);")
	host := cHostCommand(t, out, "sh", "run.sh")
	if baseline != host || host != strings.Repeat("true\n", 512)+"source recovered\n" {
		t.Fatalf("balanced source recursion/panic guard %q", host)
	}
}
