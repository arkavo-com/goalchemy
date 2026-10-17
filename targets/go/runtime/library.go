package rt

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"time"
)

// LibraryError separates boundary/cancellation, source panic and adapter fault.
// Panic values are intentionally omitted: they can contain host secrets.
type LibraryError struct {
	Kind  string
	Cause error
}

func (e *LibraryError) Error() string { return "goalchemy library: " + e.Kind }
func (e *LibraryError) Unwrap() error { return e.Cause }

var libraryGate = make(chan struct{}, 1)

// Snapshot copies permitted value trees while retaining nil/empty slices.
func Snapshot[T any](v T) T { return snapshot(reflect.ValueOf(&v).Elem()).Interface().(T) }
func snapshot(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		switch v.Type().Elem().Kind() {
		case reflect.Bool, reflect.String, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
			reflect.Copy(out, v)
			return out
		}
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(snapshot(v.Index(i)))
		}
		return out
	case reflect.Array, reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		if v.Kind() == reflect.Array {
			for i := 0; i < v.Len(); i++ {
				out.Index(i).Set(snapshot(v.Index(i)))
			}
		} else {
			for i := 0; i < v.NumField(); i++ {
				out.Field(i).Set(snapshot(v.Field(i)))
			}
		}
		return out
	default:
		return v
	}
}

// SnapshotError retains typed public value-tree errors without borrowing source
// globals. Context sentinels and errors.New values are intrinsically immutable.
// Arbitrary private/native handle-bearing error layouts are outside this ABI.
func SnapshotError(err error) error {
	if err == nil {
		return nil
	}
	v := reflect.ValueOf(err)
	t := v.Type()
	base := t
	if t.Kind() == reflect.Pointer {
		base = t.Elem()
	}
	if base.PkgPath() == "errors" && base.Name() == "errorString" {
		return err
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return err
		}
		if errorValueType(t.Elem(), map[reflect.Type]bool{}) {
			out := reflect.New(t.Elem())
			out.Elem().Set(snapshot(v.Elem()))
			return out.Interface().(error)
		}
	} else if errorValueType(t, map[reflect.Type]bool{}) {
		return snapshot(v).Interface().(error)
	}
	return &LibraryError{Kind: "unsupported_error"}
}
func errorValueType(t reflect.Type, seen map[reflect.Type]bool) bool {
	if seen[t] {
		return true
	}
	seen[t] = true
	switch t.Kind() {
	case reflect.Bool, reflect.String, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		return true
	case reflect.Array, reflect.Slice:
		return errorValueType(t.Elem(), seen)
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if field.PkgPath != "" || !errorValueType(field.Type, seen) {
				return false
			}
		}
		return true
	}
	return false
}

// RunLibrary owns one fresh scheduler and package initialization per call.
// Reservation is shared by all exports in this generated package/runtime.
// Queued cancellation never changes the active owner. Active cancellation is
// applied at driver boundaries and wakes an owner waiting for host operations.
func RunLibrary(ctx context.Context, callbacks Callbacks, build func(Context) Frame, reset func(), own func([]any) []any) (rv []any, err error) {
	var ownAll func(*Instance, []any) []any
	if own != nil {
		ownAll = func(_ *Instance, rv []any) []any { return own(rv) }
	}
	return runLibrary(ctx, callbacks, &LibraryState{Reset: reset}, nil, func(c Context, _ bool) Frame { return build(c) }, ownAll, nil, nil)
}

