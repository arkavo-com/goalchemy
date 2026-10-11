package ts

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
func (e *emitter) library() (string, error) {
	e.use("core.task.spawn")
	e.use("std.context.with_cancel")
	e.use("std.context.err")
	var b strings.Builder
	b.WriteString("export { LibraryError } from './rt/runtime/library.ts';\nexport type { CallOptions, Callback, Settlement } from './rt/runtime/library.ts';\nexport { setLogHandler } from './rt/types/log.ts';\nexport type { LogRecord, LogHandler } from './rt/types/log.ts';\n")
	abi, err := e.p.LibraryHandles(func(t *ir.Type) bool { return libraryValue(t, map[*ir.Type]bool{}) })
	if err != nil {
		return "", err
	}
	roots := map[string]bool{}
	for _, f := range e.p.Exports {
		roots[f.Pkg] = true
	}
	names := map[*ir.Type]string{}
	used := map[string]bool{"LibraryError": true, "CallOptions": true, "Callback": true, "LogRecord": true, "LogHandler": true}
	handleElems := map[*ir.Type]bool{}
	for _, h := range abi.Handles {
		if used[h.Name] {
			return "", fmt.Errorf("duplicate library public name %s", h.Name)
		}
		used[h.Name] = true
		handleElems[h.Elem] = true
	}
	for _, t := range e.p.Types.All {
		if t.Kind == ir.KNamed && roots[t.Pkg] && token.IsExported(t.Obj) && !handleElems[t] && libraryValue(t, map[*ir.Type]bool{}) {
			if used[t.Obj] {
				return "", fmt.Errorf("duplicate library public name %s", t.Obj)
			}
			used[t.Obj] = true
			names[t] = t.Obj
		}
	}
	var typ func(*ir.Type) string
	typ = func(t *ir.Type) string {
		if h := abi.Handle(t); h != nil {
			return h.Name + " | null"
		}
		if n := names[t]; n != "" {
			return n
		}
		u := t.U()
		switch u.Kind {
		case ir.KFloat:
			return "number"
		case ir.KBool:
			return "boolean"
		case ir.KInt:
			if wide(t) {
				return "bigint"
			}
			return "number"
		case ir.KString:
			return "string"
		case ir.KSlice:
			if byteElem(u.Elem) {
				return "Uint8Array | null"
			}
			return "(" + typ(u.Elem) + ")[] | null"
		case ir.KArray:
			if byteElem(u.Elem) {
				return "Uint8Array"
			}
			return "(" + typ(u.Elem) + ")[]"
		case ir.KStruct:
			var fs []string
			for _, f := range u.Fields {
				fs = append(fs, f.Name+"?: "+typ(f.Type))
			}
			return "{ " + strings.Join(fs, "; ") + " }"
		}
		return "never"
	}
	for _, t := range e.p.Types.All {
		if n := names[t]; n != "" {
			u := t.U()
			if u.Kind == ir.KStruct {
				fmt.Fprintf(&b, "export interface %s {\n", n)
				for _, f := range u.Fields {
					fmt.Fprintf(&b, " %s?: %s;\n", f.Name, typ(f.Type))
				}
				b.WriteString("}\n")
			} else {
				delete(names, t)
				fmt.Fprintf(&b, "export type %s = %s;\n", n, typ(t))
				names[t] = n
			}
		}
	}
	// Generate owner-side recursive conversion for every admitted type.
	seen := map[*ir.Type]bool{}
	var converter func(*ir.Type)
	converter = func(t *ir.Type) {
		u := t.U()
		if seen[u] {
			return
		}
		seen[u] = true
		switch u.Kind {
		case ir.KStruct:
			for _, f := range u.Fields {
				converter(f.Type)
			}
		case ir.KSlice, ir.KArray:
			converter(u.Elem)
		}
		fmt.Fprintf(&b, "function %s(v:any):any {\n", e.names.Type(u, "input$"))
		switch u.Kind {
		case ir.KFloat:
			fmt.Fprintf(&b, "return rt.boundaryFloat(v,%d);\n", u.FloatBits)
		case ir.KBool:
			b.WriteString("return rt.boundaryBool(v);\n")
		case ir.KString:
			b.WriteString("return rt.boundaryString(v);\n")
		case ir.KInt:
			fmt.Fprintf(&b, "return rt.boundaryInt(v,%d,%t);\n", u.Int.Bits(), u.Int.Signed())
		case ir.KStruct:
			fmt.Fprintf(&b, "const o=rt.boundaryObject(v),r=new %s();\n", e.class(u))
			for i, f := range u.Fields {
				fmt.Fprintf(&b, "r.%s=%s(o[%q]);\n", e.fieldProp(u, i), e.names.Type(f.Type.U(), "input$"), f.Name)
			}
			b.WriteString("return r;\n")
		case ir.KSlice:
			if byteElem(u.Elem) {
				b.WriteString("return rt.byteSlice(rt.boundaryBytes(v));\n")
			} else {
				fmt.Fprintf(&b, "const a=rt.boundaryArray(v);return a===null?rt.NIL:rt.fromArray(Array.from({length:a.length},(_,i)=>%s(a[i])));\n", e.names.Type(u.Elem.U(), "input$"))
			}
		case ir.KArray:
			if byteElem(u.Elem) {
				fmt.Fprintf(&b, "const a=rt.boundaryBytes(v);if(a===null||a.length!==%d)rt.invalidBoundary();return a;\n", u.Len)
			} else {
				fmt.Fprintf(&b, "const a=rt.boundaryArray(v);if(a===null||a.length!==%d)rt.invalidBoundary();return Array.from({length:a.length},(_,i)=>%s(a[i]));\n", u.Len, e.names.Type(u.Elem.U(), "input$"))
			}
		}
		b.WriteString("}\n")
		fmt.Fprintf(&b, "function %s(v:any):any {\n", e.names.Type(u, "output$"))
		switch u.Kind {
		case ir.KStruct:
			b.WriteString("return {")
			for i, f := range u.Fields {
				fmt.Fprintf(&b, "%s:%s(v.%s),", f.Name, e.names.Type(f.Type.U(), "output$"), e.fieldProp(u, i))
			}
			b.WriteString("};\n")
		case ir.KSlice:
			if byteElem(u.Elem) {
				b.WriteString("return v.a===null?null:rt.bytes(v);\n")
			} else {
				fmt.Fprintf(&b, "return v.a===null?null:rt.toArray(v).map(x=>%s(x));\n", e.names.Type(u.Elem.U(), "output$"))
			}
		case ir.KArray:
			if byteElem(u.Elem) {
				b.WriteString("return new Uint8Array(v);\n")
			} else {
				fmt.Fprintf(&b, "return v.map(x=>%s(x));\n", e.names.Type(u.Elem.U(), "output$"))
			}
		default:
			b.WriteString("return v;\n")
		}
		b.WriteString("}\n")
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
				converter(t)
			}
		}
		for i, t := range f.Sig.Results {
			if i == len(f.Sig.Results)-1 && sourceError(t) {
				continue
			}
			if abi.Handle(t) == nil {
				converter(t)
			}
		}
	}
	for _, t := range e.p.Types.All {
		if t.Kind == ir.KPointer && t.Elem.U().Kind == ir.KStruct && libraryValue(t.Elem, map[*ir.Type]bool{}) && e.tds[t] != "" {
			converter(t.Elem)
		}
	}
	b.WriteString("function sourceFailure(err:any):rt.LibraryError|null {\nif(err===null)return null;\n")
	for _, t := range e.p.Types.All {
		if t.Kind == ir.KPointer && t.Elem.U().Kind == ir.KStruct && libraryValue(t.Elem, map[*ir.Type]bool{}) && e.tds[t] != "" {
			fmt.Fprintf(&b, "if(err.t===%s)return new rt.LibraryError('source',%s(err.v));\n", e.tds[t], e.names.Type(t.Elem.U(), "output$"))
		}
	}
	b.WriteString("return new rt.LibraryError('source');\n}\nfunction libraryReset():void{\n")
	for _, g := range e.p.Globals {
		if g.AddrTaken && !g.Type.IsAggregate() {
			fmt.Fprintf(&b, "%s.v=%s;\n", e.symbol(g.Sym), e.zero(g.Type))
		} else {
			fmt.Fprintf(&b, "%s=%s;\n", e.symbol(g.Sym), e.zero(g.Type))
		}
	}
	b.WriteString("}\nfunction librarySave():unknown{\nreturn [")
	for _, g := range e.p.Globals {
		if g.AddrTaken && !g.Type.IsAggregate() {
			fmt.Fprintf(&b, "%s.v,", e.symbol(g.Sym))
		} else {
			fmt.Fprintf(&b, "%s,", e.symbol(g.Sym))
		}
	}
	b.WriteString("];\n}\nfunction libraryLoad(saved:unknown):void{\nconst v=saved as any[];\n")
	for i, g := range e.p.Globals {
		if g.AddrTaken && !g.Type.IsAggregate() {
			fmt.Fprintf(&b, "%s.v=v[%d];\n", e.symbol(g.Sym), i)
		} else {
			fmt.Fprintf(&b, "%s=v[%d];\n", e.symbol(g.Sym), i)
		}
	}
	b.WriteString("}\nconst libraryState:rt.LibraryState={reset:libraryReset,save:librarySave,load:libraryLoad};\n")
	if len(abi.Handles) > 0 {
		b.WriteString("const H=Symbol('handle');\n")
	}
	init := e.symbol(e.p.Init.Sym) + "()"
	if !e.p.Init.MaySuspend {
		init = "rt.sync(()=>{ " + e.symbol(e.p.Init.Sym) + "();return [];})"
	}
	frame := func(f *ir.Func, as []string) string {
		call := e.symbol(f.Sym) + "(" + strings.Join(as, ",") + ")"
		if !f.MaySuspend {
			body := call
			if len(f.Sig.Results) == 0 {
				body = call + ";return []"
			} else if len(f.Sig.Results) == 1 {
				body = "return [" + call + "]"
			} else {
				body = "return " + call
			}
			call = "rt.sync(()=>{ " + body + ";})"
		}
		return "(ctx,fresh)=>fresh?rt.librarySequence(" + init + ",()=>" + call + "):" + call
	}
	for _, h := range abi.Handles {
		fmt.Fprintf(&b, "/** Handle to a source *%s owned by a library instance. Call close() when\n * done; a collected handle is released without running Close. */\nexport class %s {\nreadonly [H]:rt.Handle;\n/** @internal */\nconstructor(h:rt.Handle){this[H]=h;}\n", h.Name, h.Name)
		run := "null"
		if h.Close != nil {
			run = frame(h.Close, []string{"rt.obj(this[H])"})
		}
		fmt.Fprintf(&b, "/** Runs the source Close, if any, and releases the handle. Queued calls\n * fail and an active call is canceled first. */\nclose():Promise<void>{return rt.closeHandle(this[H],libraryState,%s,sourceFailure);}\n[Symbol.asyncDispose]():Promise<void>{return this.close();}\n", run)
		for _, f := range h.Methods {
			if err := e.libraryWrapper(&b, abi, ir.MethodName(f), h, f, typ, frame); err != nil {
				return "", err
			}
		}
		b.WriteString("}\n")
		fmt.Fprintf(&b, "function wrap$%s(inst:rt.Instance,v:unknown):%s|null{return rt.wrap(inst,v,%q,h=>new %s(h));}\n", h.Name, h.Name, h.Name, h.Name)
		fmt.Fprintf(&b, "function handle$%s(v:unknown):rt.Handle|null{if(v===null||v===undefined)return null;if(!(v instanceof %s))rt.invalidBoundary();return v[H];}\n", h.Name, h.Name)
	}
	for _, f := range e.p.Exports {
		name := f.Name[strings.LastIndex(f.Name, ".")+1:]
		if used[name] {
			return "", fmt.Errorf("duplicate export name %s", name)
		}
		used[name] = true
		if err := e.libraryWrapper(&b, abi, name, nil, f, typ, frame); err != nil {
			return "", err
		}
	}
	return b.String(), nil
}

