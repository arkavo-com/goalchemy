package rt

import (
	"context"
	"time"

	nativehttp "github.com/eugenioenko/goalchemy/lib/http"
)

// LibHTTPDo snapshots mutable inputs before yielding and calls the actual
// bounded native transport on a worker. Only the owner applies its result.
func LibHTTPDo(t *Task, c Context, method, url string, headers []string, body []byte, maxResponseBytes int, timeoutMillis int64) {
	libHTTPDo(t, c, method, url, headers, body, maxResponseBytes, timeoutMillis, nil)
}

func libHTTPDo(t *Task, c Context, method, url string, headers []string, body []byte, maxResponseBytes int, timeoutMillis int64, options []string) {
	s := sched
	// Reject coarse input bounds before allocating snapshots. The native
	// implementation remains authoritative for all transport/URL/header checks.
	optionBytes := 0
	for _, o := range options {
		optionBytes += len(o)
	}
	invalidBounds := optionBytes > nativehttp.MaxOptionBytes || len(body) > nativehttp.MaxBytes || maxResponseBytes < 0 || maxResponseBytes > nativehttp.MaxBytes || timeoutMillis < 1 || timeoutMillis > 300000 || len(headers)%2 != 0 || len(headers) > nativehttp.MaxHeaderBytes/2
	if !invalidBounds {
		total := len(headers) * 2 // four framing bytes for each name/value pair
		for _, h := range headers {
			if len(h) > nativehttp.MaxHeaderBytes-total {
				invalidBounds = true
				break
			}
			total += len(h)
		}
	}
	if c.c == nil || (c.c != background && c.c.owner != s) || invalidBounds {
		r0, r1, r2, r3 := nativehttp.DoWith(nil, method, url, headers, body, maxResponseBytes, timeoutMillis, options)
		t.RV = []any{r0, r1, r2, r3}
		return
	}
	if !s.host {
		panic(HostFault{Value: "HTTP requires host-clock scheduler"})
	}
	op := prepareHTTP(s, t, c, method, url, headers, body, maxResponseBytes, timeoutMillis)
	op.options = append([]string(nil), options...)
	s.launchHost(op.token, op.run)
}

type httpOperation struct {
	token       hostToken
	ctx         context.Context
	cancel      context.CancelFunc
	method, url string
	headers     []string
	options     []string
	body        []byte
	max         int
	timeout     int64
}

// prepareHTTP anchors request-only timeout before copies and submission, so
// worker scheduling delay counts against it. The earlier source deadline wins.
func prepareHTTP(s *scheduler, t *Task, c Context, method, url string, headers []string, body []byte, maxResponseBytes int, timeoutMillis int64) *httpOperation {
	StdContextContextErr(c)
	deadline := time.Now().Add(time.Duration(timeoutMillis) * time.Millisecond)
	if c.c.hasDeadline {
		parent := s.epoch.Add(time.Duration(c.c.deadline))
		if parent.Before(deadline) {
			deadline = parent
		}
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	if c.c.err != nil {
		cancel()
	}
	op := &httpOperation{ctx: ctx, cancel: cancel, method: method, url: url,
		headers: append([]string(nil), headers...), body: append([]byte(nil), body...), max: maxResponseBytes, timeout: timeoutMillis}
	cleanup := func() { cancel(); delete(c.c.hooks, op.token.id) }
	canceled := func() []any {
		if err := StdContextContextErr(c); err != nil {
			return []any{0, []string(nil), []byte(nil), err}
		}
		return nil
	}
	op.token = s.registerHost(t, cancel, cleanup, canceled)
	if c.c != background && c.c.err == nil {
		if c.c.hooks == nil {
			c.c.hooks = make(map[uint64]func())
		}
		c.c.hooks[op.token.id] = cancel
	}
	return op
}
func (op *httpOperation) run() []any {
	defer op.cancel()
	r0, r1, r2, r3 := nativehttp.DoWith(op.ctx, op.method, op.url, op.headers, op.body, op.max, op.timeout, op.options)
	return []any{r0, r1, r2, r3}
}
