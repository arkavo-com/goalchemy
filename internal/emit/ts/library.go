package ts

import (
	"fmt"
	"github.com/eugenioenko/goalchemy/internal/emit/artifact"
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
	if libraryStreams(e.p, abi) {
		b.WriteString("export { SourceStream } from './rt/runtime/library.ts';\nexport type { SourceReader, SourceWriter, SourceSeeker, SourceCloser } from './rt/runtime/library.ts';\n")
		for _, n := range []string{"SourceStream", "SourceReader", "SourceWriter", "SourceSeeker", "SourceCloser"} {
			used[n] = true
		}
	}
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
		if k := ir.StreamKind(t); k != "" {
			return "(" + streamType(ir.KindOps(k)) + ") | null"
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
			start := b.Len()
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
			e.api = append(e.api, artifact.APIDeclaration{Type: t, Name: n, Signature: b.String()[start:]})
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
		fmt.Fprintf(&b, "function %s(_v:any):any {\n", e.names.Type(u, "input$"))
		switch u.Kind {
		case ir.KFloat:
			fmt.Fprintf(&b, "return rt.boundaryFloat(_v,%d);\n", u.FloatBits)
		case ir.KBool:
			b.WriteString("return rt.boundaryBool(_v);\n")
		case ir.KString:
			b.WriteString("return rt.boundaryString(_v);\n")
		case ir.KInt:
			fmt.Fprintf(&b, "return rt.boundaryInt(_v,%d,%t);\n", u.Int.Bits(), u.Int.Signed())
		case ir.KStruct:
			fmt.Fprintf(&b, "const _o=rt.boundaryObject(_v),_r=new %s();\n", e.class(u))
			for i, f := range u.Fields {
				fmt.Fprintf(&b, "_r.%s=%s(_o[%q]);\n", e.fieldProp(u, i), e.names.Type(f.Type.U(), "input$"), f.Name)
			}
			b.WriteString("return _r;\n")
		case ir.KSlice:
			if byteElem(u.Elem) {
				b.WriteString("return rt.byteSlice(rt.boundaryBytes(_v));\n")
			} else {
				fmt.Fprintf(&b, "const _a=rt.boundaryArray(_v);return _a===null?rt.NIL:rt.fromArray(Array.from({length:_a.length},(_,_i)=>%s(_a[_i])));\n", e.names.Type(u.Elem.U(), "input$"))
			}
		case ir.KArray:
			if byteElem(u.Elem) {
				fmt.Fprintf(&b, "const _a=rt.boundaryBytes(_v);if(_a===null||_a.length!==%d)rt.invalidBoundary();return _a;\n", u.Len)
			} else {
				fmt.Fprintf(&b, "const _a=rt.boundaryArray(_v);if(_a===null||_a.length!==%d)rt.invalidBoundary();return Array.from({length:_a.length},(_,_i)=>%s(_a[_i]));\n", u.Len, e.names.Type(u.Elem.U(), "input$"))
			}
		}
		b.WriteString("}\n")
		fmt.Fprintf(&b, "function %s(_v:any):any {\n", e.names.Type(u, "output$"))
		switch u.Kind {
		case ir.KStruct:
			b.WriteString("return {")
			for i, f := range u.Fields {
				fmt.Fprintf(&b, "%s:%s(_v.%s),", f.Name, e.names.Type(f.Type.U(), "output$"), e.fieldProp(u, i))
			}
			b.WriteString("};\n")
		case ir.KSlice:
			if byteElem(u.Elem) {
				b.WriteString("return _v.a===null?null:rt.bytes(_v);\n")
			} else {
				fmt.Fprintf(&b, "return _v.a===null?null:rt.toArray(_v).map(_x=>%s(_x));\n", e.names.Type(u.Elem.U(), "output$"))
			}
		case ir.KArray:
			if byteElem(u.Elem) {
				b.WriteString("return new Uint8Array(_v);\n")
			} else {
				fmt.Fprintf(&b, "return _v.map(_x=>%s(_x));\n", e.names.Type(u.Elem.U(), "output$"))
			}
		default:
			b.WriteString("return _v;\n")
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
			if abi.Handle(t) == nil && ir.StreamKind(t) == "" {
				converter(t)
			}
		}
	}
	if view := e.p.HostIOFunc("ReadView"); view != nil {
		for _, name := range []string{"ReadView", "WriteView", "SeekView"} {
			f := e.p.HostIOFunc(name)
			for _, t := range f.Sig.Params[1:] {
				converter(t)
			}
			for _, t := range f.Sig.Results[:len(f.Sig.Results)-1] {
				converter(t)
			}
		}
	}
	for _, t := range e.p.Types.All {
		if t.Kind == ir.KPointer && t.Elem.U().Kind == ir.KStruct && libraryValue(t.Elem, map[*ir.Type]bool{}) && e.tds[t] != "" {
			converter(t.Elem)
		}
	}
	b.WriteString("function _sourceFailure(err:any):rt.LibraryError|null {\nif(err===null)return null;\n")
	for _, t := range e.p.Types.All {
		if t.Kind == ir.KPointer && t.Elem.U().Kind == ir.KStruct && libraryValue(t.Elem, map[*ir.Type]bool{}) && e.tds[t] != "" {
			fmt.Fprintf(&b, "if(err.t===%s)return new rt.LibraryError('source',%s(err.v));\n", e.tds[t], e.names.Type(t.Elem.U(), "output$"))
		}
	}
	b.WriteString("return new rt.LibraryError('source');\n}\nfunction _libraryReset():void{\n")
	for _, g := range e.p.Globals {
		if g.AddrTaken && !g.Type.IsAggregate() {
			fmt.Fprintf(&b, "%s.v=%s;\n", e.symbol(g.Sym), e.zero(g.Type))
		} else {
			fmt.Fprintf(&b, "%s=%s;\n", e.symbol(g.Sym), e.zero(g.Type))
		}
	}
	b.WriteString("}\nfunction _librarySave():unknown{\nreturn [")
	for _, g := range e.p.Globals {
		if g.AddrTaken && !g.Type.IsAggregate() {
			fmt.Fprintf(&b, "%s.v,", e.symbol(g.Sym))
		} else {
			fmt.Fprintf(&b, "%s,", e.symbol(g.Sym))
		}
	}
	b.WriteString("];\n}\nfunction _libraryLoad(_saved:unknown):void{\nconst _v=_saved as any[];\n")
	for i, g := range e.p.Globals {
		if g.AddrTaken && !g.Type.IsAggregate() {
			fmt.Fprintf(&b, "%s.v=_v[%d];\n", e.symbol(g.Sym), i)
		} else {
			fmt.Fprintf(&b, "%s=_v[%d];\n", e.symbol(g.Sym), i)
		}
	}
	b.WriteString("}\nconst _libraryState:rt.LibraryState={reset:_libraryReset,save:_librarySave,load:_libraryLoad};\n")
	if len(abi.Handles) > 0 {
		b.WriteString("const _H=Symbol('handle');\n")
	}
	init := e.symbol(e.p.Init.Sym) + "()"
	if !e.p.Init.MaySuspend {
		init = "rt.sync(()=>{ " + e.symbol(e.p.Init.Sym) + "();return [];})"
	} else if e.native() {
		init = "rt.nativeTask((_t:rt.Task)=>" + e.symbol(e.p.Init.Sym) + "(_t),0)"
	}
	frame := func(f *ir.Func, as []string) string {
		call := e.symbol(f.Sym) + "(" + strings.Join(as, ",") + ")"
		if f.MaySuspend && e.native() {
			call = fmt.Sprintf("rt.nativeTask((_t:rt.Task)=>%s(%s),%d)", e.symbol(f.Sym), strings.Join(append(append([]string(nil), as...), "_t"), ","), len(f.Sig.Results))
		}
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
		return "(_ctx,_fresh)=>_fresh?rt.librarySequence(" + init + ",()=>" + call + "):" + call
	}
	streamOps := func(name string, ops ir.StreamOps, obj string, closeRun string) {
		call := func(fn string, args []string, outs string) string {
			f := e.p.HostIOFunc(fn)
			return fmt.Sprintf("rt.runLibraryCall({},_libraryState,[_h],%s,(_inst,_rv)=>[%s,_sourceFailure(_rv[%d])]).then(_o=>{if(_o[%d]!==null)throw _o[%d];return _o;})",
				frame(f, append([]string{obj}, args...)), outs, len(f.Sig.Results)-1, len(f.Sig.Results)-1, len(f.Sig.Results)-1)
		}
		conv := func(fn string, i int, dir string) string {
			f := e.p.HostIOFunc(fn)
			t := f.Sig.Params
			if dir == "output$" {
				t = f.Sig.Results
			}
			return e.names.Type(t[i].U(), dir)
		}
		fmt.Fprintf(&b, "const %s:rt.StreamOps={\n", name)
		if ops.Read {
			fmt.Fprintf(&b, "read:(_h,_n)=>%s.then(_o=>[(_o[0] as Uint8Array|null)??new Uint8Array(0),_o[1] as boolean]),\n",
				call("ReadView", []string{conv("ReadView", 1, "input$") + "(BigInt(_n))"}, conv("ReadView", 0, "output$")+"(_rv[0]),_rv[1]"))
		}
		if ops.Write {
			fmt.Fprintf(&b, "write:(_h,_d)=>%s.then(_o=>Number(_o[0])),\n",
				call("WriteView", []string{conv("WriteView", 1, "input$") + "(_d)"}, conv("WriteView", 0, "output$")+"(_rv[0])"))
		}
		if ops.Seek {
			fmt.Fprintf(&b, "seek:(_h,_off,_whence)=>%s.then(_o=>_o[0] as bigint),\n",
				call("SeekView", []string{conv("SeekView", 1, "input$") + "(_off)", conv("SeekView", 2, "input$") + "(BigInt(_whence))"}, conv("SeekView", 0, "output$")+"(_rv[0])"))
		}
		fmt.Fprintf(&b, "close:_h=>rt.closeHandle(_h,_libraryState,%s,_sourceFailure),\n};\n", closeRun)
	}
	kinds := map[string]bool{}
	for _, f := range funcs {
		for _, t := range f.Sig.Results {
			if k := ir.StreamKind(t); k != "" && !kinds[k] {
				kinds[k] = true
				run := "null"
				if ir.StreamCloses(k) {
					run = frame(e.p.HostIOFunc("CloseView"), []string{"rt.obj(_h)"})
				}
				streamOps("streamOps$"+k, ir.KindOps(k), "rt.obj(_h)", run)
			}
		}
	}
	for _, h := range abi.Handles {
		if h.Stream.Any() {
			run := "null"
			if h.Close != nil {
				run = frame(h.Close, []string{e.handleRecv(h, h.Close, "rt.obj(_h)")})
			}
			streamOps("streamOps$"+h.Name, h.Stream, "rt.box("+e.td(h.Ptr)+",rt.obj(_h))", run)
		}
	}
	for _, h := range abi.Handles {
		e.api = append(e.api, artifact.APIDeclaration{Type: h.Elem, Name: h.Name, Signature: "export class " + h.Name})
		if h.Close != nil {
			e.api = append(e.api, artifact.APIDeclaration{Function: h.Close, Name: "close", Signature: "close(): Promise<void>"})
		}
		fmt.Fprintf(&b, "/** Handle to a source *%s owned by a library instance. Call close() when\n * done; a collected handle is released without running Close. */\nexport class %s {\nreadonly [_H]:rt.Handle;\n/** @internal */\nconstructor(_h:rt.Handle){this[_H]=_h;}\n", h.Name, h.Name)
		run := "null"
		if h.Close != nil {
			run = frame(h.Close, []string{e.handleRecv(h, h.Close, "rt.obj(this[_H])")})
		}
		fmt.Fprintf(&b, "/** Runs the source Close, if any, and releases the handle. Queued calls\n * fail and an active call is canceled first. */\nclose():Promise<void>{return rt.closeHandle(this[_H],_libraryState,%s,_sourceFailure);}\n[Symbol.asyncDispose]():Promise<void>{return this.close();}\n", run)
		if h.Stream.Any() {
			fmt.Fprintf(&b, "/** The handle as a stream; each operation is a call on the handle and\n * close() closes the handle. */\nio():%s{return new rt.SourceStream(this[_H],streamOps$%s);}\n", streamType(h.Stream), h.Name)
			if h.Stream.Read {
				b.WriteString("/** A web ReadableStream over the handle's Read. */\nstream(chunkSize?:number):ReadableStream<Uint8Array>{return this.io().stream(chunkSize);}\n[Symbol.asyncIterator]():AsyncIterator<Uint8Array>{return this.io()[Symbol.asyncIterator]();}\n")
			}
			if h.Stream.Write {
				b.WriteString("/** A web WritableStream into the handle's Write; closing it closes the handle. */\nwritable():WritableStream<Uint8Array>{return this.io().writable();}\n")
			}
		}
		for _, f := range h.Methods {
			if err := e.libraryWrapper(&b, abi, ir.MethodName(f), h, f, typ, frame); err != nil {
				return "", err
			}
		}
		b.WriteString("}\n")
		fmt.Fprintf(&b, "function wrap$%s(_inst:rt.Instance,_v:unknown):%s|null{return rt.wrap(_inst,_v,%q,_h=>new %s(_h));}\n", h.Name, h.Name, h.Name, h.Name)
		fmt.Fprintf(&b, "function handle$%s(_v:unknown):rt.Handle|null{if(_v===null||_v===undefined)return null;if(!(_v instanceof %s))rt.invalidBoundary();return _v[_H];}\n", h.Name, h.Name)
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

// handleRecv is the source receiver argument of method f of h on obj; a value
// receiver gets a copy.
func (e *emitter) handleRecv(h *ir.Handle, f *ir.Func, obj string) string {
	if h.ValueReceiver(f) {
		return e.cloneExpr(f.RecvType, obj)
	}
	return obj
}

func (e *emitter) libraryWrapper(b *strings.Builder, abi *ir.LibraryABI, name string, recv *ir.Handle, f *ir.Func, typ func(*ir.Type) string, frame func(*ir.Func, []string) string) error {
	{
		params := f.Sig.Params
		var ps, as, hs []string
		if recv != nil {
			params = params[1:]
			as = append(as, e.handleRecv(recv, f, "rt.obj(this[_H])"))
			hs = append(hs, "this[_H]")
		}
		var conv, streams []string
		for i, t := range params {
			if sourceContext(t) {
				as = append(as, "_ctx")
				continue
			}
			a := fmt.Sprintf("_a%d", i)
			if k := ir.StreamKind(t); k != "" {
				ps = append(ps, a+": rt.HostStreamInput | null")
				conv = append(conv, fmt.Sprintf("const _s%d=rt.hostStream(_a%d,%q);\n", i, i, k))
				as = append(as, fmt.Sprintf("(_ids[%d]===0n?null:%s(_ids[%d]))", len(streams), e.symbol(e.p.StreamAdapter(k).Sym), len(streams)))
				streams = append(streams, fmt.Sprintf("_s%d", i))
				continue
			}
			ps = append(ps, a+": "+typ(t))
			if h := abi.Handle(t); h != nil {
				conv = append(conv, fmt.Sprintf("const _h%d=handle$%s(_a%d);\n", i, h.Name, i))
				hs = append(hs, fmt.Sprintf("_h%d", i))
				as = append(as, fmt.Sprintf("rt.obj(_h%d)", i))
				continue
			}
			conv = append(conv, fmt.Sprintf("_a%d=%s(_a%d);\n", i, e.names.Type(t.U(), "input$"), i))
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
		prefix := "async"
		if recv == nil {
			prefix = "export async function"
		}
		e.api = append(e.api, artifact.APIDeclaration{Function: f, Name: name, Signature: fmt.Sprintf("%s %s(%s): Promise<%s>", prefix, name, strings.Join(ps, ","), result)})
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
		fmt.Fprintf(b, "const _ids:bigint[]=[%s];\n", strings.Join(ids, ","))
		fmt.Fprintf(b, "const _owned=await rt.runLibraryCall(options,_libraryState,[%s],%s,(_inst,_rv)=>{\nreturn [", strings.Join(hs, ","), frame(f, as))
		for i := 0; i < n; i++ {
			if h := abi.Handle(results[i]); h != nil {
				fmt.Fprintf(b, "wrap$%s(_inst,_rv[%d]),", h.Name, i)
				continue
			}
			if k := ir.StreamKind(results[i]); k != "" {
				fmt.Fprintf(b, "rt.wrap(_inst,_rv[%d],\"io.%s\",_h=>new rt.SourceStream(_h,streamOps$%s)),", i, k, k)
				continue
			}
			fmt.Fprintf(b, "%s(_rv[%d]),", e.names.Type(results[i].U(), "output$"), i)
		}
		if hasErr {
			fmt.Fprintf(b, "_sourceFailure(_rv[%d]),", n)
		}
		fmt.Fprintf(b, "];},null,[%s],_ids);\n", strings.Join(streams, ","))
		if hasErr {
			fmt.Fprintf(b, "if(_owned[%d]!==null)throw _owned[%d];\n", n, n)
		}
		if n == 1 {
			b.WriteString("return _owned[0] as any;\n")
		} else if n > 1 {
			fmt.Fprintf(b, "return _owned.slice(0,%d) as any;\n", n)
		}
		b.WriteString("}\n")
	}
	return nil
}

// streamType is the TypeScript type of a source stream with ops.
func streamType(ops ir.StreamOps) string {
	var parts []string
	if ops.Read {
		parts = append(parts, "rt.SourceReader")
	}
	if ops.Write {
		parts = append(parts, "rt.SourceWriter")
	}
	if ops.Seek {
		parts = append(parts, "rt.SourceSeeker")
	}
	return strings.Join(append(parts, "rt.SourceCloser", "AsyncDisposable"), " & ")
}

// libraryStreams reports whether a library returns std/io streams or has
// handles usable as streams.
func libraryStreams(p *ir.Program, abi *ir.LibraryABI) bool {
	for _, h := range abi.Handles {
		if h.Stream.Any() {
			return true
		}
	}
	return p.StreamResult() != ""
}
