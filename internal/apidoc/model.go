// Package apidoc extracts source documentation and native API declarations.
package apidoc

// Catalog is the versioned, deterministic documentation interchange format.
type Catalog struct {
	SchemaVersion int       `json:"schema_version"`
	Compiler      string    `json:"compiler"`
	Title         string    `json:"title"`
	Packages      []Package `json:"packages"`
	Symbols       []*Symbol `json:"symbols"`
	Targets       []Target  `json:"targets"`
}

// Package identifies a documented source package.
type Package struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Root        bool     `json:"root"`
	Symbols     []string `json:"symbols"`
}

// Symbol identifies a source API member independently of generated private names.
type Symbol struct {
	ID          string    `json:"id"`
	Package     string    `json:"package"`
	Name        string    `json:"name"`
	Kind        string    `json:"kind"`
	Parent      string    `json:"parent,omitempty"`
	Description string    `json:"description,omitempty"`
	Deprecated  string    `json:"deprecated,omitempty"`
	Signature   string    `json:"signature"`
	Location    Location  `json:"location"`
	Parameters  []Field   `json:"parameters,omitempty"`
	Results     []Field   `json:"results,omitempty"`
	Fields      []Field   `json:"fields,omitempty"`
	Members     []string  `json:"members,omitempty"`
	References  []string  `json:"references,omitempty"`
	Bindings    []Binding `json:"bindings,omitempty"`
	Root        bool      `json:"root"`
}

// Location uses package-relative paths so documentation is portable and reproducible.
type Location struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

// Type is a source spelling with explicit links to named symbols.
type Type struct {
	Text       string   `json:"text"`
	References []string `json:"references,omitempty"`
}

// Field describes a parameter, result, struct field, or interface method.
type Field struct {
	Name        string `json:"name"`
	Type        Type   `json:"type"`
	Description string `json:"description,omitempty"`
	Tag         string `json:"tag,omitempty"`
	Embedded    bool   `json:"embedded,omitempty"`
}

// Binding is a native declaration captured from a library emitter.
type Binding struct {
	Target     string `json:"target"`
	Name       string `json:"name"`
	Signature  string `json:"signature"`
	Invocation string `json:"invocation,omitempty"`
}

// Target records whether the selected API can be emitted for a native language.
type Target struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}
