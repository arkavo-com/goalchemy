package rt

import (
	"bytes"
	"context"
	"errors"
	"io"
	"runtime"
	"strconv"
	"sync"
)

// streamCallers marks goroutines running a host stream method, so a
// library call made from inside one fails fast instead of deadlocking.
var streamCallers sync.Map

func goroutineID() uint64 {
	var buf [64]byte
	b := buf[:runtime.Stack(buf[:], false)]
	b = bytes.TrimPrefix(b, []byte("goroutine "))
	if i := bytes.IndexByte(b, ' '); i > 0 {
		b = b[:i]
	}
	id, _ := strconv.ParseUint(string(b), 10, 64)
	return id
}

func reentrantStream() bool {
	_, ok := streamCallers.Load(goroutineID())
	return ok
}

// pendingStreams carries the streams of one call until its instance is known.
type pendingStreams struct {
	streams []Callback
	ids     []int64
}

// registerStreams assigns instance stream ids and returns the registry the
// call sees: the call's callbacks plus every stream of the instance.
func registerStreams(inst *Instance, registry Callbacks, pending *pendingStreams) Callbacks {
	if inst.streams == nil && pending == nil {
		return registry
	}
	if pending != nil {
		if inst.streams == nil {
			inst.streams = Callbacks{}
		}
		for i, cb := range pending.streams {
			if cb == nil {
				continue
			}
			inst.nextStream++
			pending.ids[i] = inst.nextStream
			inst.streams["goalchemy.stream."+strconv.FormatInt(inst.nextStream, 10)] = cb
		}
	}
	out := make(Callbacks, len(registry)+len(inst.streams))
	for k, v := range registry {
		out[k] = v
	}
	for k, v := range inst.streams {
		out[k] = v
	}
	return out
}

func streamInt(b []byte) int64 {
	var v int64
	for _, c := range b[:8] {
		v = v<<8 | int64(c)
	}
	return v
}

func streamBytes(v int64) []byte {
	b := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		b[i] = byte(v)
		v >>= 8
	}
	return b
}

var errStreamRequest = errors.New("hostio: malformed stream request")

func serveStream(stream any, req []byte) ([]byte, error) {
	if len(req) == 0 {
		return nil, errStreamRequest
	}
	switch req[0] {
	case 'r':
		r, ok := stream.(io.Reader)
		if !ok || len(req) != 9 {
			return nil, errStreamRequest
		}
		n := streamInt(req[1:])
		if n < 0 || n >= 128<<10 {
			return nil, errStreamRequest
		}
		buf := make([]byte, 1+n)
		k, err := r.Read(buf[1:])
		buf[0] = 0
		if k < 0 || k > int(n) {
			return nil, errors.New("hostio: invalid read count")
		}
		if err == io.EOF {
			buf[0] = 1
		} else if err != nil {
			return nil, err
		}
		return buf[:1+k], nil
	case 'w':
		w, ok := stream.(io.Writer)
		if !ok {
			return nil, errStreamRequest
		}
		data := append([]byte(nil), req[1:]...)
		k, err := w.Write(data)
		if err != nil {
			return nil, err
		}
		return append([]byte{0}, streamBytes(int64(k))...), nil
	case 's':
		s, ok := stream.(io.Seeker)
		if !ok || len(req) != 10 {
			return nil, errStreamRequest
		}
		pos, err := s.Seek(streamInt(req[1:]), int(req[9]))
		if err != nil {
			return nil, err
		}
		return append([]byte{0}, streamBytes(pos)...), nil
	case 'c':
		c, ok := stream.(io.Closer)
		if !ok {
			return nil, errStreamRequest
		}
		return []byte{0}, c.Close()
	}
	return nil, errStreamRequest
}

// HostStream serves hostio requests on a host stream. Buffers are copied in
// both directions; host io.EOF is reported to the source as io.EOF.
func HostStream(stream any) Callback {
	if stream == nil {
		return nil
	}
	return func(_ context.Context, req []byte, settle func([]byte, error)) func() {
		id := goroutineID()
		streamCallers.Store(id, true)
		defer streamCallers.Delete(id)
		reply, err := func() (reply []byte, err error) {
			defer func() {
				if p := recover(); p != nil {
					reply, err = nil, errors.New("hostio: host stream panicked")
				}
			}()
			return serveStream(stream, req)
		}()
		if err != nil {
			reply = append([]byte{2}, err.Error()...)
		}
		settle(reply, nil)
		return func() {}
	}
}
