package rt

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sync"
	"weak"
)

// LibraryWarn reports boundary warnings such as leaked handles and abandoned
// goroutines. Hosts may replace it before the first call.
var LibraryWarn = func(msg string) { fmt.Fprintln(os.Stderr, "goalchemy: warning: "+msg) }

// LibraryState saves, restores and zeroes the generated source globals.
type LibraryState struct {
	Reset func()
	Save  func() any
	Load  func(any)
}

// Instance owns the persistent source state behind a family of handles: its
// globals, native resources and handle table. Its fields are only accessed
// while holding the library reservation.
type Instance struct {
	streams    Callbacks
	nextStream int64
	globals    any
	retire     []func()
	objects    map[any]*instanceEntry
	live       int
	poisoned   bool
	retired    bool
}

type instanceEntry struct {
	handle  *Handle
	wrapper any
}

func (inst *Instance) retireNow() {
	if inst.retired {
		return
	}
	inst.retired = true
	for _, close := range inst.retire {
		close()
	}
	inst.retire = nil
	inst.globals = nil
	inst.objects = nil
	inst.streams = nil
}

// Handle is the host's reference to one source object of an instance.
type Handle struct {
	inst     *Instance
	obj      any
	name     string
	released bool

	mu      sync.Mutex
	closed  bool
	closing chan struct{}
	active  context.CancelFunc
}

func (h *Handle) isClosed() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.closed
}

func (h *Handle) setActive(cancel context.CancelFunc) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if cancel != nil && h.closed {
		return false
	}
	h.active = cancel
	return true
}

// release drops the handle from its instance table; it requires the library
// reservation.
func (h *Handle) release() {
	if h.released {
		return
	}
	h.released = true
	inst := h.inst
	if e := inst.objects[h.obj]; e != nil && e.handle == h {
		delete(inst.objects, h.obj)
	}
	inst.live--
	h.obj = nil
}

// Wrap returns the host wrapper for obj, reusing the live wrapper for the
// same source pointer so host identity follows source identity.
func Wrap[W any](inst *Instance, obj any, name string, wrap func(*Handle) *W) *W {
	if e := inst.objects[obj]; e != nil {
		if w := e.wrapper.(weak.Pointer[W]).Value(); w != nil && !e.handle.isClosed() {
			return w
		}
	}
	h := &Handle{inst: inst, obj: obj, name: name, closing: make(chan struct{})}
	w := wrap(h)
	inst.objects[obj] = &instanceEntry{handle: h, wrapper: weak.Make(w)}
	inst.live++
	runtime.AddCleanup(w, func(h *Handle) { go h.finalize() }, h)
	return w
}

// Obj returns the source object of h, or the zero value for a nil handle.
func Obj[T any](h *Handle) T {
	if h == nil || h.obj == nil {
		var zero T
		return zero
	}
	return h.obj.(T)
}

func (h *Handle) finalize() {
	h.mu.Lock()
	closed := h.closed
	h.closed = true
	h.mu.Unlock()
	if closed {
		return
	}
	libraryGate <- struct{}{}
	defer func() { <-libraryGate }()
	if h.released {
		return
	}
	LibraryWarn(h.name + " handle was not closed; releasing it without running Close")
	inst := h.inst
	h.release()
	if inst.live == 0 {
		inst.retireNow()
	}
}

// CloseHandle fails calls queued on h, cancels its active call, runs the
// source Close (when run is non-nil and the instance is healthy), then
// releases h. Later calls on h fail with kind "closed".
func CloseHandle(h *Handle, state *LibraryState, run func(Context, bool) Frame) error {
	if h == nil {
		return &LibraryError{Kind: "invalid_handle"}
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	close(h.closing)
	if h.active != nil {
		h.active()
	}
	h.mu.Unlock()
	if run == nil {
		run = func(Context, bool) Frame { return Sync(func() []any { return []any{nil} }) }
	}
	rv, err := runLibrary(context.Background(), nil, state, []*Handle{h}, run, func(_ *Instance, rv []any) []any {
		if rv[0] != nil {
			rv[0] = SnapshotError(rv[0].(error))
		}
		return rv
	}, h, nil)
	if err != nil {
		var poisoned *LibraryError
		if e, ok := err.(*LibraryError); ok && e.Kind == "poisoned" {
			poisoned = e
		}
		if poisoned != nil {
			libraryGate <- struct{}{}
			inst := h.inst
			h.release()
			if inst.live == 0 {
				inst.retireNow()
			}
			<-libraryGate
			return nil
		}
		return err
	}
	if rv[0] != nil {
		return rv[0].(error)
	}
	return nil
}

// RunLibraryCall runs one export or handle method. handles lists the handle
// arguments, receiver first; a call with no live handle creates a fresh owner,
// which becomes an instance when its results include handles.
//
// streams are host std/io streams passed as arguments: each is registered on
// the instance under a fresh id, written to ids before build runs, and stays
// reachable to later calls on the instance.
func RunLibraryCall(ctx context.Context, callbacks Callbacks, state *LibraryState, handles []*Handle, build func(Context, bool) Frame, own func(*Instance, []any) []any, streams []Callback, ids []int64) ([]any, error) {
	var pending *pendingStreams
	if len(streams) > 0 {
		pending = &pendingStreams{streams, ids}
	}
	return runLibrary(ctx, callbacks, state, handles, build, own, nil, pending)
}
