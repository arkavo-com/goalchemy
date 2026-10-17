package java

import (
	"fmt"
	"github.com/eugenioenko/goalchemy/internal/emit/artifact"
	"github.com/eugenioenko/goalchemy/internal/ir"
	"go/token"
	"strings"
)

// LibraryBoundaryError identifies deliberately unsupported public value shapes.
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
func publicArray(element, length string) string {
	dims := ""
	for strings.HasSuffix(element, "[]") {
		element = strings.TrimSuffix(element, "[]")
		dims += "[]"
	}
	return "new " + element + "[" + length + "]" + dims
}

func (e *emitter) library() (string, error) {
	e.use("core.task.spawn")
	e.use("std.context.err")
	e.use("std.context.with_cancel")
	var b strings.Builder
	names := map[*ir.Type]string{}
	roots := map[string]bool{}
	used := map[string]bool{}
	reserved := map[string]bool{}
	for _, name := range strings.Fields("Generated Library Callback Log Native TaskSpawn Program Box Slice Ref Cell Fn TypeDesc Bounds Ints Floats Out Utf8 Desc GoMap GoPanic FatalPanic Panics Channel StdContextErr StdContextWithCancel EnvFn Object String Long Boolean Float Double Void Integer Math System Throwable RuntimeException") {
		reserved[name] = true
	}
	for _, symbol := range e.symbols {
		reserved[strings.Split(symbol, ".")[0]] = true
	}
	for _, f := range e.p.Exports {
		roots[f.Pkg] = true
	}
	abi, err := e.p.LibraryHandles(func(t *ir.Type) bool { return libraryValue(t, map[*ir.Type]bool{}) })
	if err != nil {
		return "", err
	}
	handleElems := map[*ir.Type]bool{}
	for _, h := range abi.Handles {
		if reserved[h.Name] {
			return "", fmt.Errorf("reserved Java public type name %s", h.Name)
		}
		if used[h.Name] {
			return "", fmt.Errorf("duplicate public name %s", h.Name)
		}
		used[h.Name] = true
		handleElems[h.Elem] = true
	}
	for _, t := range e.p.Types.All {
		if t.Kind == ir.KNamed && roots[t.Pkg] && token.IsExported(t.Obj) && t.U().Kind == ir.KStruct && !handleElems[t] && libraryValue(t, map[*ir.Type]bool{}) {
			if reserved[t.Obj] {
				return "", fmt.Errorf("reserved Java public type name %s", t.Obj)
			}
			if used[t.Obj] {
				return "", fmt.Errorf("duplicate public name %s", t.Obj)
			}
			used[t.Obj] = true
			names[t.U()] = t.Obj
		}
	}
	var typ func(*ir.Type) string
	typ = func(t *ir.Type) string {
		u := t.U()
		switch u.Kind {
		case ir.KBool:
			return "boolean"
		case ir.KFloat:
			if u.FloatBits == 32 {
				return "float"
			}
			return "double"
		case ir.KInt:
			return "long"
		case ir.KString:
			return "String"
		case ir.KStruct:
			if n := names[u]; n != "" {
				return n
			}
			n := fmt.Sprintf("Value%d", u.ID)
			for used[n] || reserved[n] {
				n += "_"
			}
			used[n] = true
			names[u] = n
			return n
		case ir.KArray, ir.KSlice:
			if byteElem(u.Elem) {
				return "byte[]"
			}
			return typ(u.Elem) + "[]"
		}
		return "Object"
	}
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
			n := typ(t)
			for _, f := range u.Fields {
				convert(f.Type)
			}
			start := b.Len()
			fmt.Fprintf(&b, " public static final class %s {\n", n)
			for _, f := range u.Fields {
				z := ""
				if f.Type.U().Kind == ir.KString {
					z = " = \"\""
				}
				fmt.Fprintf(&b, "  public %s %s%s;\n", typ(f.Type), f.Name, z)
			}
			b.WriteString(" }\n")
			e.api = append(e.api, artifact.APIDeclaration{Type: t, Name: n, Signature: b.String()[start:]})
		case ir.KArray, ir.KSlice:
			convert(u.Elem)
		}
		fmt.Fprintf(&b, " private static %s %s(%s _v,Library.CopyContext _copy) {\n", typ(t), e.names.Type(u, "snapshot$"), typ(t))
		aggregate := u.Kind == ir.KStruct || u.Kind == ir.KArray || u.Kind == ir.KSlice
		if aggregate {
			b.WriteString("Object _original=_v;_copy.enter(_original);try {\n")
		}
		switch u.Kind {
		case ir.KBool, ir.KFloat:
			b.WriteString("return _v;\n")
		case ir.KInt:
			fmt.Fprintf(&b, "return Library.integer(_v,%d,%t);\n", u.Int.Bits(), u.Int.Signed())
		case ir.KString:
			b.WriteString("return Library.binaryString(_v);\n")
		case ir.KStruct:
			fmt.Fprintf(&b, "if(_v==null)_v=new %s(); %s _r=new %s();\n", typ(t), typ(t), typ(t))
			for _, f := range u.Fields {
				fmt.Fprintf(&b, "_r.%s=%s(_v.%s,_copy);\n", f.Name, e.names.Type(f.Type.U(), "snapshot$"), f.Name)
			}
			b.WriteString("return _r;\n")
		case ir.KArray, ir.KSlice:
			if u.Kind == ir.KArray {
				fmt.Fprintf(&b, "if(_v==null||_v.length!=%d)throw Library.invalid();\n", u.Len)
			}
			if byteElem(u.Elem) {
				b.WriteString("return Library.bytes(_v);\n")
			} else {
				b.WriteString("if(_v==null)return null;Library.length(_v.length);\n")
				fmt.Fprintf(&b, "%s _r=%s;for(int _i=0;_i<_r.length;_i++)_r[_i]=%s(_v[_i],_copy);return _r;\n", typ(t), publicArray(typ(u.Elem), "_v.length"), e.names.Type(u.Elem.U(), "snapshot$"))
			}
		}
		if aggregate {
			b.WriteString("} finally {_copy.leave(_original); }\n")
		}
		b.WriteString(" }\n")
		fmt.Fprintf(&b, " private static %s %s(%s _v) {\n", e.jt(t), e.names.Type(u, "input$"), typ(t))
		switch u.Kind {
		case ir.KBool, ir.KFloat:
			b.WriteString("return _v;\n")
		case ir.KInt:
			fmt.Fprintf(&b, "return Library.integer(_v,%d,%t);\n", u.Int.Bits(), u.Int.Signed())
		case ir.KString:
			b.WriteString("return Library.binaryString(_v);\n")
		case ir.KStruct:
			fmt.Fprintf(&b, "if(_v==null)_v=new %s(); %s _r=new %s();\n", typ(t), e.jt(t), e.jt(t))
			for i, f := range u.Fields {
				fmt.Fprintf(&b, "_r.%s=%s(_v.%s);\n", e.fieldProp(u, i), e.names.Type(f.Type.U(), "input$"), f.Name)
			}
			b.WriteString("return _r;\n")
		case ir.KArray, ir.KSlice:
			if byteElem(u.Elem) {
				if u.Kind == ir.KArray {
					fmt.Fprintf(&b, "if(_v==null||_v.length!=%d)throw Library.invalid(); return Library.bytes(_v);\n", u.Len)
				} else {
					b.WriteString("byte[] _a=Library.bytes(_v); return _a==null?Slice.BYTE_NIL:new Slice(_a,0,_a.length,_a.length);\n")
				}
			} else {
				if u.Kind == ir.KArray {
					fmt.Fprintf(&b, "if(_v==null||_v.length!=%d)throw Library.invalid();\n", u.Len)
				} else {
					b.WriteString("if(_v==null)return Slice.NIL;\n")
				}
				b.WriteString("Library.length(_v.length);Object[] _a=new Object[_v.length];\n")
				fmt.Fprintf(&b, "for(int _i=0;_i<_a.length;_i++)_a[_i]=%s(_v[_i]);\n", e.names.Type(u.Elem.U(), "input$"))
				if u.Kind == ir.KArray {
					b.WriteString("return _a;\n")
				} else {
					b.WriteString("return new Slice(_a,0,_a.length,_a.length);\n")
				}
			}
		}
		b.WriteString(" }\n")
		fmt.Fprintf(&b, " private static %s %s(%s _v) {\n", typ(t), e.names.Type(u, "output$"), e.jt(t))
		switch u.Kind {
		case ir.KStruct:
			fmt.Fprintf(&b, "%s _r=new %s();\n", typ(t), typ(t))
			for i, f := range u.Fields {
				fmt.Fprintf(&b, "_r.%s=%s(_v.%s);\n", f.Name, e.names.Type(f.Type.U(), "output$"), e.fieldProp(u, i))
			}
			b.WriteString("return _r;\n")
		case ir.KArray, ir.KSlice:
			if byteElem(u.Elem) {
				if u.Kind == ir.KArray {
					b.WriteString("return _v.clone();\n")
				} else {
					b.WriteString("return Native.bytesNullable(_v);\n")
				}
			} else {
				length := "_v.length"
				item := "_v[_i]"
				if u.Kind == ir.KSlice {
					b.WriteString("if(_v.a==null)return null;\n")
					length = "_v.l"
					item = "_v.get(_i)"
				}
				fmt.Fprintf(&b, "%s _a=%s;\n", typ(t), publicArray(typ(u.Elem), length))
				fmt.Fprintf(&b, "for(int _i=0;_i<_a.length;_i++)_a[_i]=%s(%s);return _a;\n", e.names.Type(u.Elem.U(), "output$"), e.cast(u.Elem, item))
			}
		default:
			b.WriteString("return _v;\n")
		}
		b.WriteString(" }\n")
		fmt.Fprintf(&b, " private static Object %s(%s _v) {\n", e.names.Type(u, "wire$"), typ(t))
		switch u.Kind {
		case ir.KStruct:
			b.WriteString("java.util.Map<String,Object> _r=new java.util.LinkedHashMap<>();\n")
			for _, f := range u.Fields {
				fmt.Fprintf(&b, "_r.put(%q,%s(_v.%s));\n", f.Name, e.names.Type(f.Type.U(), "wire$"), f.Name)
			}
			b.WriteString("return _r;\n")
		case ir.KArray, ir.KSlice:
			if byteElem(u.Elem) {
				b.WriteString("return _v==null?null:_v.clone();\n")
			} else {
				fmt.Fprintf(&b, "if(_v==null)return null;Object[] _a=new Object[_v.length];for(int _i=0;_i<_a.length;_i++)_a[_i]=%s(_v[_i]);return _a;\n", e.names.Type(u.Elem.U(), "wire$"))
			}
		default:
			b.WriteString("return _v;\n")
		}
		b.WriteString(" }\n")
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
		for i, t := range f.Sig.Results {
			if (i == len(f.Sig.Results)-1 && sourceError(t)) || abi.Handle(t) != nil || ir.StreamKind(t) != "" {
				continue
			}
			convert(t)
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
	b.WriteString(" private static Library.Failure _sourceFailure(Box _err) {\nif(_err==null)return null;\n")
	for _, t := range e.p.Types.All {
		if t.Kind == ir.KPointer && t.Elem.U().Kind == ir.KStruct && libraryValue(t.Elem, map[*ir.Type]bool{}) && e.tds[t] != "" {
			fmt.Fprintf(&b, "if(_err.t==%s) { %s _v=%s((%s)_err.v); java.util.Map<String,Object> _fields=new java.util.LinkedHashMap<>();\n", e.tds[t], typ(t.Elem), e.names.Type(t.Elem.U(), "output$"), e.jt(t.Elem))
			for _, f := range t.Elem.U().Fields {
				fmt.Fprintf(&b, "_fields.put(%q,%s(_v.%s));\n", f.Name, e.names.Type(f.Type.U(), "wire$"), f.Name)
			}
			b.WriteString("return new Library.Failure(\"source\",_fields); }\n")
		}
	}
	b.WriteString("return new Library.Failure(\"source\");\n }\n private static void _libraryReset() {\n")
	for _, g := range e.p.Globals {
		if g.AddrTaken && !g.Type.IsAggregate() {
			fmt.Fprintf(&b, "%s.v=%s;\n", e.symbol(g.Sym), e.zero(g.Type))
		} else {
			fmt.Fprintf(&b, "%s=%s;\n", e.symbol(g.Sym), e.zero(g.Type))
		}
	}
	b.WriteString("Program.clearFieldRefs();\n }\n private static void _libraryZero() {\n")
	for _, g := range e.p.Globals {
		if g.AddrTaken && !g.Type.IsAggregate() {
			fmt.Fprintf(&b, "%s.v=%s;\n", e.symbol(g.Sym), e.zero(g.Type))
		} else {
			fmt.Fprintf(&b, "%s=%s;\n", e.symbol(g.Sym), e.zero(g.Type))
		}
	}
	b.WriteString(" }\n private static Object _librarySave() {\nreturn new Object[]{")
	for _, g := range e.p.Globals {
		if g.AddrTaken && !g.Type.IsAggregate() {
			fmt.Fprintf(&b, "%s.v,", e.symbol(g.Sym))
		} else {
			fmt.Fprintf(&b, "%s,", e.symbol(g.Sym))
		}
	}
	b.WriteString("};\n }\n private static void _libraryLoad(Object _saved) {\nObject[] _v=(Object[])_saved;\n")
	for i, g := range e.p.Globals {
		if g.AddrTaken && !g.Type.IsAggregate() {
			fmt.Fprintf(&b, "%s.v=%s;\n", e.symbol(g.Sym), e.cast(g.Type, fmt.Sprintf("_v[%d]", i)))
		} else {
			fmt.Fprintf(&b, "%s=%s;\n", e.symbol(g.Sym), e.cast(g.Type, fmt.Sprintf("_v[%d]", i)))
		}
	}
	b.WriteString(" }\n private static final Library.State _libraryState=new Library.State(Generated::_libraryReset,Generated::_libraryZero,Generated::_librarySave,Generated::_libraryLoad);\n")
	init := e.symbol(e.p.Init.Sym) + "()"
	if !e.p.Init.MaySuspend {
		init = "TaskSpawn.sync(() -> { " + e.symbol(e.p.Init.Sym) + "();return new Object[0];})"
	}
	frame := func(f *ir.Func, as []string) string {
		call := e.symbol(f.Sym) + "(" + strings.Join(as, ",") + ")"
		if !f.MaySuspend {
			body := call + ";return new Object[0]"
			if len(f.Sig.Results) == 1 {
				body = "return new Object[] {" + call + "}"
			} else if len(f.Sig.Results) > 1 {
				body = "return " + call
			}
			call = "TaskSpawn.sync(() -> { " + body + "; })"
		}
		return "(_ctx,_fresh) -> _fresh?Library.sequence(" + init + ",()->" + call + "):" + call
	}
	ptyp := func(t *ir.Type) string {
		if h := abi.Handle(t); h != nil {
			return h.Name
		}
		if ir.StreamKind(t) != "" {
			return "Library.SourceStream"
		}
		return typ(t)
	}
	streamOps := func(name string, ops ir.StreamOps, obj func(*ir.Type) string, closeRun string) {
		op := func(fn string, params []string, out string) string {
			f := e.p.HostIOFunc(fn)
			as := []string{obj(f.Sig.Params[0])}
			for i, p := range params {
				as = append(as, fmt.Sprintf("%s(%s)", e.names.Type(f.Sig.Params[i+1].U(), "input$"), p))
			}
			last := len(f.Sig.Results) - 1
			return fmt.Sprintf("(%s)->{final Library.Handle[] _hs=new Library.Handle[]{_h};return Library.submit(null,_libraryState,_hs,%s,(_inst,_rv)->{Library.Failure _err=_sourceFailure((Box)_rv[%d]);if(_err!=null)throw _err;return %s;},null);}",
				strings.Join(append([]string{"_h"}, params...), ","), frame(f, as), last, out)
		}
		res := func(fn string, i int) string {
			t := e.p.HostIOFunc(fn).Sig.Results[i]
			return fmt.Sprintf("%s(%s)", e.names.Type(t.U(), "output$"), e.cast(t, fmt.Sprintf("_rv[%d]", i)))
		}
		read, write, seek := "null", "null", "null"
		if ops.Read {
			read = op("ReadView", []string{"_n"}, "new Object[]{"+res("ReadView", 0)+","+res("ReadView", 1)+"}")
		}
		if ops.Write {
			write = op("WriteView", []string{"_d"}, res("WriteView", 0))
		}
		if ops.Seek {
			seek = op("SeekView", []string{"_off", "_wh"}, res("SeekView", 0))
		}
		fmt.Fprintf(&b, " private static final Library.StreamOps %s=new Library.StreamOps(%s,%s,%s,_h->{final Library.Handle _h0=_h;return Library.close(_h0,_libraryState,%s,Generated::_sourceFailure);});\n", name, read, write, seek, closeRun)
	}
	kinds := map[string]bool{}
	for _, f := range funcs {
		for _, t := range f.Sig.Results {
			if k := ir.StreamKind(t); k != "" && !kinds[k] {
				kinds[k] = true
				run := "null"
				if ir.StreamCloses(k) {
					cv := e.p.HostIOFunc("CloseView")
					run = frame(cv, []string{"Library.<" + e.jt(cv.Sig.Params[0]) + ">obj(_h0)"})
				}
				streamOps("_streamOps"+k, ir.KindOps(k), func(t *ir.Type) string { return "Library.<" + e.jt(t) + ">obj(_hs[0])" }, run)
			}
		}
	}
	for _, h := range abi.Handles {
		if h.Stream.Any() {
			run := "null"
			if h.Close != nil {
				run = frame(h.Close, []string{e.handleRecv(h, h.Close, "Library.<"+e.jt(h.Ptr)+">obj(_h0)")})
			}
			hp := h.Ptr
			streamOps("_streamOps"+h.Name, h.Stream, func(*ir.Type) string {
				return "Box.box(" + e.td(hp) + ",Library.<" + e.jt(hp) + ">obj(_hs[0]))"
			}, run)
		}
	}
	for _, h := range abi.Handles {
		e.api = append(e.api, artifact.APIDeclaration{Type: h.Elem, Name: h.Name, Signature: "public static final class " + h.Name})
		if h.Close != nil {
			e.api = append(e.api, artifact.APIDeclaration{Function: h.Close, Name: "closeAsync", Signature: "public Library.Operation<Void> closeAsync()"})
		}
		fmt.Fprintf(&b, " /** Handle to a source *%s owned by a library instance. Close it when\n  * done; a collected handle is released without running Close. */\n public static final class %s implements AutoCloseable {\n  final Library.Handle h;\n  %s(Library.Handle h){this.h=h;}\n", h.Name, h.Name, h.Name)
		run := "null"
		if h.Close != nil {
			run = frame(h.Close, []string{e.handleRecv(h, h.Close, "Library.<"+e.jt(h.Ptr)+">obj(_h0)")})
		}
		fmt.Fprintf(&b, "  /** Runs the source Close, if any, and releases the handle. Queued calls\n   * fail and an active call is canceled first. */\n  public Library.Operation<Void> closeAsync(){final Library.Handle _h0=h;return Library.close(_h0,_libraryState,%s,Generated::_sourceFailure);}\n", run)
		b.WriteString("  /** Blocks until closeAsync completes, rethrowing its failure. */\n  @Override public void close(){try{closeAsync().completion().toCompletableFuture().join();}catch(java.util.concurrent.CompletionException _e){if(_e.getCause() instanceof Library.Failure _f)throw _f;throw _e;}}\n")
		if h.Stream.Any() {
			fmt.Fprintf(&b, "  /** The handle as a stream; each operation is a call on the handle and\n   * closing the stream closes the handle. */\n  public Library.SourceStream io(){return new Library.SourceStream(h,_streamOps%s);}\n", h.Name)
			if h.Stream.Read {
				b.WriteString("  /** An InputStream over the handle's Read; closing it closes the handle. */\n  public java.io.InputStream asInputStream(){return io().asInputStream();}\n")
			}
			if h.Stream.Write {
				b.WriteString("  /** An OutputStream into the handle's Write; closing it closes the handle. */\n  public java.io.OutputStream asOutputStream(){return io().asOutputStream();}\n")
			}
		}
		for _, f := range h.Methods {
			if err := e.libraryWrapper(&b, abi, ir.MethodName(f), h, f, ptyp, frame, used, reserved); err != nil {
				return "", err
			}
		}
		b.WriteString(" }\n")
		fmt.Fprintf(&b, " private static %s _wrap%s(Library.Instance _inst,Object _v){return Library.wrap(_inst,_v,%q,%s::new);}\n", h.Name, h.Name, h.Name, h.Name)
		fmt.Fprintf(&b, " private static Library.Handle _handle%s(Object _v){return Library.handle(_v,%s.class,_x->((%s)_x).h);}\n", h.Name, h.Name, h.Name)
	}
	for _, f := range e.p.Exports {
		name := f.Name[strings.LastIndex(f.Name, ".")+1:]
		if used[name] {
			return "", fmt.Errorf("duplicate export %s", name)
		}
		used[name] = true
		if err := e.libraryWrapper(&b, abi, name, nil, f, ptyp, frame, used, reserved); err != nil {
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

func (e *emitter) libraryWrapper(b *strings.Builder, abi *ir.LibraryABI, name string, recv *ir.Handle, f *ir.Func, typ func(*ir.Type) string, frame func(*ir.Func, []string) string, used, reserved map[string]bool) error {
	results := f.Sig.Results
	n := len(results)
	hasErr := n > 0 && sourceError(results[n-1])
	if hasErr {
		n--
	}
	static, indent := " public static ", " "
	if recv != nil {
		static, indent = "  public ", "  "
	}
	result := "Void"
	if n == 1 {
		result = typ(results[0])
		switch result {
		case "long":
			result = "Long"
		case "boolean":
			result = "Boolean"
		case "float":
			result = "Float"
		case "double":
			result = "Double"
		}
	} else if n > 1 {
		result = name + "Result"
		if recv != nil {
			result = recv.Name + name + "Result"
		}
		if used[result] || reserved[result] {
			return fmt.Errorf("duplicate Java result type %s", result)
		}
		used[result] = true
		fmt.Fprintf(b, "%spublic record %s(", indent, result)
		for i := 0; i < n; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(b, "%s value%d", typ(results[i]), i)
		}
		b.WriteString(") {}\n")
	}
	params := f.Sig.Params
	var ps, as, hs, pre, streams []string
	if recv != nil {
		params = params[1:]
		hs = append(hs, "h")
		as = append(as, e.handleRecv(recv, f, "Library.<"+e.jt(recv.Ptr)+">obj(_hs[0])"))
	}
	for i, t := range params {
		if sourceContext(t) {
			as = append(as, "_ctx")
			continue
		}
		a := fmt.Sprintf("a%d", i)
		if k := ir.StreamKind(t); k != "" {
			ps = append(ps, "Object "+a)
			pre = append(pre, fmt.Sprintf("final Callback.Provider _s%d=Library.stream(a%d,%q);\n", i, i, k))
			as = append(as, fmt.Sprintf("(_ids[%d]==0?null:%s(_ids[%d]))", len(streams), e.symbol(e.p.StreamAdapter(k).Sym), len(streams)))
			streams = append(streams, fmt.Sprintf("_s%d", i))
			continue
		}
		ps = append(ps, typ(t)+" "+a)
		if h := abi.Handle(t); h != nil {
			pre = append(pre, fmt.Sprintf("final Library.Handle _h%d=_handle%s(a%d);\n", i, h.Name, i))
			hs = append(hs, fmt.Sprintf("_h%d", i))
			as = append(as, fmt.Sprintf("Library.<%s>obj(_hs[%d])", e.jt(t), len(hs)-1))
			continue
		}
		pre = append(pre, fmt.Sprintf("final %s _owneda%d=%s(a%d,_copy);\n", typ(t), i, e.names.Type(t.U(), "snapshot$"), i))
		as = append(as, fmt.Sprintf("%s(_owned%s)", e.names.Type(t.U(), "input$"), a))
	}
	ps = append(ps, "Library.Options options")
	e.api = append(e.api, artifact.APIDeclaration{Function: f, Name: name, Signature: fmt.Sprintf("%sLibrary.Operation<%s> %s(%s)", static, result, name, strings.Join(ps, ","))})
	fmt.Fprintf(b, "%sLibrary.Operation<%s> %s(%s) {\ntry {\nLibrary.CopyContext _copy=new Library.CopyContext();\n", static, result, name, strings.Join(ps, ","))
	for _, p := range pre {
		b.WriteString(p)
	}
	fmt.Fprintf(b, "final Library.Handle[] _hs=new Library.Handle[]{%s};\nfinal long[] _ids=new long[%d];\n", strings.Join(hs, ","), len(streams))
	fmt.Fprintf(b, "return Library.submit(options,_libraryState,_hs,%s,(_inst,_rv) -> {\n", frame(f, as))
	if hasErr {
		fmt.Fprintf(b, "Library.Failure _err=_sourceFailure((Box)_rv[%d]);if(_err!=null)throw _err;\n", n)
	}
	out := func(i int) string {
		if h := abi.Handle(results[i]); h != nil {
			return fmt.Sprintf("_wrap%s(_inst,_rv[%d])", h.Name, i)
		}
		if k := ir.StreamKind(results[i]); k != "" {
			return fmt.Sprintf("Library.wrap(_inst,_rv[%d],\"io.%s\",_h->new Library.SourceStream(_h,_streamOps%s))", i, k, k)
		}
		return fmt.Sprintf("%s(%s)", e.names.Type(results[i].U(), "output$"), e.cast(results[i], fmt.Sprintf("_rv[%d]", i)))
	}
	if n == 1 {
		fmt.Fprintf(b, "return %s;\n", out(0))
	} else if n > 1 {
		fmt.Fprintf(b, "return new %s(", result)
		for i := 0; i < n; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(out(i))
		}
		b.WriteString(");\n")
	} else {
		b.WriteString("return null;\n")
	}
	fmt.Fprintf(b, "},null,new Callback.Provider[]{%s},_ids);\n} catch(Library.Failure _e) { return Library.failed(_e); }\n%s}\n", strings.Join(streams, ","), indent)
	return nil
}
