package rt

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func isolatedHost(t *testing.T) *scheduler {
	t.Helper()
	sched.shutdown()
	s := &scheduler{rng: 1, nextID: 1, host: true, epoch: time.Now(), harness: true}
	sched = s
	t.Cleanup(func() { s.shutdown(); sched = &scheduler{rng: 1} })
	return s
}

func requireSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("bounded synchronization timed out")
	}
}

func TestHostCompletionBeforeSubmissionReturnsAndFIFO(t *testing.T) {
	s := isolatedHost(t)
	a, b := &Task{id: 1}, &Task{id: 2}
	cleaned := 0
	token := s.registerHost(a, nil, func() { cleaned++ }, nil)
	if !a.blocked {
		t.Fatal("registration did not park task")
	}
	s.ready(b)
	token.complete([]any{7}, nil) // synchronous/reentrant submission
	if a.RV != nil || s.runq.len() != 1 {
		t.Fatal("callback mutated source state")
	}
	token.complete([]any{99}, nil)
	if got := s.next(); got != b {
		t.Fatal("independent task lost FIFO order")
	}
	if got := s.next(); got != a {
		t.Fatal("completion did not resume task")
	}
	if a.RV[0] != 7 || cleaned != 1 || len(s.operations) != 0 {
		t.Fatal("terminal application was not exactly once")
	}
}

func TestHostPendingWaitWakeAndUnrelatedProgress(t *testing.T) {
	s := isolatedHost(t)
	a, b := &Task{id: 1}, &Task{id: 2}
	started, release := make(chan struct{}), make(chan struct{})
	token := s.registerHost(a, nil, nil, nil)
	s.launchHost(token, func() []any { close(started); <-release; return []any{"done"} })
	requireSignal(t, started)
	s.ready(b)
	if s.next() != b {
		t.Fatal("pending work blocked unrelated task")
	}
	// No run queue and no timer: the live operation must wait, then wake.
	go func() { close(release) }()
	if s.next() != a || a.RV[0] != "done" {
		t.Fatal("driver did not wake for completion")
	}
}

func TestHostOutOfOrderDuplicateAndForeignTokens(t *testing.T) {
	s := isolatedHost(t)
	a, b := &Task{id: 1}, &Task{id: 2}
	x := s.registerHost(a, nil, nil, nil)
	y := s.registerHost(b, nil, nil, nil)
	foreign := hostToken{&hostMailbox{wake: make(chan struct{}, 1)}, x.id, a.id}
	foreign.complete([]any{-1}, nil)
	y.complete([]any{2}, nil)
	x.complete([]any{1}, nil)
	y.complete([]any{3}, nil)
	if s.next() != b || s.next() != a {
		t.Fatal("completion queue order lost")
	}
	if b.RV[0] != 2 || a.RV[0] != 1 || len(s.operations) != 0 {
		t.Fatal("duplicate/foreign token changed result")
	}
}

func TestHostQueuedSuccessCancellationPrecedenceAndCleanup(t *testing.T) {
	s := isolatedHost(t)
	c, cancel := StdContextWithCancel(StdContextBackground())
	a := &Task{id: 1}
	cleaned := 0
	token := s.registerHost(a, nil, func() { cleaned++ }, func() []any {
		if err := StdContextContextErr(c); err != nil {
			return []any{err}
		}
		return nil
	})
	token.complete([]any{"success"}, nil)
	cancel()
	s.drainHost()
	if !errors.Is(a.RV[0].(error), context.Canceled) || cleaned != 1 {
		t.Fatal("queued success won over observed cancellation")
	}
	// Applied success is final; a later cancel cannot rewrite it.
	c2, cancel2 := StdContextWithCancel(StdContextBackground())
	b := &Task{id: 2}
	token2 := s.registerHost(b, nil, nil, func() []any {
		if err := StdContextContextErr(c2); err != nil {
			return []any{err}
		}
		return nil
	})
	token2.complete([]any{"success"}, nil)
	s.drainHost()
	cancel2()
	if b.RV[0] != "success" {
		t.Fatal("cancel rewrote committed success")
	}
}

