package ir

import (
	"fmt"
	"go/token"
	"strings"
)

// Handle is an exported root struct type whose pointer crosses a library
// boundary as an opaque object owned by an instance.
type Handle struct {
	// Ptr is *T and Elem is T.
	Ptr, Elem *Type
	// Name is the source type name, used as the public name.
	Name string
	// Methods lists the exported methods callable from the host, in source
	// order, excluding Close.
	Methods []*Func
	// Close is the source Close() error method, if any. Host Close runs it
	// before releasing the handle.
	Close *Func
}

// LibraryABI describes the handles reachable from a library's exports.
type LibraryABI struct {
	Handles []*Handle
	byPtr   map[*Type]*Handle
}

// Handle returns the handle t denotes, or nil when t is not a handle type.
func (a *LibraryABI) Handle(t *Type) *Handle {
	if a == nil {
		return nil
	}
	return a.byPtr[t]
}

// HandleParams reports whether any non-receiver parameter of f is a handle.
func (a *LibraryABI) HandleParams(f *Func) bool {
	params := f.Sig.Params
	if f.Recv != nil {
		params = params[1:]
	}
	for _, t := range params {
		if a.Handle(t) != nil {
			return true
		}
	}
	return false
}

// HandleResults reports whether any result of f is a handle.
func (a *LibraryABI) HandleResults(f *Func) bool {
	for _, t := range f.Sig.Results {
		if a.Handle(t) != nil {
			return true
		}
	}
	return false
}

// ioPackage holds the std/io interfaces a host may pass as stream arguments.
const ioPackage = "github.com/eugenioenko/goalchemy/std/io"

// HostIOPackage adapts host streams to the std/io interfaces.
const HostIOPackage = "github.com/eugenioenko/goalchemy/std/io/hostio"

var streamKinds = map[string]bool{"Reader": true, "Writer": true, "ReadCloser": true, "WriteCloser": true,
	"ReadSeeker": true, "ReadSeekCloser": true, "WriteSeeker": true, "ReadWriter": true}

// StreamKind returns the std/io interface name of a host stream parameter
// type, or "" when t is not one.
func StreamKind(t *Type) string {
	if t.Kind == KNamed && t.Pkg == ioPackage && streamKinds[t.Obj] {
		return t.Obj
	}
	return ""
}

// StreamReads reports whether a stream kind supports Read, and so on.
func StreamReads(kind string) bool  { return strings.Contains(kind, "Read") }
func StreamWrites(kind string) bool { return strings.Contains(kind, "Write") }
func StreamSeeks(kind string) bool  { return strings.Contains(kind, "Seek") }
func StreamCloses(kind string) bool { return strings.Contains(kind, "Close") }

// StreamAdapter returns the hostio function that wraps a stream id as kind.
func (p *Program) StreamAdapter(kind string) *Func {
	for _, f := range p.Funcs {
		if f.Pkg == HostIOPackage && f.Recv == nil && f.Name == "hostio."+kind {
			return f
		}
	}
	return nil
}

// MethodName returns the source method name of a handle method.
func MethodName(f *Func) string { return f.Name[strings.LastIndex(f.Name, ".")+1:] }

// LibraryHandles finds the handle types of p: root exported structs whose
// pointers appear directly in export signatures, or in the signatures of
// exposed methods of other handles. value reports whether a target admits a
// type as a copied value. Exported functions whose signatures contain other
// shapes are reported as errors; methods that cannot cross are not exposed.
func (p *Program) LibraryHandles(value func(*Type) bool) (*LibraryABI, error) {
	roots := map[string]bool{}
	for _, pkg := range p.Packages {
		if pkg.Root {
			roots[pkg.Path] = true
		}
	}
	a := &LibraryABI{byPtr: map[*Type]*Handle{}}
	handleOf := func(t *Type) *Handle {
		if h := a.byPtr[t]; h != nil {
			return h
		}
		if t.Kind != KPointer || t.Elem == nil {
			return nil
		}
		e := t.Elem
		if e.Kind != KNamed || !roots[e.Pkg] || !token.IsExported(e.Obj) || e.U().Kind != KStruct {
			return nil
		}
		h := &Handle{Ptr: t, Elem: e, Name: e.Obj}
		a.byPtr[t] = h
		a.Handles = append(a.Handles, h)
		return h
	}
	isContext := func(t *Type) bool { return t.Kind == KOpaque && t.Name == "context.Context" }
	isError := func(t *Type) bool { return t.Kind == KNamed && t.Name == "error" }
	crosses := func(t *Type) bool { return value(t) || handleOf(t) != nil }
	check := func(f *Func, params []*Type) error {
		if f.Sig.Variadic {
			return fmt.Errorf("variadic source parameters are unsupported")
		}
		for _, t := range params {
			if !isContext(t) && !crosses(t) && (StreamKind(t) == "" || p.StreamAdapter(StreamKind(t)) == nil) {
				return fmt.Errorf("unsupported parameter %s", t.Name)
			}
		}
		results := f.Sig.Results
		for i, t := range results {
			if !(i == len(results)-1 && isError(t)) && !crosses(t) {
				return fmt.Errorf("unsupported result %s", t.Name)
			}
		}
		return nil
	}
	// Methods are only probed: a signature that cannot cross must not create
	// handles as a side effect.
	probe := func(f *Func) bool {
		if f.Sig.Variadic {
			return false
		}
		ok := func(t *Type) bool { return value(t) || isHandleShape(t, roots) }
		for _, t := range f.Sig.Params[1:] {
			if !isContext(t) && !ok(t) && (StreamKind(t) == "" || p.StreamAdapter(StreamKind(t)) == nil) {
				return false
			}
		}
		for i, t := range f.Sig.Results {
			if !(i == len(f.Sig.Results)-1 && isError(t)) && !ok(t) {
				return false
			}
		}
		return true
	}
	for _, f := range p.Exports {
		if err := check(f, f.Sig.Params); err != nil {
			return nil, fmt.Errorf("library export %s: %v", f.Name[strings.LastIndex(f.Name, ".")+1:], err)
		}
	}
	for i := 0; i < len(a.Handles); i++ {
		h := a.Handles[i]
		for _, f := range p.Funcs {
			if f.Wrapper || f.Recv == nil || f.MethodID == "" || (f.RecvType != h.Ptr && f.RecvType != h.Elem) {
				continue
			}
			name := MethodName(f)
			if !token.IsExported(name) {
				continue
			}
			if name == "Close" {
				if len(f.Sig.Params) == 1 && len(f.Sig.Results) == 1 && isError(f.Sig.Results[0]) {
					h.Close = f
					continue
				}
				return nil, fmt.Errorf("library handle %s: Close must have signature Close() error", h.Name)
			}
			if !probe(f) {
				continue
			}
			for _, t := range f.Sig.Params[1:] {
				handleOf(t)
			}
			for _, t := range f.Sig.Results {
				handleOf(t)
			}
			h.Methods = append(h.Methods, f)
		}
	}
	return a, nil
}

func isHandleShape(t *Type, roots map[string]bool) bool {
	if t.Kind != KPointer || t.Elem == nil {
		return false
	}
	e := t.Elem
	return e.Kind == KNamed && roots[e.Pkg] && token.IsExported(e.Obj) && e.U().Kind == KStruct
}
