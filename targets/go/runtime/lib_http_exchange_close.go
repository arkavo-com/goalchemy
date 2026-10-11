package rt

import (
	"context"
	"sync"

	nativehttp "github.com/eugenioenko/goalchemy/lib/http"
)

// Exchange is a lib/http exchange owned by one scheduler. Its context is
// canceled with the source context it was opened under.
type Exchange struct {
	n      *nativehttp.Exchange
	cancel context.CancelFunc
	unhook func()
	owner  *scheduler
}

var exchanges struct {
	mu   sync.Mutex
	live map[*scheduler]map[*Exchange]struct{}
}

// track registers ex for release when its scheduler retires.
func (ex *Exchange) track() {
	exchanges.mu.Lock()
	defer exchanges.mu.Unlock()
	if exchanges.live == nil {
		exchanges.live = map[*scheduler]map[*Exchange]struct{}{}
	}
	set := exchanges.live[ex.owner]
	if set == nil {
		set = map[*Exchange]struct{}{}
		exchanges.live[ex.owner] = set
		owner := ex.owner
		owner.retire = append(owner.retire, func() {
			exchanges.mu.Lock()
			set := exchanges.live[owner]
			delete(exchanges.live, owner)
			exchanges.mu.Unlock()
			for e := range set {
				e.cancel()
				e.n.Close()
			}
		})
	}
	set[ex] = struct{}{}
}

func (ex *Exchange) untrack() {
	exchanges.mu.Lock()
	delete(exchanges.live[ex.owner], ex)
	exchanges.mu.Unlock()
}

// exchangeOp runs work on a host worker; canceling the operation cancels the
// exchange.
func exchangeOp(t *Task, ex *Exchange, work func(*nativehttp.Exchange) []any) {
	if ex == nil {
		t.RV = work(nil)
		return
	}
	s := sched
	if ex.owner != s {
		panic(HostFault{Value: "HTTP exchange belongs to another scheduler"})
	}
	token := s.registerHost(t, ex.cancel, func() {}, func() []any { return nil })
	s.launchHost(token, func() []any { return work(ex.n) })
}

// LibHTTPExchangeClose ends the exchange and releases its connection.
func LibHTTPExchangeClose(t *Task, ex *Exchange) {
	if ex == nil {
		t.RV = nil
		return
	}
	ex.cancel()
	if ex.unhook != nil {
		ex.unhook()
		ex.unhook = nil
	}
	ex.untrack()
	exchangeOp(t, ex, func(n *nativehttp.Exchange) []any { n.Close(); return nil })
}
