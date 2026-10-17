package golang

import (
	"fmt"
	"go/token"
	"strings"

	"github.com/eugenioenko/goalchemy/internal/ir"
	"github.com/eugenioenko/goalchemy/internal/naming"
)

// LibraryBoundaryError reports a deliberately unsupported public ABI shape.
type LibraryBoundaryError struct{ Message string }

func (e *LibraryBoundaryError) Error() string { return e.Message }

// Library exports deliberately admit immutable value trees, not arbitrary
// source pointers, channels, maps, callbacks or native key handles.
func libraryValue(t *ir.Type, seen map[*ir.Type]bool) bool {
	if seen[t] {
		return true
	}
	seen[t] = true
	u := t.U()
	switch u.Kind {
	case ir.KBool, ir.KInt, ir.KString, ir.KFloat:
		return true
	case ir.KSlice, ir.KArray:
		return libraryValue(u.Elem, seen)
	case ir.KStruct:
		for _, f := range u.Fields {
			if !token.IsExported(f.Name) || f.Embedded || !libraryValue(f.Type, seen) {
				return false
			}
		}
		return true
	}
	return false
}
func sourceContext(t *ir.Type) bool { return t.Kind == ir.KOpaque && t.Name == "context.Context" }
func sourceError(t *ir.Type) bool   { return t.Kind == ir.KNamed && t.Name == "error" }

// libraryNames gives every named value exposed through the boundary a stable
// public alias before type declarations, including nested dependency values.
func (e *emitter) libraryNames() error {
	e.publicTypes = map[*ir.Type]string{}
	abi, err := e.p.LibraryHandles(func(t *ir.Type) bool { return libraryValue(t, map[*ir.Type]bool{}) })
	if err != nil {
		return err
	}
	e.abi = abi
	used := map[string]bool{"Callbacks": true, "Callback": true, "LibraryError": true}
	roots := map[string]bool{}
	for _, f := range e.p.Exports {
		roots[f.Pkg] = true
		used[f.Name[strings.LastIndex(f.Name, ".")+1:]] = true
	}
	handleElems := map[*ir.Type]bool{}
	for _, h := range abi.Handles {
		if used[h.Name] {
			return fmt.Errorf("duplicate library public name %s", h.Name)
		}
		used[h.Name] = true
		handleElems[h.Elem] = true
	}
	for _, t := range e.p.Types.All {
		if t.Kind != ir.KNamed || !roots[t.Pkg] || !token.IsExported(t.Obj) || handleElems[t] || !libraryValue(t, map[*ir.Type]bool{}) {
			continue
		}
		if used[t.Obj] {
			return fmt.Errorf("duplicate library public name %s", t.Obj)
		}
		used[t.Obj] = true
		e.publicTypes[t] = t.Obj
	}
	seen := map[*ir.Type]bool{}
	var visit func(*ir.Type)
	visit = func(t *ir.Type) {
		if seen[t] || !libraryValue(t, map[*ir.Type]bool{}) {
			return
		}
		seen[t] = true
		if t.Kind == ir.KNamed && t.Pkg != "" && e.publicTypes[t] == "" {
			alias := fmt.Sprintf("Source_%s_%d", naming.Identifier(t.Name), t.ID)
			for used[alias] {
				alias += "_"
			}
			used[alias] = true
			e.publicTypes[t] = alias
		}
		u := t.U()
		switch u.Kind {
		case ir.KStruct:
			for _, field := range u.Fields {
				visit(field.Type)
			}
		case ir.KArray, ir.KSlice:
			visit(u.Elem)
		}
	}
	// Root aliases may expose dependency values even when not a direct parameter.
	for _, t := range e.p.Types.All {
		if e.publicTypes[t] != "" {
			visit(t)
		}
	}
	for _, f := range e.libraryFuncs() {
		for _, t := range f.Sig.Params {
			visit(t)
		}
		for _, t := range f.Sig.Results {
			visit(t)
		}
	}
	return nil
}

// libraryFuncs lists the exports and every exposed handle method.
func (e *emitter) libraryFuncs() []*ir.Func {
	fs := append([]*ir.Func{}, e.p.Exports...)
	for _, h := range e.abi.Handles {
		fs = append(fs, h.Methods...)
		if h.Close != nil {
			fs = append(fs, h.Close)
		}
	}
	return fs
}