func TestHostCancelWaitsForFinishedWorkerCleanup(t *testing.T) {
	s := isolatedHost(t)
	requested, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	a := &Task{id: 1}
	canceled := false
	token := s.registerHost(a, func() { canceled = true; close(requested) }, nil, func() []any {
		if canceled {
			return []any{context.Canceled}
		}
		return nil
	})
	s.launchHost(token, func() []any { <-release; close(finished); return []any{"success"} })
	s.operations[token.id].cancel()
	requireSignal(t, requested)
	s.drainHost()
	if a.RV != nil || len(s.operations) != 1 {
		t.Fatal("cancellation resumed before cleanup acknowledgement")
	}
	close(release)
	if s.next() != a {
		t.Fatal("canceled worker did not resume")
	}
	requireSignal(t, finished)
	if !errors.Is(a.RV[0].(error), context.Canceled) {
		t.Fatal("wrong cancellation result")
	}
}

func TestHostFaultIsNotSourcePanicAndWinsOverCancel(t *testing.T) {
	s := isolatedHost(t)
	a := &Task{id: 1}
	cleaned := 0
	token := s.registerHost(a, nil, func() { cleaned++ }, func() []any { return []any{context.Canceled} })
	s.launchHost(token, func() []any { panic("adapter defect") })
	s.workers.Wait()
	func() {
		defer func() {
			r := recover()
			if f, ok := r.(HostFault); !ok || f.Value != "adapter defect" {
				t.Fatalf("fault changed to source error/panic: %#v", r)
			}
		}()
		s.drainHost()
		t.Fatal("host fault was swallowed by cancellation")
	}()
	if cleaned != 1 || a.RV != nil {
		t.Fatal("fault resumed task or skipped cleanup")
	}
	for _, protected := range []func(){
		func() { callProtected(func() { panic(HostFault{"fault"}) }) },
		func() { startProtected(func() Frame { panic(HostFault{"fault"}) }) },
		func() { s.step(&Task{Frame: Sync(func() []any { panic(HostFault{"fault"}) })}) },
	} {
		func() {
			defer func() {
				if _, ok := recover().(HostFault); !ok {
					t.Fatal("source recovery caught host fault")
				}
			}()
			protected()
		}()
	}
}

func TestHostShutdownAndResetRejectLateCompletion(t *testing.T) {
	s := isolatedHost(t)
	a := &Task{id: 1}
	release := make(chan struct{})
	var released atomic.Int32
	token := s.registerHost(a, func() { close(release) }, func() { released.Add(1) }, nil)
	s.launchHost(token, func() []any { <-release; return []any{1} })
	ResetScheduler()
	if !s.closed || released.Load() != 1 || a.RV != nil || s.runq.len() != 0 {
		t.Fatal("retirement drove source or leaked work")
	}
	token.complete([]any{2}, &HostFault{"late"})
	s.drainHost()
	if len(s.mail.queue) != 0 || sched.runq.len() != 0 {
		t.Fatal("old callback revived scheduler")
	}
	s.shutdown()
	if released.Load() != 1 {
		t.Fatal("shutdown cleanup repeated")
	}
}

func TestHostContextDeadlinesAndRegistrationPruning(t *testing.T) {
	s := isolatedHost(t)
	parent, cancelParent := StdContextWithTimeout(StdContextBackground(), int64(time.Hour))
	defer cancelParent()
	for i := 0; i < 1000; i++ {
		c, cancel := StdContextWithTimeout(parent, int64(time.Hour))
		if c.c.deadline != parent.c.deadline {
			t.Fatal("nested timeout extended parent")
		}
		cancel()
	}
	if len(parent.c.children) != 0 || len(s.timers) != 1 {
		t.Fatal("child/timer registrations grew with canceled requests")
	}
	expired, cancel := StdContextWithTimeout(parent, -1)
	defer cancel()
	if StdContextContextErr(expired) != ContextDeadlineExceeded || !expired.c.done.c.closed {
		t.Fatal("expired timeout is incoherent")
	}
	nested, cancelNested := StdContextWithTimeout(expired, int64(time.Hour))
	defer cancelNested()
	if nested.c.err != ContextDeadlineExceeded || len(expired.c.children) != 0 {
		t.Fatal("expired parent registered live child")
	}
	cancelParent()
	child, cancelChild := StdContextWithCancel(parent)
	defer cancelChild()
	if child.c.err != ContextCanceled || len(parent.c.children) != 0 || len(s.timers) != 0 {
		t.Fatal("already canceled parent not inherited")
	}
	if ContextCanceled != context.Canceled || ContextDeadlineExceeded != context.DeadlineExceeded {
		t.Fatal("context bridge lost standard sentinel identity")
	}
}

