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
	e.p_("func libraryReset() {\n")
	for _, g := range e.p.Globals {
		e.p_("%s = *new(%s)\n", e.names.Symbol(g.Sym), e.typ(g.Type))
	}
	e.p_("}\n")
	e.p_("func librarySave() any {\nreturn []any{")
	for _, g := range e.p.Globals {
		e.p_("%s, ", e.names.Symbol(g.Sym))
	}
	e.p_("}\n}\nfunc libraryLoad(v any) {\nsaved := v.([]any)\n_ = saved\n")
	for i, g := range e.p.Globals {
		e.p_("%s, _ = saved[%d].(%s)\n", e.names.Symbol(g.Sym), i, e.typ(g.Type))
	}
	e.p_("}\nvar libraryState = &rt.LibraryState{Reset: libraryReset, Save: librarySave, Load: libraryLoad}\n")
	for _, h := range e.abi.Handles {
		e.p_("// %s is a handle to a source *%s owned by a library instance. Call Close\n// when done; a handle reclaimed by the garbage collector is released\n// without running Close.\n", h.Name, h.Name)
		e.p_("type %s struct{ h *rt.Handle }\n", h.Name)
		e.p_("func wrap%s(inst *rt.Instance, p %s) *%s {\nif p == nil { return nil }\nreturn rt.Wrap(inst, any(p), %q, func(h *rt.Handle) *%s { return &%s{h: h} })\n}\n",
			h.Name, e.typ(h.Ptr), h.Name, h.Name, h.Name, h.Name)
		e.p_("func (x *%s) handle() *rt.Handle {\nif x == nil { return nil }\nreturn x.h\n}\n", h.Name)
		closeRun := "nil"
		if h.Close != nil {
			closeRun = "func(sourceCtx rt.Context, fresh bool) rt.Frame {\n" + e.libraryCallFrame(h.Close, []string{"rt.Obj[" + e.typ(h.Ptr) + "](x.h)"}) + "\n}"
		}
		e.p_("// Close runs the source Close, if any, and releases the handle. Calls\n// queued on the handle fail and an active call is canceled first.\nfunc (x *%s) Close() error {\nif x == nil { return &LibraryError{Kind: \"invalid_handle\"} }\nreturn rt.CloseHandle(x.h, libraryState, %s)\n}\n", h.Name, closeRun)
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

// libraryCallFrame renders the frame body that calls f with the given source
// arguments, initializing packages first when the owner is fresh.
func (e *emitter) libraryCallFrame(f *ir.Func, args []string) string {
	init := e.names.Symbol(e.p.Init.Sym) + "()"
	if !e.p.Init.MaySuspend {
		init = "rt.Sync(func() []any { " + e.names.Symbol(e.p.Init.Sym) + "(); return nil })"
	}
	call := e.funcRef(f) + "(" + strings.Join(args, ", ") + ")"
	if !f.MaySuspend {
		call = "rt.Sync(func() []any { " + syncFrameBody(len(f.Sig.Results), call) + " })"
	}
	return "call := func() rt.Frame { return " + call + " }\nif fresh {\nlibraryReset()\nreturn rt.LibrarySequence(" + init + ", call)\n}\nreturn call()"
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
		as = append(as, "rt.Obj["+e.typ(recv.Ptr)+"](x.h)")
	}
	for i, t := range params {
		if sourceContext(t) {
			as = append(as, "sourceCtx")
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
			as = append(as, fmt.Sprintf("func() %s { if ids[%d] == 0 { return nil }; return %s(ids[%d]) }()", e.typ(t), len(streams), e.names.Symbol(e.p.StreamAdapter(k).Sym), len(streams)))
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
	e.p_("ids := make([]int64, %d)\n_ = ids\n", len(streams))
	e.p_("var registry Callbacks\nif len(callbacks)>1 { return %s }\nif len(callbacks)==1 { registry=callbacks[0] }\n", libraryErrorReturn(n, "&LibraryError{Kind: \"invalid_callbacks\"}"))
	handleList := "nil"
	if len(handles) > 0 {
		handleList = "[]*rt.Handle{" + strings.Join(handles, ", ") + "}"
	}
	e.p_("rv, runErr := rt.RunLibraryCall(ctx, registry, libraryState, %s, func(sourceCtx rt.Context, fresh bool) rt.Frame {\n%s\n}, func(inst *rt.Instance, rv []any) []any {\nowned := make([]any, len(rv))\n", handleList, e.libraryCallFrame(f, as))
	for i := 0; i < n; i++ {
		if h := e.abi.Handle(results[i]); h != nil {
			e.p_("owned[%d] = wrap%s(inst, rv[%d].(%s))\n", i, h.Name, i, e.typ(results[i]))
			continue
		}
		e.p_("owned[%d] = rt.Snapshot(rv[%d].(%s))\n", i, i, e.typ(results[i]))
	}
	if hasError {
		e.p_("if rv[%d]!=nil { owned[%d]=rt.SnapshotError(rv[%d].(error)) }\n", n, n, n)
	}
	e.p_("return owned\n}, %s, ids)\n", streamList)
	e.p_("if runErr != nil { return %s }\n_ = rv\n", libraryErrorReturn(n, "runErr"))
	for i := 0; i < n; i++ {
		e.p_("r%d = rv[%d].(%s)\n", i, i, publicType(results[i]))
	}
	if hasError {
		e.p_("if rv[%d] != nil { err = rv[%d].(error) }\n", n, n)
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