func (e *emitter) library() error {
	e.use("core.task.spawn")
	e.use("std.context.background")
	e.use("std.context.with_cancel")
	e.use("std.context.with_timeout")
	e.use("std.context.err")
	e.p_("type Callbacks = rt.Callbacks\ntype Callback = rt.Callback\ntype LibraryError = rt.LibraryError\n")
	// Public aliases retain native fields and integer/slice types. Names are
	// allocated independently of compact internal naming, including dependencies.
	for _, t := range e.p.Types.All {
		if alias, ok := e.publicTypes[t]; ok {
			e.p_("type %s = %s\n", alias, e.typeNames[t])
		}
	}
	aliases := map[string]bool{"Callbacks": true, "Callback": true, "LibraryError": true}
	for _, alias := range e.publicTypes {
		aliases[alias] = true
	}
	for _, h := range e.abi.Handles {
		aliases[h.Name] = true
	}
	e.p_("func _libraryReset() {\n")
	for _, g := range e.p.Globals {
		e.p_("%s = *new(%s)\n", e.names.Symbol(g.Sym), e.typ(g.Type))
	}
	e.p_("}\n")
	e.p_("func _librarySave() any {\nreturn []any{")
	for _, g := range e.p.Globals {
		e.p_("%s, ", e.names.Symbol(g.Sym))
	}
	e.p_("}\n}\nfunc _libraryLoad(v any) {\n_saved := v.([]any)\n_ = _saved\n")
	for i, g := range e.p.Globals {
		e.p_("%s, _ = _saved[%d].(%s)\n", e.names.Symbol(g.Sym), i, e.typ(g.Type))
	}
	e.p_("}\nvar _libraryState = &rt.LibraryState{Reset: _libraryReset, Save: _librarySave, Load: _libraryLoad}\n")
	for _, h := range e.abi.Handles {
		e.p_("// %s is a handle to a source *%s owned by a library instance. Call Close\n// when done; a handle reclaimed by the garbage collector is released\n// without running Close.\n", h.Name, h.Name)
		e.p_("type %s struct{ h *rt.Handle }\n", h.Name)
		e.p_("func %s(_inst *rt.Instance, _p %s) *%s {\nif _p == nil { return nil }\nreturn rt.Wrap(_inst, any(_p), %q, func(_h *rt.Handle) *%s { return &%s{h: _h} })\n}\n",
			e.names.Derived("wrap", h.Name), e.typ(h.Ptr), h.Name, h.Name, h.Name, h.Name)
		e.p_("func (x *%s) handle() *rt.Handle {\nif x == nil { return nil }\nreturn x.h\n}\n", h.Name)
		closeRun := "nil"
		if h.Close != nil {
			closeRun = "func(_sourceCtx rt.Context, _fresh bool) rt.Frame {\n" + e.libraryCallFrame(h.Close, []string{e.handleRecv(h, h.Close, "x.h")}) + "\n}"
		}
		e.p_("// Close runs the source Close, if any, and releases the handle. Calls\n// queued on the handle fail and an active call is canceled first.\nfunc (x *%s) Close() error {\nif x == nil { return &LibraryError{Kind: \"invalid_handle\"} }\nreturn rt.CloseHandle(x.h, _libraryState, %s)\n}\n", h.Name, closeRun)
	}
	kinds := map[string]bool{}
	for _, f := range e.libraryFuncs() {
		for _, t := range f.Sig.Results {
			if k := ir.StreamKind(t); k != "" && !kinds[k] {
				kinds[k] = true
				close := ""
				if ir.StreamCloses(k) {
					close = "func(_sourceCtx rt.Context, _fresh bool) rt.Frame {\n" + e.libraryCallFrame(e.p.HostIOFunc("CloseView"), []string{"rt.Obj[*rt.StreamObj](_h).V.(" + e.typ(e.p.HostIOFunc("CloseView").Sig.Params[0]) + ")"}) + "\n}"
				}
				e.streamOps(e.names.Derived("streamOps", k), ir.KindOps(k), func(iface *ir.Type) string {
					return "rt.Obj[*rt.StreamObj](_h).V.(" + e.typ(iface) + ")"
				}, close)
			}
		}
	}
	for _, h := range e.abi.Handles {
		if !h.Stream.Any() {
			continue
		}
		for _, f := range h.Methods {
			if ir.MethodName(f) == "IO" {
				return &LibraryBoundaryError{"library handle " + h.Name + " is a stream and cannot also declare method IO"}
			}
		}
		close := "nil"
		if h.Close != nil {
			close = "func(_sourceCtx rt.Context, _fresh bool) rt.Frame {\n" + e.libraryCallFrame(h.Close, []string{e.handleRecv(h, h.Close, "_h")}) + "\n}"
		}
		e.streamOps(e.names.Derived("streamOps", h.Name), h.Stream, func(iface *ir.Type) string {
			return e.typ(iface) + "(rt.Obj[" + e.typ(h.Ptr) + "](_h))"
		}, close)
		var parts []string
		for _, p := range []struct {
			on   bool
			name string
		}{{h.Stream.Read, "io.Reader"}, {h.Stream.Write, "io.Writer"}, {h.Stream.Seek, "io.Seeker"}, {true, "io.Closer"}} {
			if p.on {
				parts = append(parts, p.name)
			}
		}
		e.p_("// IO returns the handle as a standard io stream. Each operation is a call\n// on the handle; Close closes the handle.\nfunc (x *%s) IO() interface{ %s } {\nif x == nil || x.h == nil { return nil }\nreturn rt.NewStream(x.h, %s)\n}\n", h.Name, strings.Join(parts, "; "), e.names.Derived("streamOps", h.Name))
	}
	for _, f := range e.p.Exports {
		name := f.Name[strings.LastIndex(f.Name, ".")+1:]
		if aliases[name] {
			return fmt.Errorf("duplicate library public name %s", name)
		}
		aliases[name] = true
		e.libraryWrapper(name, nil, f)
	}
	for _, h := range e.abi.Handles {
		for _, f := range h.Methods {
			e.libraryWrapper(ir.MethodName(f), h, f)
		}
	}
	return nil
}

