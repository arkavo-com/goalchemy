package rt

import (
	"context"
	"time"

	nativehttp "github.com/eugenioenko/goalchemy/lib/http"
)

// LibHTTPOpen starts an exchange without waiting for the connection. Its
// context ends with the source context or the source deadline.
func LibHTTPOpen(t *Task, c Context, method, url string, headers []string, contentLength int64, timeoutMillis int64, options []string) {
	s := sched
	if c.c == nil || (c.c != background && c.c.owner != s) {
		_, err := nativehttp.Open(nil, method, url, headers, contentLength, timeoutMillis, options)
		t.RV = []any{(*Exchange)(nil), err}
		return
	}
	if !s.host {
		panic(HostFault{Value: "HTTP requires host-clock scheduler"})
	}
	if err := StdContextContextErr(c); err != nil {
		t.RV = []any{(*Exchange)(nil), err}
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	if c.c.hasDeadline {
		cancel()
		ctx, cancel = context.WithDeadline(context.Background(), s.epoch.Add(time.Duration(c.c.deadline)))
	}
	n, err := nativehttp.Open(ctx, method, url, append([]string(nil), headers...), contentLength, timeoutMillis, append([]string(nil), options...))
	if err != nil {
		cancel()
		t.RV = []any{(*Exchange)(nil), err}
		return
	}
	ex := &Exchange{n: n, cancel: cancel, owner: s}
	if c.c != background {
		if c.c.hooks == nil {
			c.c.hooks = make(map[uint64]func())
		}
		s.nextOperation++
		id, state := s.nextOperation, c.c
		state.hooks[id] = cancel
		ex.unhook = func() { delete(state.hooks, id) }
	}
	ex.track()
	t.RV = []any{ex, nil}
}