func (e *emitter) libraryWrapper(b *strings.Builder, abi *ir.LibraryABI, name string, recv *ir.Handle, f *ir.Func, typ func(*ir.Type) string, frame func(*ir.Func, []string) string) error {
	{
		params := f.Sig.Params
		var ps, as, hs []string
		if recv != nil {
			params = params[1:]
			as = append(as, "rt.obj(this[H])")
			hs = append(hs, "this[H]")
		}
		var conv, streams []string
		for i, t := range params {
			if sourceContext(t) {
				as = append(as, "ctx")
				continue
			}
			a := fmt.Sprintf("a%d", i)
			if k := ir.StreamKind(t); k != "" {
				ps = append(ps, a+": rt.HostStreamInput | null")
				conv = append(conv, fmt.Sprintf("const s%d=rt.hostStream(a%d,%q);\n", i, i, k))
				as = append(as, fmt.Sprintf("(ids[%d]===0n?null:%s(ids[%d]))", len(streams), e.symbol(e.p.StreamAdapter(k).Sym), len(streams)))
				streams = append(streams, fmt.Sprintf("s%d", i))
				continue
			}
			ps = append(ps, a+": "+typ(t))
			if h := abi.Handle(t); h != nil {
				conv = append(conv, fmt.Sprintf("const h%d=handle$%s(a%d);\n", i, h.Name, i))
				hs = append(hs, fmt.Sprintf("h%d", i))
				as = append(as, fmt.Sprintf("rt.obj(h%d)", i))
				continue
			}
			conv = append(conv, fmt.Sprintf("a%d=%s(a%d);\n", i, e.names.Type(t.U(), "input$"), i))
			as = append(as, a)
		}
		ps = append(ps, "options: rt.CallOptions = {}")
		results := f.Sig.Results
		n := len(results)
		hasErr := n > 0 && sourceError(results[n-1])
		if hasErr {
			n--
		}
		result := "void"
		if n == 1 {
			result = typ(results[0])
		} else if n > 1 {
			var rs []string
			for i := 0; i < n; i++ {
				rs = append(rs, typ(results[i]))
			}
			result = "[" + strings.Join(rs, ",") + "]"
		}
		if recv != nil {
			fmt.Fprintf(b, "async %s(%s):Promise<%s>{\noptions=rt.snapshotOptions(options);\n", name, strings.Join(ps, ","), result)
		} else {
			fmt.Fprintf(b, "export async function %s(%s):Promise<%s>{\noptions=rt.snapshotOptions(options);\n", name, strings.Join(ps, ","), result)
		}
		for _, c := range conv {
			b.WriteString(c)
		}
		ids := make([]string, len(streams))
		for i := range ids {
			ids[i] = "0n"
		}
		fmt.Fprintf(b, "const ids:bigint[]=[%s];\n", strings.Join(ids, ","))
		fmt.Fprintf(b, "const owned=await rt.runLibraryCall(options,libraryState,[%s],%s,(inst,rv)=>{\nreturn [", strings.Join(hs, ","), frame(f, as))
		for i := 0; i < n; i++ {
			if h := abi.Handle(results[i]); h != nil {
				fmt.Fprintf(b, "wrap$%s(inst,rv[%d]),", h.Name, i)
				continue
			}
			fmt.Fprintf(b, "%s(rv[%d]),", e.names.Type(results[i].U(), "output$"), i)
		}
		if hasErr {
			fmt.Fprintf(b, "sourceFailure(rv[%d]),", n)
		}
		fmt.Fprintf(b, "];},null,[%s],ids);\n", strings.Join(streams, ","))
		if hasErr {
			fmt.Fprintf(b, "if(owned[%d]!==null)throw owned[%d];\n", n, n)
		}
		if n == 1 {
			b.WriteString("return owned[0] as any;\n")
		} else if n > 1 {
			fmt.Fprintf(b, "return owned.slice(0,%d) as any;\n", n)
		}
		b.WriteString("}\n")
	}
	return nil
}