// streamOps declares the rt.StreamOps that run hostio view functions on a
// handle's source object; obj converts it to a std/io interface type.
func (e *emitter) streamOps(name string, ops ir.StreamOps, obj func(*ir.Type) string, close string) {
	call := func(fn string, args []string, own string) string {
		f := e.p.HostIOFunc(fn)
		args = append([]string{obj(f.Sig.Params[0])}, args...)
		return fmt.Sprintf("_rv, _err := rt.RunLibraryCall(context.Background(), nil, _libraryState, []*rt.Handle{_h}, func(_sourceCtx rt.Context, _fresh bool) rt.Frame {\n%s\n}, func(_inst *rt.Instance, _rv []any) []any {\n_owned := make([]any, len(_rv))\n%s\nif _rv[len(_rv)-1] != nil { _owned[len(_rv)-1] = rt.SnapshotError(_rv[len(_rv)-1].(error)) }\nreturn _owned\n}, nil, nil)\n", e.libraryCallFrame(f, args), own)
	}
	param := func(fn string, i int) string { return e.typ(e.p.HostIOFunc(fn).Sig.Params[i]) }
	result := func(fn string, i int) string { return e.typ(e.p.HostIOFunc(fn).Sig.Results[i]) }
	e.p_("var %s = &rt.StreamOps{\n", name)
	if ops.Read {
		e.p_("Read: func(_h *rt.Handle, _n int) ([]byte, bool, error) {\n%sif _err != nil { return nil, false, _err }\nif _rv[2] != nil { return nil, false, _rv[2].(error) }\nreturn []byte(_rv[0].(%s)), _rv[1].(bool), nil\n},\n",
			call("ReadView", []string{param("ReadView", 1) + "(_n)"}, fmt.Sprintf("_owned[0] = rt.Snapshot(_rv[0].(%s))\n_owned[1] = _rv[1]", result("ReadView", 0))), result("ReadView", 0))
	}
	if ops.Write {
		e.p_("Write: func(_h *rt.Handle, _p []byte) (int, error) {\n%sif _err != nil { return 0, _err }\n_n := int(_rv[0].(%s))\nif _rv[1] != nil { return _n, _rv[1].(error) }\nreturn _n, nil\n},\n",
			call("WriteView", []string{param("WriteView", 1) + "(_p)"}, "_owned[0] = _rv[0]"), result("WriteView", 0))
	}
	if ops.Seek {
		e.p_("Seek: func(_h *rt.Handle, _offset int64, _whence int) (int64, error) {\n%sif _err != nil { return 0, _err }\nif _rv[1] != nil { return 0, _rv[1].(error) }\nreturn int64(_rv[0].(%s)), nil\n},\n",
			call("SeekView", []string{param("SeekView", 1) + "(_offset)", param("SeekView", 2) + "(_whence)"}, "_owned[0] = _rv[0]"), result("SeekView", 0))
	}
	if close == "" {
		close = "nil"
	}
	e.p_("Close: func(_h *rt.Handle) error { return rt.CloseHandle(_h, _libraryState, %s) },\n}\n", close)
}

