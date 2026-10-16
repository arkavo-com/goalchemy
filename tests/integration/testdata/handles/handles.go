// Package handles exercises exported handles: persistent instances, derived
// handles, identity, Close, poisoning, cancellation, native resources and
// value receivers.
package handles

import (
	"github.com/eugenioenko/goalchemy/lib/context"
	"github.com/eugenioenko/goalchemy/lib/crypto"
	"github.com/eugenioenko/goalchemy/std/errors"
	"github.com/eugenioenko/goalchemy/std/time"
)

var ErrNegative = errors.New("negative")

var inits int
var created int
var closes int

func init() { inits++ }

// Counter is a handle with private state.
type Counter struct {
	name string
	n    int
	last error
	key  *crypto.Key
	busy bool
}

// Snapshot is a value copied across the boundary.
type Snapshot struct {
	Name    string
	N       int
	Inits   int
	Created int
	Closes  int
	LastNeg bool
}

// New starts an instance whose counter owns a native key.
func New(name string) (*Counter, error) {
	key, err := crypto.GenerateP256()
	if err != nil {
		return nil, err
	}
	created++
	return &Counter{name: name, key: key}, nil
}

// Inits reports package initializations seen by a fresh call.
func Inits() int { return inits }

// Nil returns a nil handle.
func Nil() *Counter { return nil }

// Sum adds two counters of the same instance.
func Sum(a, b *Counter) int {
	if a == nil || b == nil {
		return -1
	}
	return a.n + b.n
}

func (c *Counter) Add(d int) (int, error) {
	if c.busy {
		panic("overlapping calls")
	}
	c.busy = true
	defer func() { c.busy = false }()
	if d < 0 {
		c.last = ErrNegative
		return c.n, ErrNegative
	}
	c.n += d
	return c.n, nil
}

// Name has a value receiver.
func (c Counter) Name() string { return c.name }

func (c *Counter) Snap() Snapshot {
	return Snapshot{c.name, c.n, inits, created, closes, errors.Is(c.last, ErrNegative)}
}

// Self returns the receiver, so hosts can check handle identity.
func (c *Counter) Self() *Counter { return c }

// Sibling creates a counter in the same instance.
func (c *Counter) Sibling(name string) *Counter {
	created++
	return &Counter{name: name, key: c.key}
}

// Child returns a derived handle that shares the counter's state.
func (c *Counter) Child() *Child { return &Child{parent: c} }

// PublicKey uses the native key created by New in an earlier call.
func (c *Counter) PublicKey() (string, error) { return c.key.PublicPEM() }

// Wait sleeps in steps until ms elapse or ctx is canceled.
func (c *Counter) Wait(ctx context.Context, ms int) error {
	for i := 0; i < ms; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		time.Sleep(time.Millisecond)
	}
	return nil
}

// Boom panics, poisoning the instance.
func (c *Counter) Boom() { panic("boom") }

// Leak leaves a goroutine running when the call returns.
func (c *Counter) Leak() {
	ch := make(chan int)
	go func() { <-ch }()
}

func (c *Counter) Close() error {
	closes++
	if c.name == "bad-close" {
		return errors.New("close failed")
	}
	return nil
}

// Child is a derived handle without a source Close.
type Child struct{ parent *Counter }

func (h *Child) Bump() int {
	h.parent.n++
	return h.parent.n
}

func (h *Child) Parent() *Counter { return h.parent }

// Coord is a handle whose value-receiver methods run on a copy of the coord.
type Coord struct{ X, Y int }

// NewCoord starts an instance that owns a coord.
func NewCoord(x, y int) *Coord { return &Coord{x, y} }

func (p Coord) Total() int { return p.X + p.Y }

// Shift moves its copy, not the handle's coord.
func (p Coord) Shift(d int) int {
	p.X += d
	return p.X
}

// Moved returns a moved copy.
func (p Coord) Moved(d int) Coord {
	p.X += d
	return p
}

func (p Coord) Less(o Coord) bool { return p.X < o.X || p.X == o.X && p.Y < o.Y }

func (p *Coord) Move(d int) int {
	p.X += d
	return p.X
}

func (p Coord) Close() error {
	if p.X < 0 {
		return errors.New("negative coord")
	}
	return nil
}
