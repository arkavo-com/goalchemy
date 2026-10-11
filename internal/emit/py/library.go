package py

import (
	"fmt"
	"github.com/eugenioenko/goalchemy/internal/ir"
	"go/token"
	"strings"
)

type LibraryBoundaryError struct{ Message string }

func (e *LibraryBoundaryError) Error() string { return e.Message }
func libraryValue(t *ir.Type, seen map[*ir.Type]bool) bool {
	if seen[t] {
		return true
	}
	seen[t] = true
	u := t.U()
	switch u.Kind {
	case ir.KBool, ir.KInt, ir.KString, ir.KFloat:
		return true
	case ir.KArray, ir.KSlice:
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
func (e *emitter) library() (string, error) {
	// These are Go-exported names but Python keywords, so preserving the public
	// name cannot produce a valid Python function declaration in either mode.
	for _, f := range e.p.Exports {
		name := f.Name[strings.LastIndex(f.Name, ".")+1:]
		switch name {
		case "None", "True", "False":
			return "", &LibraryBoundaryError{"reserved Python public function name " + name}
		}
	}

	abi, err := e.p.LibraryHandles(func(t *ir.Type) bool { return libraryValue(t, map[*ir.Type]bool{}) })
	if err != nil {
		return "", &LibraryBoundaryError{err.Error()}
	}
	e.use("core.task.spawn")
	e.use("std.context.with_cancel")
	var b strings.Builder
	seen := map[*ir.Type]bool{}
	var convert func(*ir.Type)
	convert = func(t *ir.Type) {
		u := t.U()
		if seen[u] {
			return
		}
		seen[u] = true
		switch u.Kind {
		case ir.KStruct:
			for _, f := range u.Fields {
				convert(f.Type)
			}
		case ir.KArray, ir.KSlice:
			convert(u.Elem)
		}
		fmt.Fprintf(&b, "def _input_%s(v):\n", e.names.Type(u, ""))
		switch u.Kind {
		case ir.KFloat:
			fmt.Fprintf(&b, "    return rt.library_float(v, %d)\n", u.FloatBits)
		case ir.KBool:
			b.WriteString("    return rt.library_bool(v)\n")
		case ir.KInt:
			fmt.Fprintf(&b, "    return rt.library_int(v, %d, %s)\n", u.Int.Bits(), pyBool(u.Int.Signed()))
		case ir.KString:
			b.WriteString("    return rt.library_string(v)\n")
		case ir.KStruct:
			fmt.Fprintf(&b, "    rt.library_fields(v, %q)\n    r = %s()\n", strings.Join(func() []string {
				var n []string
				for _, f := range u.Fields {
					n = append(n, f.Name)
				}
				return n
			}(), ","), e.class(u))
			for i, f := range u.Fields {
				fmt.Fprintf(&b, "    if %q in v: r.%s = _input_%s(v[%q])\n", f.Name, e.fieldProp(u, i), e.names.Type(f.Type.U(), ""), f.Name)
			}
			b.WriteString("    return r\n")
		case ir.KArray, ir.KSlice:
			if byteElem(u.Elem) {
				b.WriteString("    a = rt.library_bytes(v)\n")
			} else {
				fmt.Fprintf(&b, "    a = None if v is None else [_input_%s(x) for x in rt.library_list(v)]\n", e.names.Type(u.Elem.U(), ""))
			}
			if u.Kind == ir.KArray {
				fmt.Fprintf(&b, "    if a is None or len(a) != %d: raise rt.LibraryFailure('invalid_argument')\n    return a\n", u.Len)
			} else {
				fmt.Fprintf(&b, "    return rt.Slice(a, 0, 0 if a is None else len(a), 0 if a is None else len(a), %s)\n", pyBool(byteElem(u.Elem)))
			}
		}
		fmt.Fprintf(&b, "\ndef _output_%s(v):\n", e.names.Type(u, ""))
		switch u.Kind {
		case ir.KBool, ir.KInt, ir.KString, ir.KFloat:
			b.WriteString("    return v\n")
		case ir.KStruct:
			b.WriteString("    return {\n")
			for i, f := range u.Fields {
				fmt.Fprintf(&b, "        %q: _output_%s(v.%s),\n", f.Name, e.names.Type(f.Type.U(), ""), e.fieldProp(u, i))
			}
			b.WriteString("    }\n")
		case ir.KArray, ir.KSlice:
			expr := "v"
			if u.Kind == ir.KSlice {
				b.WriteString("    if v.a is None: return None\n")
				expr = "v.a[v.o:v.o+v.l]"
			}
			if byteElem(u.Elem) {
				if u.Kind == ir.KSlice {
					expr = "memoryview(v.a)[v.o:v.o+v.l]"
				}
				fmt.Fprintf(&b, "    return bytes(%s)\n", expr)
			} else {
				fmt.Fprintf(&b, "    return [_output_%s(x) for x in %s]\n", e.names.Type(u.Elem.U(), ""), expr)
			}
		}
		b.WriteString("\n")
	}
	funcs := append([]*ir.Func{}, e.p.Exports...)
	for _, h := range abi.Handles {
		funcs = append(funcs, h.Methods...)
	}
	for _, f := range funcs {
		params := f.Sig.Params
		if f.Recv != nil {
			params = params[1:]
		}
		for _, t := range params {
			if !sourceContext(t) && abi.Handle(t) == nil && ir.StreamKind(t) == "" {
				convert(t)
			}
		}
		for _, t := range f.Sig.Results {
			if !sourceError(t) && abi.Handle(t) == nil {
				convert(t)
			}
		}
	}
	for _, t := range e.p.Types.All {
		if t.Kind == ir.KPointer && t.Elem.U().Kind == ir.KStruct && libraryValue(t.Elem, map[*ir.Type]bool{}) && e.tds[t] != "" {
			convert(t.Elem)
		}
	}
	b.WriteString("set_log_handler = rt.set_log_handler\nlogging_handler = rt.logging_handler\nLogRecord = rt.LogRecord\n\n")
	b.WriteString("def _source_failure(err):\n    if err is None: return None\n")
	for _, t := range e.p.Types.All {
		if t.Kind == ir.KPointer && t.Elem.U().Kind == ir.KStruct && libraryValue(t.Elem, map[*ir.Type]bool{}) && e.tds[t] != "" {
			fmt.Fprintf(&b, "    if err.t is %s: return rt.LibraryFailure('source', _output_%s(err.v))\n", e.tds[t], e.names.Type(t.Elem.U(), ""))
		}
	}
	b.WriteString("    return rt.LibraryFailure('source')\n\ndef _library_zero():\n")
	for _, g := range e.p.Globals {
		slot := e.symbol(g.Sym)
		if g.AddrTaken && !g.Type.IsAggregate() {
			slot += ".v"
		}
		fmt.Fprintf(&b, "    %s = %s\n", slot, e.zero(g.Type))
	}
	b.WriteString("    pass\n\ndef _library_reset():\n    _library_zero()\n    rt.library_clear_refs()\n\ndef _library_save():\n    return [")
	for _, g := range e.p.Globals {
		slot := e.symbol(g.Sym)
		if g.AddrTaken && !g.Type.IsAggregate() {
			slot += ".v"
		}
		fmt.Fprintf(&b, "%s, ", slot)
	}
	b.WriteString("]\n\ndef _library_load(v):\n")
	for i, g := range e.p.Globals {
		slot := e.symbol(g.Sym)
		if g.AddrTaken && !g.Type.IsAggregate() {
			slot += ".v"
		}
		fmt.Fprintf(&b, "    %s = v[%d]\n", slot, i)
	}
	b.WriteString("    pass\n\n_library_state = rt.LibraryState(_library_reset, _library_zero, _library_save, _library_load)\n\n")
	init := e.symbol(e.p.Init.Sym) + "()"
	if !e.p.Init.MaySuspend {
		init = "rt.sync(lambda: (" + init + ", [])[1])"
	}
	factory := func(f *ir.Func, as []string, indent string) string {
		call := e.symbol(f.Sym) + "(" + strings.Join(as, ", ") + ")"
		if !f.MaySuspend {
			if len(f.Sig.Results) == 0 {
				call = "rt.sync(lambda: (" + call + ", [])[1])"
			} else if len(f.Sig.Results) == 1 {
				call = "rt.sync(lambda: [" + call + "])"
			} else {
				call = "rt.sync(lambda: list(" + call + "))"
			}
		}
		return fmt.Sprintf("%sdef factory(ctx, owned, fresh):\n%s    call = lambda: %s\n%s    return rt.LibrarySequence(%s, call) if fresh else call()\n", indent, indent, call, indent, init)
	}
	used := map[string]bool{}
	for _, h := range abi.Handles {
		if used[h.Name] {
			return "", &LibraryBoundaryError{"duplicate Python public name " + h.Name}
		}
		used[h.Name] = true
		fmt.Fprintf(&b, "class %s:\n    \"\"\"Handle to a source *%s owned by a library instance. Call close() or use\n    it as a context manager; a collected handle is released without running Close.\"\"\"\n    __slots__ = ('_h', '__weakref__')\n    def __init__(self, h): self._h = h\n", h.Name, h.Name)
		b.WriteString("    def close(self):\n        \"\"\"Runs the source Close, if any, and releases the handle. Queued calls\n        fail and an active call is canceled first.\"\"\"\n        h0 = self._h\n")
		if h.Close != nil {
			b.WriteString(factory(h.Close, []string{"rt.library_obj(h0)"}, "        "))
		} else {
			b.WriteString("        factory = None\n")
		}
		b.WriteString("        return rt.library_close(h0, _library_state, factory, _source_failure)\n    def __enter__(self): return self\n    def __exit__(self, *exc):\n        self.close().result()\n        return False\n    async def __aenter__(self): return self\n    async def __aexit__(self, *exc):\n        await self.close()\n        return False\n")
		for _, f := range h.Methods {
			if err := e.libraryWrapper(&b, abi, ir.MethodName(f), h, f, factory); err != nil {
				return "", err
			}
		}
		fmt.Fprintf(&b, "\ndef _wrap_%s(inst, v):\n    return rt.library_wrap(inst, v, %q, %s)\n\n", h.Name, h.Name, h.Name)
	}
	for _, f := range e.p.Exports {
		name := f.Name[strings.LastIndex(f.Name, ".")+1:]
		if used[name] {
			return "", &LibraryBoundaryError{"duplicate Python export"}
		}
		used[name] = true
		if err := e.libraryWrapper(&b, abi, name, nil, f, factory); err != nil {
			return "", err
		}
	}
	return b.String(), nil
}

func (e *emitter) libraryWrapper(b *strings.Builder, abi *ir.LibraryABI, name string, recv *ir.Handle, f *ir.Func, factory func(*ir.Func, []string, string) string) error {
	indent := ""
	params := f.Sig.Params
	ps := []string{}
	var as, owned, hs, conv, streams []string
	if recv != nil {
		indent = "    "
		params = params[1:]
		ps = append(ps, "self")
		hs = append(hs, "self._h")
		as = append(as, "rt.library_obj(hs[0])")
	}
	for i, t := range params {
		if sourceContext(t) {
			as = append(as, "ctx")
			continue
		}
		a := fmt.Sprintf("a%d", i)
		ps = append(ps, a)
		if k := ir.StreamKind(t); k != "" {
			as = append(as, fmt.Sprintf("(None if ids[%d] == 0 else %s(ids[%d]))", len(streams), e.symbol(e.p.StreamAdapter(k).Sym), len(streams)))
			streams = append(streams, fmt.Sprintf("rt.library_stream(%s, %q)", a, k))
			continue
		}
		if h := abi.Handle(t); h != nil {
			conv = append(conv, fmt.Sprintf("rt.library_handle(%s, %s)", a, h.Name))
			hs = append(hs, fmt.Sprintf("hv[%d]", len(conv)-1))
			as = append(as, fmt.Sprintf("rt.library_obj(hs[%d])", len(hs)-1))
			continue
		}
		owned = append(owned, a)
		as = append(as, fmt.Sprintf("_input_%s(owned[%d])", e.names.Type(t.U(), ""), len(owned)-1))
	}
	ps = append(ps, "options=None")
	fmt.Fprintf(b, "%sdef %s(%s):\n", indent, name, strings.Join(ps, ", "))
	in := indent + "    "
	if len(conv) > 0 {
		fmt.Fprintf(b, "%stry: hv = [%s]\n%sexcept rt.LibraryFailure as e: return rt.library_failed(e)\n", in, strings.Join(conv, ", "), in)
	}
	fmt.Fprintf(b, "%sids = [0] * %d\n", in, len(streams))
	if len(streams) > 0 {
		fmt.Fprintf(b, "%stry: sv = [%s]\n%sexcept rt.LibraryFailure as e: return rt.library_failed(e)\n", in, strings.Join(streams, ", "), in)
	} else {
		fmt.Fprintf(b, "%ssv = []\n", in)
	}
	fmt.Fprintf(b, "%shs = [%s]\n", in, strings.Join(hs, ", "))
	b.WriteString(factory(f, as, in))
	fmt.Fprintf(b, "%sdef output(rv, inst):\n", in)
	results := f.Sig.Results
	n := len(results)
	if n > 0 && sourceError(results[n-1]) {
		n--
		fmt.Fprintf(b, "%s    err = _source_failure(rv[%d])\n%s    if err is not None: raise err\n", in, n, in)
	}
	out := func(i int) string {
		if h := abi.Handle(results[i]); h != nil {
			return fmt.Sprintf("_wrap_%s(inst, rv[%d])", h.Name, i)
		}
		return fmt.Sprintf("_output_%s(rv[%d])", e.names.Type(results[i].U(), ""), i)
	}
	if n == 0 {
		fmt.Fprintf(b, "%s    return None\n", in)
	} else if n == 1 {
		fmt.Fprintf(b, "%s    return %s\n", in, out(0))
	} else {
		var vals []string
		for i := 0; i < n; i++ {
			vals = append(vals, out(i))
		}
		fmt.Fprintf(b, "%s    return (%s,)\n", in, strings.Join(vals, ", "))
	}
	fmt.Fprintf(b, "%sreturn rt.library_submit(factory, output, _library_state, [%s], options, hs, None, sv, ids)\n\n", in, strings.Join(owned, ", "))
	return nil
}
