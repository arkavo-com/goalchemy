package csharp

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
	for _, name := range strings.Fields("GoProgram Generated Library Callback Log Native TaskSpawn Program Box Slice Ref Cell Fn TypeDesc Bounds Ints Floats Out Utf8 Desc GoMap GoPanic FatalPanic Panics Channel StdContextErr StdContextWithCancel EnvFn object string Long Boolean Void Integer Math System Throwable RuntimeException") {
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
			return "", fmt.Errorf("reserved C# public type name %s", h.Name)
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
				return "", fmt.Errorf("reserved C# public type name %s", t.Obj)
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
			return "bool"
		case ir.KFloat:
			if u.FloatBits == 32 {
				return "float"
			}
			return "double"
		case ir.KInt:
			return "long"
		case ir.KString:
			return "string"
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
		return "object"
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
			fmt.Fprintf(&b, " public sealed class %s {\n", n)
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
		fmt.Fprintf(&b, " private static %s %s(%s v,Library.CopyContext copy) {\n", typ(t), e.names.Type(u, "snapshot_"), typ(t))
		aggregate := u.Kind == ir.KStruct || u.Kind == ir.KArray || u.Kind == ir.KSlice
		if aggregate {
			b.WriteString("object original=v;copy.enter(original);try {\n")
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
				fmt.Fprintf(&b, "r.%s=%s(v.%s,copy);\n", f.Name, e.names.Type(f.Type.U(), "snapshot_"), f.Name)
			}
			b.WriteString("return r;\n")
		case ir.KArray, ir.KSlice:
			if u.Kind == ir.KArray {
				fmt.Fprintf(&b, "if(v==null||v.Length!=%d)throw Library.invalid();\n", u.Len)
			}
			if byteElem(u.Elem) {
				b.WriteString("return Library.bytes(v);\n")
			} else {
				b.WriteString("if(v==null)return null;Library.length(v.Length);\n")
				fmt.Fprintf(&b, "%s r=%s;for(int i=0;i<r.Length;i++)r[i]=%s(v[i],copy);return r;\n", typ(t), publicArray(typ(u.Elem), "v.Length"), e.names.Type(u.Elem.U(), "snapshot_"))
			}
		}
		if aggregate {
			b.WriteString("} finally {copy.leave(original); }\n")
		}
		b.WriteString(" }\n")
		fmt.Fprintf(&b, " private static %s %s(%s v) {\n", e.jt(t), e.names.Type(u, "input_"), typ(t))
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
				fmt.Fprintf(&b, "r.%s=%s(v.%s);\n", e.fieldProp(u, i), e.names.Type(f.Type.U(), "input_"), f.Name)
			}
			b.WriteString("return r;\n")
		case ir.KArray, ir.KSlice:
			if byteElem(u.Elem) {
				if u.Kind == ir.KArray {
					fmt.Fprintf(&b, "if(v==null||v.Length!=%d)throw Library.invalid(); return Library.bytes(v);\n", u.Len)
				} else {
					b.WriteString("byte[] a=Library.bytes(v); return a==null?Slice.BYTE_NIL:new Slice(a,0,a.Length,a.Length);\n")
				}
			} else {
				if u.Kind == ir.KArray {
					fmt.Fprintf(&b, "if(v==null||v.Length!=%d)throw Library.invalid();\n", u.Len)
				} else {
					b.WriteString("if(v==null)return Slice.NIL;\n")
				}
				b.WriteString("Library.length(v.Length);object[] a=new object[v.Length];\n")
				fmt.Fprintf(&b, "for(int i=0;i<a.Length;i++)a[i]=%s(v[i]);\n", e.names.Type(u.Elem.U(), "input_"))
				if u.Kind == ir.KArray {
					b.WriteString("return a;\n")
				} else {
					b.WriteString("return new Slice(a,0,a.Length,a.Length);\n")
				}
			}
		}
		b.WriteString(" }\n")
		fmt.Fprintf(&b, " private static %s %s(%s v) {\n", typ(t), e.names.Type(u, "output_"), e.jt(t))
		switch u.Kind {
		case ir.KStruct:
			fmt.Fprintf(&b, "%s r=new %s();\n", typ(t), typ(t))
			for i, f := range u.Fields {
				fmt.Fprintf(&b, "r.%s=%s(v.%s);\n", f.Name, e.names.Type(f.Type.U(), "output_"), e.fieldProp(u, i))
			}
			b.WriteString("return r;\n")
		case ir.KArray, ir.KSlice:
			if byteElem(u.Elem) {
				if u.Kind == ir.KArray {
					b.WriteString("return (byte[])v.Clone();\n")
				} else {
					b.WriteString("return Native.bytesNullable(v);\n")
				}
			} else {
				length := "v.Length"
				item := "v[i]"
				if u.Kind == ir.KSlice {
					b.WriteString("if(v.a==null)return null;\n")
					length = "v.l"
					item = "v.Get(i)"
				}
				fmt.Fprintf(&b, "%s a=%s;\n", typ(t), publicArray(typ(u.Elem), length))
				fmt.Fprintf(&b, "for(int i=0;i<a.Length;i++)a[i]=%s(%s);return a;\n", e.names.Type(u.Elem.U(), "output_"), e.cast(u.Elem, item))
			}
		default:
			b.WriteString("return v;\n")
		}
		b.WriteString(" }\n")
		fmt.Fprintf(&b, " private static object %s(%s v) {\n", e.names.Type(u, "wire_"), typ(t))
		switch u.Kind {
		case ir.KStruct:
			b.WriteString("var r=new System.Collections.Generic.Dictionary<string,object>();\n")
			for _, f := range u.Fields {
				fmt.Fprintf(&b, "r.Add(%q,%s(v.%s));\n", f.Name, e.names.Type(f.Type.U(), "wire_"), f.Name)
			}
			b.WriteString("return r;\n")
		case ir.KArray, ir.KSlice:
			if byteElem(u.Elem) {
				b.WriteString("return v==null?null:(byte[])v.Clone();\n")
			} else {
				fmt.Fprintf(&b, "if(v==null)return null;object[] a=new object[v.Length];for(int i=0;i<a.Length;i++)a[i]=%s(v[i]);return a;\n", e.names.Type(u.Elem.U(), "wire_"))
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
			if !sourceContext(t) && abi.Handle(t) == nil {
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
			fmt.Fprintf(&b, "if(err.t==%s) { %s v=%s((%s)err.v); var fields=new System.Collections.Generic.Dictionary<string,object>();\n", e.tds[t], typ(t.Elem), e.names.Type(t.Elem.U(), "output_"), e.jt(t.Elem))
			for _, f := range t.Elem.U().Fields {
				fmt.Fprintf(&b, "fields.Add(%q,%s(v.%s));\n", f.Name, e.names.Type(f.Type.U(), "wire_"), f.Name)
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
	b.WriteString("Rt.Program.clearFieldRefs();\n }\n private static void libraryZero() {\n")
	for _, g := range e.p.Globals {
		if g.AddrTaken && !g.Type.IsAggregate() {
			fmt.Fprintf(&b, "%s.v=%s;\n", e.symbol(g.Sym), e.zero(g.Type))
		} else {
			fmt.Fprintf(&b, "%s=%s;\n", e.symbol(g.Sym), e.zero(g.Type))
		}
	}
	b.WriteString(" }\n private static object librarySave() {\nreturn new object[]{")
	for _, g := range e.p.Globals {
		if g.AddrTaken && !g.Type.IsAggregate() {
			fmt.Fprintf(&b, "%s.v,", e.symbol(g.Sym))
		} else {
			fmt.Fprintf(&b, "%s,", e.symbol(g.Sym))
		}
	}
	b.WriteString("};\n }\n private static void libraryLoad(object saved) {\nvar v=(object[])saved;\n")
	for i, g := range e.p.Globals {
		if g.AddrTaken && !g.Type.IsAggregate() {
			fmt.Fprintf(&b, "%s.v=%s;\n", e.symbol(g.Sym), e.cast(g.Type, fmt.Sprintf("v[%d]", i)))
		} else {
			fmt.Fprintf(&b, "%s=%s;\n", e.symbol(g.Sym), e.cast(g.Type, fmt.Sprintf("v[%d]", i)))
		}
	}
	b.WriteString(" }\n private static readonly Library.State libraryState=new Library.State(libraryReset,libraryZero,librarySave,libraryLoad);\n")
	init := e.symbol(e.p.Init.Sym) + "()"
	if !e.p.Init.MaySuspend {
		init = "R.sync(() => { " + e.symbol(e.p.Init.Sym) + "();return new object[0];})"
	}
	frame := func(f *ir.Func, as []string) string {
		call := e.symbol(f.Sym) + "(" + strings.Join(as, ",") + ")"
		if !f.MaySuspend {
			body := call + ";return new object[0]"
			if len(f.Sig.Results) == 1 {
				body = "return new object[] {" + call + "}"
			} else if len(f.Sig.Results) > 1 {
				body = "return " + call
			}
			call = "R.sync(() => { " + body + "; })"
		}
		return "(ctx,fresh) => fresh?Library.sequence(" + init + ",() =>" + call + "):" + call
	}
	ptyp := func(t *ir.Type) string {
		if h := abi.Handle(t); h != nil {
			return h.Name
		}
		return typ(t)
	}
	for _, h := range abi.Handles {
		fmt.Fprintf(&b, " /// <summary>Handle to a source *%s owned by a library instance. Dispose it when done; a\n /// collected handle is released without running Close.</summary>\n public sealed class %s : System.IDisposable, System.IAsyncDisposable {\n  internal readonly Library.Handle h;\n  internal %s(Library.Handle h){this.h=h;}\n", h.Name, h.Name, h.Name)
		run := "null"
		if h.Close != nil {
			run = frame(h.Close, []string{"Library.obj<" + e.jt(h.Ptr) + ">(h0)"})
		}
		fmt.Fprintf(&b, "  /// <summary>Runs the source Close, if any, and releases the handle. Queued calls fail and an\n  /// active call is canceled first.</summary>\n  public Library.Operation<object> CloseAsync(){var h0=h;return Library.close(h0,libraryState,%s,sourceFailure);}\n", run)
		b.WriteString("  public void Dispose(){try{CloseAsync().Completion.GetAwaiter().GetResult();}catch(System.AggregateException e) when (e.InnerException is Library.Failure f){throw f;}}\n  public async System.Threading.Tasks.ValueTask DisposeAsync(){await CloseAsync().Completion;}\n")
		for _, f := range h.Methods {
			if err := e.libraryWrapper(&b, abi, ir.MethodName(f), h, f, ptyp, frame, used, reserved); err != nil {
				return "", err
			}
		}
		b.WriteString(" }\n")
		fmt.Fprintf(&b, " private static %s wrap%s(Library.Instance inst,object v)=>Library.wrap(inst,v,%q,h=>new %s(h));\n", h.Name, h.Name, h.Name, h.Name)
		fmt.Fprintf(&b, " private static Library.Handle handle%s(object v)=>Library.handle(v,typeof(%s),x=>((%s)x).h);\n", h.Name, h.Name, h.Name)
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
	result := "object"
	if n == 1 {
		result = typ(results[0])
	} else if n > 1 {
		result = name + "Result"
		if recv != nil {
			result = recv.Name + name + "Result"
		}
		if used[result] || reserved[result] {
			return fmt.Errorf("duplicate C# result type %s", result)
		}
		used[result] = true
		fmt.Fprintf(b, "%spublic sealed record %s(", indent, result)
		for i := 0; i < n; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(b, "%s value%d", typ(results[i]), i)
		}
		b.WriteString(");\n")
	}
	params := f.Sig.Params
	var ps, as, hs, pre []string
	if recv != nil {
		params = params[1:]
		hs = append(hs, "h")
		as = append(as, "Library.obj<"+e.jt(recv.Ptr)+">(hs[0])")
	}
	for i, t := range params {
		if sourceContext(t) {
			as = append(as, "ctx")
			continue
		}
		a := fmt.Sprintf("a%d", i)
		ps = append(ps, typ(t)+" "+a)
		if h := abi.Handle(t); h != nil {
			pre = append(pre, fmt.Sprintf("var h%d=handle%s(a%d);\n", i, h.Name, i))
			hs = append(hs, fmt.Sprintf("h%d", i))
			as = append(as, fmt.Sprintf("Library.obj<%s>(hs[%d])", e.jt(t), len(hs)-1))
			continue
		}
		pre = append(pre, fmt.Sprintf("%s owneda%d=%s(a%d,copy);\n", typ(t), i, e.names.Type(t.U(), "snapshot_"), i))
		as = append(as, fmt.Sprintf("%s(owned%s)", e.names.Type(t.U(), "input_"), a))
	}
	ps = append(ps, "Library.Options options")
	fmt.Fprintf(b, "%sLibrary.Operation<%s> %s(%s) {\ntry {\nLibrary.CopyContext copy=new Library.CopyContext();\n", static, result, name, strings.Join(ps, ","))
	for _, p := range pre {
		b.WriteString(p)
	}
	fmt.Fprintf(b, "var hs=new Library.Handle[]{%s};\n", strings.Join(hs, ","))
	fmt.Fprintf(b, "return Library.submit<%s>(options,libraryState,hs,%s,(inst,rv) => {\n", result, frame(f, as))
	if hasErr {
		fmt.Fprintf(b, "Library.Failure err=sourceFailure((Box)rv[%d]);if(err!=null)throw err;\n", n)
	}
	out := func(i int) string {
		if h := abi.Handle(results[i]); h != nil {
			return fmt.Sprintf("wrap%s(inst,rv[%d])", h.Name, i)
		}
		return fmt.Sprintf("%s(%s)", e.names.Type(results[i].U(), "output_"), e.cast(results[i], fmt.Sprintf("rv[%d]", i)))
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
	fmt.Fprintf(b, "},null);\n} catch(Library.Failure e) { return Library.failed<%s>(e); }\n%s}\n", result, indent)
	return nil
}
