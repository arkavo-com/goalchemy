// Package library is the documentation generator's portable SDK fixture.
package library

import "github.com/eugenioenko/goalchemy/lib/context"

// Item is a copied value accepted by [Echo] and [Client.Next].
type Item struct {
	// Name identifies the item. Literal HTML such as <script> is text.
	Name string `json:"name"`
	Data []byte
}

// Alias retains Item's source identity as an alias.
type Alias = Item

// Reader provides an interface whose methods can be browsed individually.
type Reader interface {
	// Read accepts an Item.
	Read(input Item) (Item, error)
}

// Mode identifies the default mode.
const Mode = "safe"

// Client owns a counter across calls.
type Client struct{ count int }

// New creates a Client. Call Close when finished.
func New() *Client { return &Client{} }

// Next returns the input and advances the counter.
func (c *Client) Next(ctx context.Context, input Item) (Item, error) {
	c.count++
	return input, nil
}

// Count returns the number of calls.
func (c Client) Count() int { return c.count }

// Close releases the source resources.
func (c *Client) Close() error { return nil }

// Echo returns a copy of input. See [Item].
//
// Deprecated: use Client.Next instead.
func Echo(input Item) (Item, error) { return input, nil }