func TestHostOverflowAndDueTimerOrdering(t *testing.T) {
	s := isolatedHost(t)
	c, cancel := StdContextWithTimeout(StdContextBackground(), math.MaxInt64)
	if c.c.deadline != math.MaxInt64 || c.c.err != nil {
		t.Fatal("positive duration wrapped to expired deadline")
	}
	cancel()
	if len(s.timers) != 0 {
		t.Fatal("saturated timer not removed")
	}
	var order []int
	one := s.addTimer(1, nil, func() { order = append(order, 1) })
	two := s.addTimer(1, nil, func() { order = append(order, 2) })
	three := s.addTimer(1, nil, func() { order = append(order, 3) })
	one.at, two.at, three.at = 2, 1, 1
	s.ready(&Task{id: 7})
	s.next() // due timers must fire despite runnable source tasks
	if !reflect.DeepEqual(order, []int{2, 3, 1}) {
		t.Fatalf("timer order %v", order)
	}
}

func TestHostBoundedCompletionStress(t *testing.T) {
	s := isolatedHost(t)
	const n = 256
	var cleaned atomic.Int32
	for i := 0; i < n; i++ {
		a := &Task{id: i}
		token := s.registerHost(a, nil, func() { cleaned.Add(1) }, nil)
		s.launchHost(token, func() []any { return []any{token.task} })
	}
	s.workers.Wait()
	s.drainHost()
	if len(s.operations) != 0 || s.runq.len() != n || cleaned.Load() != n {
		t.Fatal("stress cleanup/application count wrong")
	}
	for i := range s.runq.len() {
		if a := s.runq.at(i); a.RV[0] != a.id {
			t.Fatal("completion applied to wrong task")
		}
	}
}

func TestAwaitSourcePanicRetiresOwnerAndWorker(t *testing.T) {
	isolatedHost(t)
	release, finished := make(chan struct{}), make(chan struct{})
	var owner *scheduler
	func() {
		defer func() {
			if recover() != "source panic" {
				t.Fatal("source panic result changed")
			}
		}()
		Await(func(task *Task) {
			owner = sched
			token := owner.registerHost(task, func() { close(release) }, nil, nil)
			owner.launchHost(token, func() []any { <-release; close(finished); return nil })
			panic("source panic")
		})
	}()
	requireSignal(t, finished)
	if !owner.closed || owner.cur != nil || owner.main != nil || len(owner.operations) != 0 {
		t.Fatal("source panic retained owner/work")
	}
}

type hostTestFrame struct {
	FrameBase
	step func(*Task)
}

func (f *hostTestFrame) Step(task *Task) { f.step(task) }
func (f *hostTestFrame) Results() []any  { return nil }

func TestRunMainHostFaultRetiresOtherPendingWorker(t *testing.T) {
	isolatedHost(t)
	release, finished := make(chan struct{}), make(chan struct{})
	var owner *scheduler
	frame := &hostTestFrame{}
	frame.step = func(task *Task) {
		owner = sched
		other := owner.registerHost(&Task{id: 77}, func() { close(release) }, nil, nil)
		owner.launchHost(other, func() []any { <-release; close(finished); return nil })
		bad := owner.registerHost(task, nil, nil, nil)
		owner.launchHost(bad, func() []any { panic("adapter failure") })
	}
	func() {
		defer func() {
			f, ok := recover().(HostFault)
			if !ok || f.Value != "adapter failure" {
				t.Fatal("host fault not propagated separately")
			}
		}()
		RunMainHost(frame)
	}()
	requireSignal(t, finished)
	if !owner.closed || len(owner.operations) != 0 || owner.cur != nil {
		t.Fatal("host fault left live owner/worker")
	}
}

func TestHostConcurrentOwnerMailboxesAreIsolated(t *testing.T) {
	one := isolatedHost(t)
	two := &scheduler{host: true, epoch: time.Now()}
	defer two.shutdown()
	a, b := &Task{id: 1}, &Task{id: 1}
	x := one.registerHost(a, nil, nil, nil)
	y := two.registerHost(b, nil, nil, nil)
	x.complete([]any{"one"}, nil)
	two.drainHost()
	if b.RV != nil || len(two.operations) != 1 {
		t.Fatal("other owner applied completion")
	}
	y.complete([]any{"two"}, nil)
	one.drainHost()
	two.drainHost()
	if a.RV[0] != "one" || b.RV[0] != "two" {
		t.Fatal("same sequence/task IDs crossed owner boundary")
	}
}