func runLibrary(ctx context.Context, callbacks Callbacks, state *LibraryState, handles []*Handle, build func(Context, bool) Frame, own func(*Instance, []any) []any, closing *Handle, streams *pendingStreams) (rv []any, err error) {
	// Covers native boundary failures before scheduler construction/reservation.
	defer func() {
		if recover() != nil {
			rv = nil
			err = &LibraryError{Kind: "host_fault"}
		}
	}()
	if ctx == nil {
		return nil, &LibraryError{Kind: "invalid_context"}
	}
	if reentrantStream() {
		return nil, &LibraryError{Kind: "reentrant"}
	}
	// Registry copied before queue acquisition, alongside generated input snapshots.
	registry := make(Callbacks, len(callbacks))
	for name, cb := range callbacks {
		registry[name] = cb
	}
	var stop <-chan struct{}
	var self *Handle
	for _, h := range handles {
		if h != nil {
			self = h
			break
		}
	}
	if self != nil && self != closing {
		stop = self.closing
		select {
		case <-stop:
			return nil, &LibraryError{Kind: "closed"}
		default:
		}
	}
	select {
	case libraryGate <- struct{}{}:
	case <-ctx.Done():
		return nil, &LibraryError{Kind: "canceled", Cause: ctx.Err()}
	case <-stop:
		return nil, &LibraryError{Kind: "closed"}
	}
	defer func() { <-libraryGate }()
	if ctx.Err() != nil {
		return nil, &LibraryError{Kind: "canceled", Cause: ctx.Err()}
	}
	var inst *Instance
	for _, h := range handles {
		if h == nil {
			continue
		}
		if h.released || (h != closing && h.isClosed()) {
			return nil, &LibraryError{Kind: "closed"}
		}
		if inst != nil && h.inst != inst {
			return nil, &LibraryError{Kind: "instance_mismatch"}
		}
		inst = h.inst
	}
	fresh := inst == nil
	if fresh {
		inst = &Instance{objects: map[any]*instanceEntry{}}
	} else if inst.poisoned {
		return nil, &LibraryError{Kind: "poisoned"}
	}
	if self != nil && self != closing {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		if !self.setActive(cancel) {
			return nil, &LibraryError{Kind: "closed"}
		}
		defer self.setActive(nil)
	}
	sched.shutdown()
	s := &scheduler{rng: seed(), nextID: 1, harness: true, host: true, epoch: time.Now(), library: true}
	sched = s
	s.callbacks = registerStreams(inst, registry, streams)
	keep := false
	defer func() {
		p := recover()
		if p != nil && !fresh {
			inst.poisoned = true
		}
		if keep && p == nil {
			if s.live > 0 {
				LibraryWarn(fmt.Sprintf("%d goroutine(s) still running when a handle call returned were abandoned", s.live))
			}
			if state.Save != nil {
				inst.globals = state.Save()
			}
			inst.retire = append(inst.retire, s.retire...)
			s.retire = nil
		}
		s.shutdown()
		s.callbacks = nil
		if state.Reset != nil {
			state.Reset()
		}
		if closing != nil {
			closing.release()
		}
		if (keep || closing != nil) && inst.live == 0 {
			inst.retireNow()
		}
		// No executable or old library owner survives retirement.
		sched = &scheduler{rng: 1}
		if p != nil {
			rv = nil
			switch p.(type) {
			case fatalPanicSignal:
				err = &LibraryError{Kind: "source_panic"}
			case HostFault:
				err = &LibraryError{Kind: "host_fault"}
			case deadlockSignal:
				err = &LibraryError{Kind: "deadlock"}
			default:
				err = &LibraryError{Kind: "host_fault"}
			}
		}
	}()
	if !fresh && state.Load != nil {
		state.Load(inst.globals)
	}
	if !fresh {
		keep = true
	}
	root, cancel := StdContextWithCancel(StdContextBackground())
	defer cancel()
	if deadline, ok := ctx.Deadline(); ok {
		cancel()
		root, cancel = StdContextWithTimeout(StdContextBackground(), time.Until(deadline).Nanoseconds())
		defer cancel()
	}
	s.boundary = func() {
		if e := ctx.Err(); e != nil {
			root.c.cancel(e)
		}
	}
	s.boundary()
	// This watcher publishes wake only; source contexts belong to the driver.
	watchDone := make(chan struct{})
	var watch sync.WaitGroup
	watch.Add(1)
	mailbox := s.mailbox()
	go func() {
		defer watch.Done()
		select {
		case <-ctx.Done():
			select {
			case mailbox.wake <- struct{}{}:
			default:
			}
		case <-watchDone:
		}
	}()
	defer func() { close(watchDone); watch.Wait() }()
	frame := build(root, fresh)
	main := &Task{Frame: frame, deferTarget: -1}
	s.main = main
	s.ready(main)
	s.drive()
	rv = frame.Results()
	// A canceled root cannot report successful partial plaintext/results.
	if ctx.Err() != nil {
		return nil, &LibraryError{Kind: "canceled", Cause: ctx.Err()}
	}
	if own != nil {
		rv = own(inst, rv)
	}
	if fresh && inst.live > 0 {
		keep = true
	}
	return rv, nil
}

type librarySequence struct {
	FrameBase
	init   Frame
	call   func() Frame
	result []any
}

func LibrarySequence(init Frame, call func() Frame) Frame {
	return &librarySequence{init: init, call: call}
}
func (f *librarySequence) Results() []any { return f.result }
func (f *librarySequence) Step(t *Task) {
	switch f.PC {
	case 0:
		f.PC = 1
		Call(t, f.init)
	case 1:
		f.PC = 2
		Call(t, f.call())
	default:
		f.result = t.RV
		Ret(t, f)
	}
}
