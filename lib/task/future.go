package task

import (
	"reflect"
	"sync"
)

// FutureState is the runtime capability carried by a typed future.
type FutureState interface {
	futureState() *futureState
}

// Future stores the results of a zero-argument function with signature F.
type Future[F any] struct {
	state FutureState
}

type futureState struct {
	done     chan struct{}
	mu       sync.Mutex
	results  []any
	panic    any
	observed bool
}

func (f *futureState) futureState() *futureState { return f }

var futures struct {
	sync.Mutex
	states []*futureState
}

// Start queues fn without yielding and returns its typed future.
func Start[F any](fn F) Future[F] {
	return Future[F]{start(func() []any {
		f := reflect.ValueOf(fn)
		if f.IsNil() {
			var p *int
			_ = *p
		}
		values := f.Call(nil)
		results := make([]any, len(values))
		for i, value := range values {
			results[i] = value.Interface()
		}
		return results
	})}
}

// Await returns a future's results in Goalchemy source. Native Go reference
// programs invoke the returned function; the frontend inserts that invocation.
func Await[F any](f Future[F]) F {
	t := reflect.TypeOf((*F)(nil)).Elem()
	return reflect.MakeFunc(t, func([]reflect.Value) []reflect.Value {
		results := await(f.state)
		values := make([]reflect.Value, len(results))
		for i, result := range results {
			values[i] = reflect.New(t.Out(i)).Elem()
			if result != nil {
				values[i].Set(reflect.ValueOf(result))
			}
		}
		return values
	}).Interface().(F)
}

func start(fn any) FutureState {
	f := &futureState{done: make(chan struct{})}
	futures.Lock()
	futures.states = append(futures.states, f)
	futures.Unlock()
	go func() {
		defer close(f.done)
		defer func() { f.panic = recover() }()
		f.results = fn.(func() []any)()
	}()
	return f
}

func await(state FutureState) []any {
	if state == nil {
		select {}
	}
	f := state.futureState()
	f.mu.Lock()
	f.observed = true
	f.mu.Unlock()
	<-f.done
	if f.panic != nil {
		panic(f.panic)
	}
	return f.results
}

// CheckUnawaited is called by the reference-Go runner at executable exit.
func CheckUnawaited() {
	futures.Lock()
	states := append([]*futureState(nil), futures.states...)
	futures.Unlock()
	for _, f := range states {
		select {
		case <-f.done:
			f.mu.Lock()
			observed := f.observed
			f.mu.Unlock()
			if !observed && f.panic != nil {
				panic(f.panic)
			}
		default:
		}
	}
}