// libraryCallFrame renders the frame body that calls f with the given source
// arguments, initializing packages first when the owner is fresh.
func (e *emitter) libraryCallFrame(f *ir.Func, args []string) string {
	if e.native() {
		init := e.names.Symbol(e.p.Init.Sym) + "()"
		if e.p.Init.MaySuspend {
			init = e.names.Symbol(e.p.Init.Sym) + "(_t)"
		}
		if f.MaySuspend {
			args = append([]string{"_t"}, args...)
		}
		call := e.funcRef(f) + "(" + strings.Join(args, ", ") + ")"
		return "return rt.NativeTask(func(_t *rt.Task) []any {\nif _fresh {\n_libraryReset()\n" + init + "\n}\n" + syncFrameBody(len(f.Sig.Results), call) + "\n})"
	}
	init := e.names.Symbol(e.p.Init.Sym) + "()"
	if !e.p.Init.MaySuspend {
		init = "rt.Sync(func() []any { " + e.names.Symbol(e.p.Init.Sym) + "(); return nil })"
	}
	call := e.funcRef(f) + "(" + strings.Join(args, ", ") + ")"
	if !f.MaySuspend {
		call = "rt.Sync(func() []any { " + syncFrameBody(len(f.Sig.Results), call) + " })"
	}
	return "_call := func() rt.Frame { return " + call + " }\nif _fresh {\n_libraryReset()\nreturn rt.LibrarySequence(" + init + ", _call)\n}\nreturn _call()"
}

// handleRecv is the source receiver argument of method f of h on handle hv.
// Dereferencing copies the object for a value receiver.
func (e *emitter) handleRecv(h *ir.Handle, f *ir.Func, hv string) string {
	obj := "rt.Obj[" + e.typ(h.Ptr) + "](" + hv + ")"
	if h.ValueReceiver(f) {
		return "*" + obj
	}
	return obj
}

