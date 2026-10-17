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
		fmt.Fprintf(&b, "def %s(_v):\n", e.names.Type(u, "input_"))
		switch u.Kind {
		case ir.KFloat:
			fmt.Fprintf(&b, "    return rt.library_float(_v, %d)\n", u.FloatBits)
		case ir.KBool:
			b.WriteString("    return rt.library_bool(_v)\n")
		case ir.KInt:
			fmt.Fprintf(&b, "    return rt.library_int(_v, %d, %s)\n", u.Int.Bits(), pyBool(u.Int.Signed()))
		case ir.KString:
			b.WriteString("    return rt.library_string(_v)\n")
		case ir.KStruct:
			fmt.Fprintf(&b, "    rt.library_fields(_v, %q)\n    _r = %s()\n", strings.Join(func() []string {
				var n []string
				for _, f := range u.Fields {
					n = append(n, f.Name)
				}
				return n
			}(), ","), e.class(u))
			for i, f := range u.Fields {
				fmt.Fprintf(&b, "    if %q in _v: _r.%s = %s(_v[%q])\n", f.Name, e.fieldProp(u, i), e.names.Type(f.Type.U(), "input_"), f.Name)
			}
			b.WriteString("    return _r\n")
		case ir.KArray, ir.KSlice:
			if byteElem(u.Elem) {
				b.WriteString("    _a = rt.library_bytes(_v)\n")
			} else {
				fmt.Fprintf(&b, "    _a = None if _v is None else [%s(_x) for _x in rt.library_list(_v)]\n", e.names.Type(u.Elem.U(), "input_"))
			}
			if u.Kind == ir.KArray {
				fmt.Fprintf(&b, "    if _a is None or len(_a) != %d: raise rt.LibraryFailure('invalid_argument')\n    return _a\n", u.Len)
			} else {
				fmt.Fprintf(&b, "    return rt.Slice(_a, 0, 0 if _a is None else len(_a), 0 if _a is None else len(_a), %s)\n", pyBool(byteElem(u.Elem)))
			}
		}
		fmt.Fprintf(&b, "\ndef %s(_v):\n", e.names.Type(u, "output_"))
		switch u.Kind {
		case ir.KBool, ir.KInt, ir.KString, ir.KFloat:
			b.WriteString("    return _v\n")
		case ir.KStruct:
			b.WriteString("    return {\n")
			for i, f := range u.Fields {
				fmt.Fprintf(&b, "        %q: %s(_v.%s),\n", f.Name, e.names.Type(f.Type.U(), "output_"), e.fieldProp(u, i))
			}
			b.WriteString("    }\n")
		case ir.KArray, ir.KSlice:
			expr := "_v"
			if u.Kind == ir.KSlice {
				b.WriteString("    if _v.a is None: return None\n")
				expr = "_v.a[_v.o:_v.o+_v.l]"
			}
			if byteElem(u.Elem) {
				if u.Kind == ir.KSlice {
					expr = "memoryview(_v.a)[_v.o:_v.o+_v.l]"
				}
				fmt.Fprintf(&b, "    return bytes(%s)\n", expr)
			} else {
				fmt.Fprintf(&b, "    return [%s(_x) for _x in %s]\n", e.names.Type(u.Elem.U(), "output_"), expr)
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
			if !sourceError(t) && abi.Handle(t) == nil && ir.StreamKind(t) == "" {
				convert(t)
			}
		}
	}
	if e.p.HostIOFunc("ReadView") != nil {
		for _, name := range []string{"ReadView", "WriteView", "SeekView"} {
			f := e.p.HostIOFunc(name)
			for _, t := range f.Sig.Params[1:] {
				convert(t)
			}
			for _, t := range f.Sig.Results[:len(f.Sig.Results)-1] {
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
	b.WriteString("def _source_failure(_err):\n    if _err is None: return None\n")
	for _, t := range e.p.Types.All {
		if t.Kind == ir.KPointer && t.Elem.U().Kind == ir.KStruct && libraryValue(t.Elem, map[*ir.Type]bool{}) && e.tds[t] != "" {
			fmt.Fprintf(&b, "    if _err.t is %s: return rt.LibraryFailure('source', %s(_err.v))\n", e.tds[t], e.names.Type(t.Elem.U(), "output_"))
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
	b.WriteString("]\n\ndef _library_load(_v):\n")
	for i, g := range e.p.Globals {
		slot := e.symbol(g.Sym)
		if g.AddrTaken && !g.Type.IsAggregate() {
			slot += ".v"
		}
		fmt.Fprintf(&b, "    %s = _v[%d]\n", slot, i)
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
		return fmt.Sprintf("%sdef _factory(_ctx, _owned, _fresh):\n%s    _call = lambda: %s\n%s    return rt.LibrarySequence(%s, _call) if _fresh else _call()\n", indent, indent, call, indent, init)
	}
	streamOps := func(name string, ops ir.StreamOps, obj string, closeFactory string) {
		op := func(fn string, params []string, out string) {
			f := e.p.HostIOFunc(fn)
			as := []string{obj}
			for i := range params {
				as = append(as, fmt.Sprintf("%s(_owned[%d])", e.names.Type(f.Sig.Params[i+1].U(), "input_"), i))
			}
			last := len(f.Sig.Results) - 1
			fmt.Fprintf(&b, "def %s_%s(_h, %s):\n    _hs = [_h]\n%s    def _output(_rv, _inst):\n        _err = _source_failure(_rv[%d])\n        if _err is not None: raise _err\n        return %s\n    return rt.library_submit(_factory, _output, _library_state, [%s], None, _hs).result()\n\n",
				name, fn, strings.Join(params, ", "), factory(f, as, "    "), last, out, strings.Join(params, ", "))
		}
		var entries []string
		if ops.Read {
			op("ReadView", []string{"_n"}, fmt.Sprintf("(%s(_rv[0]) or b'', _rv[1])", e.names.Type(e.p.HostIOFunc("ReadView").Sig.Results[0].U(), "output_")))
			entries = append(entries, fmt.Sprintf("'read': %s_ReadView", name))
		}
		if ops.Write {
			op("WriteView", []string{"_data"}, "_rv[0]")
			entries = append(entries, fmt.Sprintf("'write': %s_WriteView", name))
		}
		if ops.Seek {
			op("SeekView", []string{"_offset", "_whence"}, "_rv[0]")
			entries = append(entries, fmt.Sprintf("'seek': %s_SeekView", name))
		}
		fmt.Fprintf(&b, "def %s_close(_h):\n    _hs = [_h]\n", name)
		if closeFactory == "" {
			b.WriteString("    _factory = None\n")
		} else {
			b.WriteString(closeFactory)
		}
		fmt.Fprintf(&b, "    rt.library_close(_h, _library_state, _factory, _source_failure).result()\n\n%s = {%s}\n\n", name, strings.Join(append(entries, fmt.Sprintf("'close': %s_close", name)), ", "))
	}
	kinds := map[string]bool{}
	for _, f := range funcs {
		for _, t := range f.Sig.Results {
			if k := ir.StreamKind(t); k != "" && !kinds[k] {
				kinds[k] = true
				run := ""
				if ir.StreamCloses(k) {
					run = factory(e.p.HostIOFunc("CloseView"), []string{"rt.library_obj(_hs[0])"}, "    ")
				}
				streamOps("_stream_"+k, ir.KindOps(k), "rt.library_obj(_hs[0])", run)
			}
		}
	}
	for _, h := range abi.Handles {
		if h.Stream.Any() {
			run := ""
			if h.Close != nil {
				run = factory(h.Close, []string{e.handleRecv(h, h.Close, "rt.library_obj(_hs[0])")}, "    ")
			}
			streamOps("_stream_"+h.Name, h.Stream, "rt.box("+e.td(h.Ptr)+", rt.library_obj(_hs[0]))", run)
		}
	}
	used := map[string]bool{}
	for _, h := range abi.Handles {
		if used[h.Name] {
			return "", &LibraryBoundaryError{"duplicate Python public name " + h.Name}
		}
		used[h.Name] = true
		fmt.Fprintf(&b, "class %s:\n    \"\"\"Handle to a source *%s owned by a library instance. Call close() or use\n    it as a context manager; a collected handle is released without running Close.\"\"\"\n    __slots__ = ('_h', '__weakref__')\n    def __init__(self, _h): self._h = _h\n", h.Name, h.Name)
		b.WriteString("    def close(self):\n        \"\"\"Runs the source Close, if any, and releases the handle. Queued calls\n        fail and an active call is canceled first.\"\"\"\n        _h0 = self._h\n")
		if h.Close != nil {
			b.WriteString(factory(h.Close, []string{e.handleRecv(h, h.Close, "rt.library_obj(_h0)")}, "        "))
		} else {
			b.WriteString("        _factory = None\n")
		}
		b.WriteString("        return rt.library_close(_h0, _library_state, _factory, _source_failure)\n")
		if h.Stream.Any() {
			fmt.Fprintf(&b, "    def as_file(self):\n        \"\"\"Returns the handle as a binary file-like object (io.RawIOBase). Each\n        operation is a blocking call on the handle; closing it closes the handle.\"\"\"\n        return rt.SourceStream(self._h, _stream_%s)\n", h.Name)
		}
		b.WriteString("    def __enter__(self): return self\n    def __exit__(self, *exc):\n        self.close().result()\n        return False\n    async def __aenter__(self): return self\n    async def __aexit__(self, *exc):\n        await self.close()\n        return False\n")
		for _, f := range h.Methods {
			if err := e.libraryWrapper(&b, abi, ir.MethodName(f), h, f, factory); err != nil {
				return "", err
			}
		}
		fmt.Fprintf(&b, "\ndef _wrap_%s(_inst, _v):\n    return rt.library_wrap(_inst, _v, %q, %s)\n\n", h.Name, h.Name, h.Name)
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

// handleRecv is the source receiver argument of method f of h on obj; a value
// receiver gets a copy.
func (e *emitter) handleRecv(h *ir.Handle, f *ir.Func, obj string) string {
	if h.ValueReceiver(f) {
		return e.cloneExpr(f.RecvType, obj)
	}
	return obj
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
		as = append(as, e.handleRecv(recv, f, "rt.library_obj(_hs[0])"))
	}
	for i, t := range params {
		if sourceContext(t) {
			as = append(as, "_ctx")
			continue
		}
		a := fmt.Sprintf("_a%d", i)
		ps = append(ps, a)
		if k := ir.StreamKind(t); k != "" {
			as = append(as, fmt.Sprintf("(None if _ids[%d] == 0 else %s(_ids[%d]))", len(streams), e.symbol(e.p.StreamAdapter(k).Sym), len(streams)))
			streams = append(streams, fmt.Sprintf("rt.library_stream(%s, %q)", a, k))
			continue
		}
		if h := abi.Handle(t); h != nil {
			conv = append(conv, fmt.Sprintf("rt.library_handle(%s, %s)", a, h.Name))
			hs = append(hs, fmt.Sprintf("_hv[%d]", len(conv)-1))
			as = append(as, fmt.Sprintf("rt.library_obj(_hs[%d])", len(hs)-1))
			continue
		}
		owned = append(owned, a)
		as = append(as, fmt.Sprintf("%s(_owned[%d])", e.names.Type(t.U(), "input_"), len(owned)-1))
	}
	ps = append(ps, "options=None")
	fmt.Fprintf(b, "%sdef %s(%s):\n", indent, name, strings.Join(ps, ", "))
	in := indent + "    "
	if len(conv) > 0 {
		fmt.Fprintf(b, "%stry: _hv = [%s]\n%sexcept rt.LibraryFailure as _e: return rt.library_failed(_e)\n", in, strings.Join(conv, ", "), in)
	}
	fmt.Fprintf(b, "%s_ids = [0] * %d\n", in, len(streams))
	if len(streams) > 0 {
		fmt.Fprintf(b, "%stry: _sv = [%s]\n%sexcept rt.LibraryFailure as _e: return rt.library_failed(_e)\n", in, strings.Join(streams, ", "), in)
	} else {
		fmt.Fprintf(b, "%s_sv = []\n", in)
	}
	fmt.Fprintf(b, "%s_hs = [%s]\n", in, strings.Join(hs, ", "))
	b.WriteString(factory(f, as, in))
	fmt.Fprintf(b, "%sdef _output(_rv, _inst):\n", in)
	results := f.Sig.Results
	n := len(results)
	if n > 0 && sourceError(results[n-1]) {
		n--
		fmt.Fprintf(b, "%s    _err = _source_failure(_rv[%d])\n%s    if _err is not None: raise _err\n", in, n, in)
	}
	out := func(i int) string {
		if h := abi.Handle(results[i]); h != nil {
			return fmt.Sprintf("_wrap_%s(_inst, _rv[%d])", h.Name, i)
		}
		if k := ir.StreamKind(results[i]); k != "" {
			return fmt.Sprintf("rt.library_wrap(_inst, _rv[%d], \"io.%s\", lambda _h: rt.SourceStream(_h, _stream_%s))", i, k, k)
		}
		return fmt.Sprintf("%s(_rv[%d])", e.names.Type(results[i].U(), "output_"), i)
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
	fmt.Fprintf(b, "%sreturn rt.library_submit(_factory, _output, _library_state, [%s], options, _hs, None, _sv, _ids)\n\n", in, strings.Join(owned, ", "))
	return nil
}