func TestHTTPBridgeRepeatedRequestsPrunesCancelHooks(t *testing.T) {
	isolatedHost(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	defer server.Close()
	var c Context
	var cancel func()
	requests := 0
	frame := &hostTestFrame{}
	frame.step = func(task *Task) {
		if c.c == nil {
			c, cancel = StdContextWithTimeout(StdContextBackground(), int64(time.Hour))
		}
		if frame.PC == 1 {
			if task.RV[0] != 200 || task.RV[3] != nil || string(task.RV[2].([]byte)) != "ok" {
				t.Errorf("actual HTTP failed: %v", task.RV)
			}
			if len(c.c.hooks) != 0 {
				t.Error("finished HTTP retained cancellation hook")
			}
			requests++
			if requests == 32 {
				cancel()
				if len(sched.timers) != 0 {
					t.Error("canceled HTTP parent retained timer")
				}
				Ret(task, frame)
				return
			}
		}
		frame.PC = 1
		LibHTTPDo(task, c, "GET", server.URL, nil, nil, 64, 2000)
		if len(c.c.hooks) != 1 {
			t.Error("pending HTTP missing cancellation bridge")
		}
	}
	RunMainHost(frame)
	if requests != 32 || len(c.c.hooks) != 0 {
		t.Fatal("request loop did not clean registrations")
	}
}

func TestExecutableFatalSourcePanicWaitsForWorkerCleanup(t *testing.T) {
	if marker := os.Getenv("GOALCHEMY_FATAL_HOST_TEST_MARKER"); marker != "" {
		frame := &hostTestFrame{}
		frame.step = func(task *Task) {
			release := make(chan struct{})
			token := sched.registerHost(task, func() { close(release) }, nil, nil)
			sched.launchHost(token, func() []any {
				<-release
				if err := os.WriteFile(marker, []byte("cleaned"), 0600); err != nil {
					panic(err)
				}
				return nil
			})
			panic("source fatal")
		}
		RunMainHost(frame)
		t.Fatal("fatal source panic returned")
	}
	marker := filepath.Join(t.TempDir(), "cleanup")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestExecutableFatalSourcePanicWaitsForWorkerCleanup$")
	cmd.Env = append(os.Environ(), "GOALCHEMY_FATAL_HOST_TEST_MARKER="+marker)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 || string(out) != "panic: source fatal\n" {
		t.Fatalf("wrong fatal result: %v %q", err, out)
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "cleaned" {
		t.Fatalf("os.Exit happened before worker cleanup: %v %q", err, data)
	}
}

func TestUnixClockIndependentOfVirtualScheduler(t *testing.T) {
	isolatedHost(t)
	ResetScheduler()
	sched.clock = math.MaxInt64
	before := time.Now().Unix()
	got := LibClockUnix()
	after := time.Now().Unix()
	if got < min(before, after) || got > max(before, after) {
		t.Fatalf("Unix followed virtual clock: %d", got)
	}
	beforeNano := time.Now().UnixNano()
	gotNano := LibClockUnixNano()
	afterNano := time.Now().UnixNano()
	if gotNano < beforeNano || gotNano > afterNano {
		t.Fatalf("UnixNano followed virtual clock: %d", gotNano)
	}
}

func TestHTTPRequestTimeoutIncludesSubmissionDelay(t *testing.T) {
	s := isolatedHost(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.Write([]byte("late")) }))
	defer server.Close()
	parent, cancel := StdContextWithCancel(StdContextBackground())
	defer cancel()
	task := &Task{id: 1}
	op := prepareHTTP(s, task, parent, "GET", server.URL, nil, nil, 64, 10)
	// Deterministically hold worker submission until the boundary's native
	// deadline fires; no timing threshold or sleep assumes scheduling delay.
	requireSignal(t, op.ctx.Done())
	s.launchHost(op.token, op.run)
	if s.next() != task {
		t.Fatal("expired request did not resume")
	}
	if task.RV[0] != 0 || task.RV[1].([]string) != nil || task.RV[2].([]byte) != nil || !errors.Is(task.RV[3].(error), context.DeadlineExceeded) {
		t.Fatalf("late submission returned partial success: %v", task.RV)
	}
	if requests.Load() != 0 || StdContextContextErr(parent) != nil || parent.c.done.c.closed {
		t.Fatal("request timeout reached transport or canceled source parent")
	}
	if len(parent.c.hooks) != 0 {
		t.Fatal("expired submission retained cancel hook")
	}
}