func (e *emitter) libraryWrapper(name string, recv *ir.Handle, f *ir.Func) {
	results := f.Sig.Results
	params := f.Sig.Params
	if recv != nil {
		params = params[1:]
	}
	hasError := len(results) > 0 && sourceError(results[len(results)-1])
	n := len(results)
	if hasError {
		n--
	}
	publicType := func(t *ir.Type) string {
		if h := e.abi.Handle(t); h != nil {
			return "*" + h.Name
		}
		if k := ir.StreamKind(t); k != "" {
			return "io." + k
		}
		return e.typ(t)
	}
	ps := []string{"ctx context.Context"}
	as := []string{}
	handles := []string{}
	streams := []string{}
	if recv != nil {
		handles = append(handles, "x.h")
		as = append(as, e.handleRecv(recv, f, "x.h"))
	}
	for i, t := range params {
		if sourceContext(t) {
			as = append(as, "_sourceCtx")
			continue
		}
		a := fmt.Sprintf("a%d", i)
		ps = append(ps, a+" "+publicType(t))
		if h := e.abi.Handle(t); h != nil {
			handles = append(handles, a+".handle()")
			as = append(as, "rt.Obj["+e.typ(t)+"]("+a+".handle())")
			continue
		}
		if k := ir.StreamKind(t); k != "" {
			as = append(as, fmt.Sprintf("func() %s { if _ids[%d] == 0 { return nil }; return %s(_ids[%d]) }()", e.typ(t), len(streams), e.names.Symbol(e.p.StreamAdapter(k).Sym), len(streams)))
			streams = append(streams, "rt.HostStream("+a+")")
			continue
		}
		as = append(as, a)
	}
	ps = append(ps, "callbacks ...Callbacks")
	rs := []string{}
	for i := 0; i < n; i++ {
		rs = append(rs, fmt.Sprintf("r%d %s", i, publicType(results[i])))
	}
	rs = append(rs, "err error")
	if recv != nil {
		e.p_("func (x *%s) %s(%s) (%s) {\n", recv.Name, name, strings.Join(ps, ", "), strings.Join(rs, ", "))
		e.p_("if x == nil || x.h == nil { return %s }\n", libraryErrorReturn(n, "&LibraryError{Kind: \"invalid_handle\"}"))
	} else {
		e.p_("func %s(%s) (%s) {\n", name, strings.Join(ps, ", "), strings.Join(rs, ", "))
	}
	for i, t := range params {
		if !sourceContext(t) && e.abi.Handle(t) == nil && ir.StreamKind(t) == "" {
			e.p_("a%d = rt.Snapshot(a%d)\n", i, i)
		}
	}
	streamList := "nil"
	if len(streams) > 0 {
		streamList = "[]rt.Callback{" + strings.Join(streams, ", ") + "}"
	}
	e.p_("_ids := make([]int64, %d)\n_ = _ids\n", len(streams))
	e.p_("var _registry Callbacks\nif len(callbacks)>1 { return %s }\nif len(callbacks)==1 { _registry=callbacks[0] }\n", libraryErrorReturn(n, "&LibraryError{Kind: \"invalid_callbacks\"}"))
	handleList := "nil"
	if len(handles) > 0 {
		handleList = "[]*rt.Handle{" + strings.Join(handles, ", ") + "}"
	}
	e.p_("_rv, _runErr := rt.RunLibraryCall(ctx, _registry, _libraryState, %s, func(_sourceCtx rt.Context, _fresh bool) rt.Frame {\n%s\n}, func(_inst *rt.Instance, _rv []any) []any {\n_owned := make([]any, len(_rv))\n", handleList, e.libraryCallFrame(f, as))
	for i := 0; i < n; i++ {
		if h := e.abi.Handle(results[i]); h != nil {
			e.p_("_owned[%d] = %s(_inst, _rv[%d].(%s))\n", i, e.names.Derived("wrap", h.Name), i, e.typ(results[i]))
			continue
		}
		if k := ir.StreamKind(results[i]); k != "" {
			e.p_("if _rv[%d] != nil { _owned[%d] = rt.Wrap(_inst, any(&rt.StreamObj{V: _rv[%d]}), \"io.%s\", func(_h *rt.Handle) *rt.Stream { return rt.NewStream(_h, %s) }).As(%q) }\n", i, i, i, k, e.names.Derived("streamOps", k), k)
			continue
		}
		e.p_("_owned[%d] = rt.Snapshot(_rv[%d].(%s))\n", i, i, e.typ(results[i]))
	}
	if hasError {
		e.p_("if _rv[%d]!=nil { _owned[%d]=rt.SnapshotError(_rv[%d].(error)) }\n", n, n, n)
	}
	e.p_("return _owned\n}, %s, _ids)\n", streamList)
	e.p_("if _runErr != nil { return %s }\n_ = _rv\n", libraryErrorReturn(n, "_runErr"))
	for i := 0; i < n; i++ {
		e.p_("r%d, _ = _rv[%d].(%s)\n", i, i, publicType(results[i]))
	}
	if hasError {
		e.p_("if _rv[%d] != nil { err = _rv[%d].(error) }\n", n, n)
	}
	e.p_("return\n}\n")
}
func libraryErrorReturn(n int, err string) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf("r%d", i)
	}
	return strings.Join(append(parts, err), ", ")
}
