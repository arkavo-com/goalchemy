package java

import (
	"fmt"
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
			fmt.Fprintf(&b, " public static final class %s {\n", n)
			for _, f := range u.Fields {
				z := ""
				if f.Type.U().Kind == ir.KString {
					z = " = \"\""
				}
				fmt.Fprintf(&b, "  public %s %s%s;\n", typ(f.Type), f.Name, z)
			}
			b.WriteString(" }\n")
		case ir.KArray, ir.KSlice:
			convert(u.Elem)
		}
		fmt.Fprintf(&b, " private static %s %s(%s v,Library.CopyContext copy) {\n", typ(t), e.names.Type(u, "snapshot$"), typ(t))
		aggregate := u.Kind == ir.KStruct || u.Kind == ir.KArray || u.Kind == ir.KSlice
		if aggregate {
			b.WriteString("Object original=v;copy.enter(original);try {\n")
		}
		switch u.Kind {
		case ir.KBool, ir.KFloat:
			b.WriteString("return v;\n")
		case ir.KInt:
			fmt.Fprintf(&b, "return Library.integer(v,%d,%t);\n", u.Int.Bits(), u.Int.Signed())
		case ir.KString:
			b.WriteString("return Library.binaryString(v);\n")
		case ir.KStruct:
			fmt.Fprintf(&b, "if(v==null)v=new %s(); %s r=new %s();\n", typ(t), typ(t), typ(t))
			for _, f := range u.Fields {
				fmt.Fprintf(&b, "r.%s=%s(v.%s,copy);\n", f.Name, e.names.Type(f.Type.U(), "snapshot$"), f.Name)
			}
			b.WriteString("return r;\n")
		case ir.KArray, ir.KSlice:
			if u.Kind == ir.KArray {
				fmt.Fprintf(&b, "if(v==null||v.length!=%d)throw Library.invalid();\n", u.Len)
			}
			if byteElem(u.Elem) {
				b.WriteString("return Library.bytes(v);\n")
			} else {
				b.WriteString("if(v==null)return null;Library.length(v.length);\n")
				fmt.Fprintf(&b, "%s r=%s;for(int i=0;i<r.length;i++)r[i]=%s(v[i],copy);return r;\n", typ(t), publicArray(typ(u.Elem), "v.length"), e.names.Type(u.Elem.U(), "snapshot$"))
			}
		}
		if aggregate {
			b.WriteString("} finally {copy.leave(original); }\n")
		}
		b.WriteString(" }\n")
		fmt.Fprintf(&b, " private static %s %s(%s v) {\n", e.jt(t), e.names.Type(u, "input$"), typ(t))
		switch u.Kind {
		case ir.KBool, ir.KFloat:
			b.WriteString("return v;\n")
		case ir.KInt:
			fmt.Fprintf(&b, "return Library.integer(v,%d,%t);\n", u.Int.Bits(), u.Int.Signed())
		case ir.KString:
			b.WriteString("return Library.binaryString(v);\n")
		case ir.KStruct:
			fmt.Fprintf(&b, "if(v==null)v=new %s(); %s r=new %s();\n", typ(t), e.jt(t), e.jt(t))
			for i, f := range u.Fields {
				fmt.Fprintf(&b, "r.%s=%s(v.%s);\n", e.fieldProp(u, i), e.names.Type(f.Type.U(), "input$"), f.Name)
			}
			b.WriteString("return r;\n")
		case ir.KArray, ir.KSlice:
			if byteElem(u.Elem) {
				if u.Kind == ir.KArray {
					fmt.Fprintf(&b, "if(v==null||v.length!=%d)throw Library.invalid(); return Library.bytes(v);\n", u.Len)
				} else {
					b.WriteString("byte[] a=Library.bytes(v); return a==null?Slice.BYTE_NIL:new Slice(a,0,a.length,a.length);\n")
				}
			} else {
				if u.Kind == ir.KArray {
					fmt.Fprintf(&b, "if(v==null||v.length!=%d)throw Library.invalid();\n", u.Len)
				} else {
					b.WriteString("if(v==null)return Slice.NIL;\n")
				}
				b.WriteString("Library.length(v.length);Object[] a=new Object[v.length];\n")
				fmt.Fprintf(&b, "for(int i=0;i<a.length;i++)a[i]=%s(v[i]);\n", e.names.Type(u.Elem.U(), "input$"))
				if u.Kind == ir.KArray {
					b.WriteString("return a;\n")
				} else {
					b.WriteString("return new Slice(a,0,a.length,a.length);\n")
				}
			}
		}
		b.WriteString(" }\n")
		fmt.Fprintf(&b, " private static %s %s(%s v) {\n", typ(t), e.names.Type(u, "output$"), e.jt(t))
		switch u.Kind {
		case ir.KStruct:
			fmt.Fprintf(&b, "%s r=new %s();\n", typ(t), typ(t))
			for i, f := range u.Fields {
				fmt.Fprintf(&b, "r.%s=%s(v.%s);\n", f.Name, e.names.Type(f.Type.U(), "output$"), e.fieldProp(u, i))
			}
			b.WriteString("return r;\n")
		case ir.KArray, ir.KSlice:
			if byteElem(u.Elem) {
				if u.Kind == ir.KArray {
					b.WriteString("return v.clone();\n")
				} else {
					b.WriteString("return Native.bytesNullable(v);\n")
				}
			} else {
				length := "v.length"
				item := "v[i]"
				if u.Kind == ir.KSlice {
					b.WriteString("if(v.a==null)return null;\n")
					length = "v.l"
					item = "v.get(i)"
				}
				fmt.Fprintf(&b, "%s a=%s;\n", typ(t), publicArray(typ(u.Elem), length))
				fmt.Fprintf(&b, "for(int i=0;i<a.length;i++)a[i]=%s(%s);return a;\n", e.names.Type(u.Elem.U(), "output$"), e.cast(u.Elem, item))
			}
		default:
			b.WriteString("return v;\n")
		}
		b.WriteString(" }\n")
		fmt.Fprintf(&b, " private static Object %s(%s v) {\n", e.names.Type(u, "wire$"), typ(t))
		switch u.Kind {
		case ir.KStruct:
			b.WriteString("java.util.Map<String,Object> r=new java.util.LinkedHashMap<>();\n")
			for _, f := range u.Fields {
				fmt.Fprintf(&b, "r.put(%q,%s(v.%s));\n", f.Name, e.names.Type(f.Type.U(), "wire$"), f.Name)
			}
			b.WriteString("return r;\n")
		case ir.KArray, ir.KSlice:
			if byteElem(u.Elem) {
				b.WriteString("return v==null?null:v.clone();\n")
			} else {
				fmt.Fprintf(&b, "if(v==null)return null;Object[] a=new Object[v.length];for(int i=0;i<a.length;i++)a[i]=%s(v[i]);return a;\n", e.names.Type(u.Elem.U(), "wire$"))
			}
		default:
			b.WriteString("return v;\n")
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
			if (i == len(f.Sig.Results)-1 && sourceError(t)) || abi.Handle(t) != nil {
				continue
			}
			convert(t)
		}
	}
	for _, t := range e.p.Types.All {
		if t.Kind == ir.KPointer && t.Elem.U().Kind == ir.KStruct && libraryValue(t.Elem, map[*ir.Type]bool{}) && e.tds[t] != "" {
			convert(t.Elem)
		}
	}
	b.WriteString(" private static Library.Failure sourceFailure(Box err) {\nif(err==null)return null;\n")
	for _, t := range e.p.Types.All {
		if t.Kind == ir.KPointer && t.Elem.U().Kind == ir.KStruct && libraryValue(t.Elem, map[*ir.Type]bool{}) && e.tds[t] != "" {
			fmt.Fprintf(&b, "if(err.t==%s) { %s v=%s((%s)err.v); java.util.Map<String,Object> fields=new java.util.LinkedHashMap<>();\n", e.tds[t], typ(t.Elem), e.names.Type(t.Elem.U(), "output$"), e.jt(t.Elem))
			for _, f := range t.Elem.U().Fields {
				fmt.Fprintf(&b, "fields.put(%q,%s(v.%s));\n", f.Name, e.names.Type(f.Type.U(), "wire$"), f.Name)
			}
			b.WriteString("return new Library.Failure(\"source\",fields); }\n")
		}
	}
	b.WriteString("return new Library.Failure(\"source\");\n }\n private static void libraryReset() {\n")
	for _, g := range e.p.Globals {
		if g.AddrTaken && !g.Type.IsAggregate() {
			fmt.Fprintf(&b, "%s.v=%s;\n", e.symbol(g.Sym), e.zero(g.Type))
		} else {
			fmt.Fprintf(&b, "%s=%s;\n", e.symbol(g.Sym), e.zero(g.Type))
		}
	}
	b.WriteString("Program.clearFieldRefs();\n }\n private static void libraryZero() {\n")
	for _, g := range e.p.Globals {
		if g.AddrTaken && !g.Type.IsAggregate() {
			fmt.Fprintf(&b, "%s.v=%s;\n", e.symbol(g.Sym), e.zero(g.Type))
		} else {
			fmt.Fprintf(&b, "%s=%s;\n", e.symbol(g.Sym), e.zero(g.Type))
		}
	}
	b.WriteString(" }\n private static Object librarySave() {\nreturn new Object[]{")
	for _, g := range e.p.Globals {
		if g.AddrTaken && !g.Type.IsAggregate() {
			fmt.Fprintf(&b, "%s.v,", e.symbol(g.Sym))
		} else {
			fmt.Fprintf(&b, "%s,", e.symbol(g.Sym))
		}
	}
	b.WriteString("};\n }\n private static void libraryLoad(Object saved) {\nObject[] v=(Object[])saved;\n")
	for i, g := range e.p.Globals {
		if g.AddrTaken && !g.Type.IsAggregate() {
			fmt.Fprintf(&b, "%s.v=%s;\n", e.symbol(g.Sym), e.cast(g.Type, fmt.Sprintf("v[%d]", i)))
		} else {
			fmt.Fprintf(&b, "%s=%s;\n", e.symbol(g.Sym), e.cast(g.Type, fmt.Sprintf("v[%d]", i)))
		}
	}
	b.WriteString(" }\n private static final Library.State libraryState=new Library.State(Generated::libraryReset,Generated::libraryZero,Generated::librarySave,Generated::libraryLoad);\n")
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
		return "(ctx,fresh) -> fresh?Library.sequence(" + init + ",()->" + call + "):" + call
	}
	ptyp := func(t *ir.Type) string {
		if h := abi.Handle(t); h != nil {
			return h.Name
		}
		return typ(t)
	}
	for _, h := range abi.Handles {
		fmt.Fprintf(&b, " /** Handle to a source *%s owned by a library instance. Close it when\n  * done; a collected handle is released without running Close. */\n public static final class %s implements AutoCloseable {\n  final Library.Handle h;\n  %s(Library.Handle h){this.h=h;}\n", h.Name, h.Name, h.Name)
		run := "null"
		if h.Close != nil {
			run = frame(h.Close, []string{"Library.<" + e.jt(h.Ptr) + ">obj(h0)"})
		}
		fmt.Fprintf(&b, "  /** Runs the source Close, if any, and releases the handle. Queued calls\n   * fail and an active call is canceled first. */\n  public Library.Operation<Void> closeAsync(){final Library.Handle h0=h;return Library.close(h0,libraryState,%s,Generated::sourceFailure);}\n", run)
		b.WriteString("  /** Blocks until closeAsync completes, rethrowing its failure. */\n  @Override public void close(){try{closeAsync().completion().toCompletableFuture().join();}catch(java.util.concurrent.CompletionException e){if(e.getCause() instanceof Library.Failure f)throw f;throw e;}}\n")
		for _, f := range h.Methods {
			if err := e.libraryWrapper(&b, abi, ir.MethodName(f), h, f, ptyp, frame, used, reserved); err != nil {
				return "", err
			}
		}
		b.WriteString(" }\n")
		fmt.Fprintf(&b, " private static %s wrap%s(Library.Instance inst,Object v){return Library.wrap(inst,v,%q,%s::new);}\n", h.Name, h.Name, h.Name, h.Name)
		fmt.Fprintf(&b, " private static Library.Handle handle%s(Object v){return Library.handle(v,%s.class,x->((%s)x).h);}\n", h.Name, h.Name, h.Name)
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
		as = append(as, "Library.<"+e.jt(recv.Ptr)+">obj(hs[0])")
	}
	for i, t := range params {
		if sourceContext(t) {
			as = append(as, "ctx")
			continue
		}
		a := fmt.Sprintf("a%d", i)
		if k := ir.StreamKind(t); k != "" {
			ps = append(ps, "Object "+a)
			pre = append(pre, fmt.Sprintf("final Callback.Provider s%d=Library.stream(a%d,%q);\n", i, i, k))
			as = append(as, fmt.Sprintf("(ids[%d]==0?null:%s(ids[%d]))", len(streams), e.symbol(e.p.StreamAdapter(k).Sym), len(streams)))
			streams = append(streams, fmt.Sprintf("s%d", i))
			continue
		}
		ps = append(ps, typ(t)+" "+a)
		if h := abi.Handle(t); h != nil {
			pre = append(pre, fmt.Sprintf("final Library.Handle h%d=handle%s(a%d);\n", i, h.Name, i))
			hs = append(hs, fmt.Sprintf("h%d", i))
			as = append(as, fmt.Sprintf("Library.<%s>obj(hs[%d])", e.jt(t), len(hs)-1))
			continue
		}
		pre = append(pre, fmt.Sprintf("final %s owneda%d=%s(a%d,copy);\n", typ(t), i, e.names.Type(t.U(), "snapshot$"), i))
		as = append(as, fmt.Sprintf("%s(owned%s)", e.names.Type(t.U(), "input$"), a))
	}
	ps = append(ps, "Library.Options options")
	fmt.Fprintf(b, "%sLibrary.Operation<%s> %s(%s) {\ntry {\nLibrary.CopyContext copy=new Library.CopyContext();\n", static, result, name, strings.Join(ps, ","))
	for _, p := range pre {
		b.WriteString(p)
	}
	fmt.Fprintf(b, "final Library.Handle[] hs=new Library.Handle[]{%s};\nfinal long[] ids=new long[%d];\n", strings.Join(hs, ","), len(streams))
	fmt.Fprintf(b, "return Library.submit(options,libraryState,hs,%s,(inst,rv) -> {\n", frame(f, as))
	if hasErr {
		fmt.Fprintf(b, "Library.Failure err=sourceFailure((Box)rv[%d]);if(err!=null)throw err;\n", n)
	}
	out := func(i int) string {
		if h := abi.Handle(results[i]); h != nil {
			return fmt.Sprintf("wrap%s(inst,rv[%d])", h.Name, i)
		}
		return fmt.Sprintf("%s(%s)", e.names.Type(results[i].U(), "output$"), e.cast(results[i], fmt.Sprintf("rv[%d]", i)))
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
	fmt.Fprintf(b, "},null,new Callback.Provider[]{%s},ids);\n} catch(Library.Failure e) { return Library.failed(e); }\n%s}\n", strings.Join(streams, ","), indent)
	return nil
}
