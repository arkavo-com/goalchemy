// Package swift emits native Swift with explicit Go storage. Functions that
// cannot pause are plain Swift functions; the others are resumable frames
// driven by the cooperative scheduler.
package swift

import (
	"bytes"
	"fmt"
	"go/constant"
	"go/token"
	"go/types"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/eugenioenko/goalchemy/internal/emit/artifact"
	"github.com/eugenioenko/goalchemy/internal/ir"
	"github.com/eugenioenko/goalchemy/internal/naming"
	"github.com/eugenioenko/goalchemy/internal/structure"
)

type Output struct {
	Files     []artifact.File
	Packages  []artifact.PackageFiles
	Contracts []string
}

type emitter struct {
	p           *ir.Program
	names       *naming.Names
	handleNames map[*ir.Handle]string
	contracts   map[string]bool
	symbols     map[string]string
}

func quote(s string) string { return strconv.Quote(s) }

// swiftString spells valid UTF-8 as a Swift string literal.
func swiftString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '\\' || r == '"':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r >= 0x20 && r < 0x7f:
			b.WriteRune(r)
		default:
			fmt.Fprintf(&b, `\u{%x}`, r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
func id(t *ir.Type) int {
	if t == nil {
		return -1
	}
	return t.ID
}
func (e *emitter) use(s string) { e.contracts[s] = true }

func (e *emitter) native() bool { return e.p.PauseModel == "native" }

// async reports whether f is an async function: it may pause, under the
// native pause model.
func (e *emitter) async(f *ir.Func) bool { return f.MaySuspend && e.native() }

// function returns a function value of f with the given captured cells.
func (e *emitter) functionValue(f *ir.Func, env string) string {
	sym := e.names.Symbol(f.Sym)
	args := []string{strconv.Itoa(f.ID), sym}
	if env != "" {
		args = append(args, env)
	}
	if e.async(f) {
		args = append(args, "native: "+sym+"_n")
	}
	return "GFunction(" + strings.Join(args, ", ") + ")"
}
func (e *emitter) types(b *bytes.Buffer) {
	// Preserve Go's complete method signatures, including aliases and receiver
	// method sets. Native errors have negative IDs and use declared signatures.
	var interfaces []*ir.Type
	for _, t := range e.p.Types.All {
		if t.IsInterface() && t.Go != nil {
			interfaces = append(interfaces, t)
		}
	}
	nativeSignatures := map[string]*types.Signature{}
	errorType := types.Universe.Lookup("error").Type()
	for name, result := range map[string]types.Type{
		"Error": types.Typ[types.String], "String": types.Typ[types.String], "RuntimeError": nil,
		"Timeout": types.Typ[types.Bool], "Temporary": types.Typ[types.Bool],
		"Unwrap": errorType, "Is": types.Typ[types.Bool],
	} {
		params := types.NewTuple()
		if name == "Is" {
			params = types.NewTuple(types.NewVar(token.NoPos, nil, "", errorType))
		}
		results := types.NewTuple()
		if result != nil {
			results = types.NewTuple(types.NewVar(token.NoPos, nil, "", result))
		}
		nativeSignatures[name] = types.NewSignatureType(nil, nil, nil, params, results, false)
	}
	const typeRowsPerFunc = 32
	chunks := (len(e.p.Types.All) + typeRowsPerFunc - 1) / typeRowsPerFunc
	missingTables := map[string]string{}
	var missingDeclarations strings.Builder
	for i, t := range e.p.Types.All {
		if i%typeRowsPerFunc == 0 {
			if i > 0 {
				b.WriteString("}\n\n")
			}
			fmt.Fprintf(b, "private func gTypeRows%d(_ table: inout [GType]) {\n", i/typeRowsPerFunc)
		}
		u := t.U()
		var fields, names, blank []string
		for _, f := range u.Fields {
			fields = append(fields, strconv.Itoa(f.Type.ID))
			names = append(names, quote(f.Name))
			blank = append(blank, strconv.FormatBool(f.Name == "_"))
		}
		var methods, nativeMethods, implementations, missingMethods []string
		for _, m := range u.Methods {
			methods = append(methods, quote(m.ID))
		}
		if t.Go != nil {
			methodSet := types.NewMethodSet(t.Go)
			for i := 0; i < methodSet.Len(); i++ {
				method := methodSet.At(i).Obj()
				if signature := nativeSignatures[method.Id()]; signature != nil && types.Identical(method.Type(), signature) {
					nativeMethods = append(nativeMethods, quote(method.Id()))
				}
			}
			for _, target := range interfaces {
				iface := target.Go.Underlying().(*types.Interface)
				if types.Implements(t.Go, iface) {
					implementations = append(implementations, strconv.Itoa(target.ID))
				} else if method, _ := types.MissingMethod(t.Go, iface, true); method != nil {
					missingMethods = append(missingMethods, fmt.Sprintf("%d: %s", target.ID, quote(method.Id())))
				}
			}
		}
		missing := strings.Join(missingMethods, ", ")
		if missing == "" {
			missing = "[:]"
		} else {
			symbol, ok := missingTables[missing]
			if !ok {
				symbol = fmt.Sprintf("gMissingInterfaceMethods%d", len(missingTables))
				missingTables[missing] = symbol
				fmt.Fprintf(&missingDeclarations, "private let %s: [Int: String] = [%s]\n", symbol, missing)
			}
			missing = symbol
		}
		bits := 0
		signed := false
		if u.Kind == ir.KInt {
			bits = u.Int.Bits()
			signed = u.Int.Signed()
		}
		if u.Kind == ir.KFloat {
			bits = u.FloatBits
		}
		comparable := t.Go != nil && t.Comparable()
		fmt.Fprintf(b, "    table.append(GType(%s, %s, %d, %t, %d, %d, %d, [%s], [%s], [%s], [%s], %t, [%s], %s, [%s])) // %s\n", quote(ir.TypeString(t)), quote(u.Kind.String()), bits, signed, id(u.Elem), id(u.Key), u.Len, strings.Join(fields, ", "), strings.Join(names, ", "), strings.Join(blank, ", "), strings.Join(methods, ", "), comparable, strings.Join(implementations, ", "), missing, strings.Join(nativeMethods, ", "), e.names.Type(t, "T_"))
	}
	if chunks > 0 {
		b.WriteString("}\n\n")
	}
	b.WriteString(missingDeclarations.String())
	fmt.Fprintf(b, "let gTypeRegistration: Void = {\n    var table: [GType] = []\n    table.reserveCapacity(%d)\n", len(e.p.Types.All))
	for i := 0; i < chunks; i++ {
		fmt.Fprintf(b, "    gTypeRows%d(&table)\n", i)
	}
	b.WriteString("    GTypes.table = table\n}()\n")
}

func (e *emitter) constant(c *ir.Const) string {
	if c.Nil {
		return fmt.Sprintf("GTypes.zero(%d)", c.Type.ID)
	}
	u := c.Type.U()
	if u.Kind == ir.KFloat {
		return fmt.Sprintf("GValue.float(%s, %d)", ir.FloatLiteral(c), u.FloatBits)
	}
	switch c.Val.Kind() {
	case constant.Bool:
		return "GValue.bool(" + strconv.FormatBool(constant.BoolVal(c.Val)) + ")"
	case constant.Int:
		v, _ := constant.Uint64Val(c.Val)
		if constant.Sign(c.Val) < 0 {
			n, _ := constant.Int64Val(c.Val)
			v = uint64(n)
		}
		if width := u.Int.Bits(); width < 64 {
			v &= (uint64(1) << width) - 1
		}
		return fmt.Sprintf("GValue.integer(%d, %d, %t)", v, u.Int.Bits(), u.Int.Signed())
	case constant.String:
		s := constant.StringVal(c.Val)
		// swiftc type-checks large byte-array literals slowly, so valid UTF-8
		// uses a string literal, whose utf8 view holds the same bytes.
		if utf8.ValidString(s) {
			return "GValue.text(" + swiftString(s) + ")"
		}
		var bytes []string
		for _, v := range []byte(s) {
			bytes = append(bytes, strconv.Itoa(int(v)))
		}
		return "GValue.string([" + strings.Join(bytes, ",") + "])"
	}
	panic("swift: invalid constant")
}

type fnEmitter struct {
	e      *emitter
	f      *ir.Func
	b      strings.Builder
	labels map[*ir.Block]int
	next   int
	folds  *structure.Folds
	// frame is set when f is a resumable frame rather than a plain function.
	frame bool
	// async is set when f is an async function of the native pause model;
	// defers when it also has deferred calls, run after its body.
	async, defers bool
}

func (fe *fnEmitter) w(format string, a ...any) { fmt.Fprintf(&fe.b, "            "+format+"\n", a...) }
func (fe *fnEmitter) local(l *ir.Local) string {
	if n, ok := fe.folds.Name(l); ok {
		return n
	}
	return fe.e.names.Local(l, "_")
}
func (fe *fnEmitter) val(v ir.Value) string {
	switch v := v.(type) {
	case *ir.Local:
		if s, ok := fe.folds.Use(v); ok {
			return s
		}
		return fe.local(v) + ".value"
	case *ir.Const:
		return fe.e.constant(v)
	case *ir.FuncRef:
		return ".function(" + fe.e.functionValue(v.Func, "") + ")"
	}
	panic("swift operand")
}
func (fe *fnEmitter) values(vs []ir.Value) string {
	var xs []string
	for _, v := range vs {
		xs = append(xs, fe.val(v))
	}
	return "[" + strings.Join(xs, ", ") + "]"
}
func (fe *fnEmitter) results() string {
	var xs []string
	for _, r := range fe.f.Results {
		xs = append(xs, "GCopy("+fe.val(r)+")")
	}
	if !fe.frame && len(xs) == 1 {
		return xs[0]
	}
	if !fe.frame {
		return "(" + strings.Join(xs, ", ") + ")"
	}
	return "[" + strings.Join(xs, ", ") + "]"
}

// plainParams lists a plain function's parameters: the task, one value per
// parameter, and the captured cells of a closure.
func plainParams(f *ir.Func) []string {
	ps := []string{"_ task: GTask"}
	for i := range f.Params {
		ps = append(ps, fmt.Sprintf("_ p%d: GValue", i))
	}
	if len(f.Env) > 0 {
		ps = append(ps, "_ env: [GCell]")
	}
	return ps
}

func plainResults(f *ir.Func) string {
	switch n := len(f.Results); n {
	case 0:
		return ""
	case 1:
		return " -> GValue"
	default:
		return " -> (" + strings.TrimSuffix(strings.Repeat("GValue, ", n), ", ") + ")"
	}
}

// plain reports whether f runs as a plain Swift function: it cannot pause,
// and it has no deferred calls or recover, which the frame scheduler runs.
func plain(f *ir.Func) bool {
	if f.MaySuspend {
		return false
	}
	for _, b := range f.Blocks {
		for _, in := range b.Instrs {
			switch in.(type) {
			case *ir.Defer, *ir.Recover:
				return false
			}
		}
	}
	return true
}

func hasDefer(f *ir.Func) bool {
	for _, b := range f.Blocks {
		for _, in := range b.Instrs {
			if _, ok := in.(*ir.Defer); ok {
				return true
			}
		}
	}
	return false
}

func (e *emitter) function(f *ir.Func) string {
	fe := &fnEmitter{e: e, f: f, labels: map[*ir.Block]int{}, next: len(f.Blocks), frame: !plain(f) && !e.async(f), async: e.async(f)}
	fe.defers = fe.async && hasDefer(f)
	for i, b := range f.Blocks {
		fe.labels[b] = i
	}
	structure.Analyze(f, structure.FoldOptions{Inline: fe.async, PauseAlone: fe.async, Order: structure.LeftToRight}, swiftProbe{fe}, func(fs *structure.Folds) { fe.folds = fs })
	sym := e.names.Symbol(f.Sym)
	if fe.async {
		fmt.Fprintf(&fe.b, "\n%sfunc %s_a(%s) async throws%s {\n", position(f.Pos), sym, strings.Join(plainParams(f), ", "), plainResults(f))
	} else if fe.frame {
		fmt.Fprintf(&fe.b, "\n%sfunc %s(_ args: [GValue], _ env: [GCell]) -> GFrame {\n    _ = gTypeRegistration\n", position(f.Pos), sym)
	} else {
		fmt.Fprintf(&fe.b, "\n%sfunc %s_p(%s) throws%s {\n", position(f.Pos), sym, strings.Join(plainParams(f), ", "), plainResults(f))
	}
	params := map[*ir.Local]int{}
	for i, l := range f.Params {
		params[l] = i
	}
	captures := map[*ir.Local]int{}
	for i, l := range f.Env {
		captures[l] = i
	}
	rebound := map[*ir.Local]bool{}
	for _, block := range f.Blocks {
		for _, instruction := range block.Instrs {
			switch in := instruction.(type) {
			case *ir.DeclVar:
				rebound[in.L] = true
			case *ir.Rebind:
				rebound[in.L] = true
			}
		}
	}
	var roots []string
	for _, l := range f.Locals {
		if fe.folds.Temp(l) {
			continue
		}
		roots = append(roots, ".pointer("+fe.local(l)+")")
		binding := "let"
		if rebound[l] {
			binding = "var"
		}
		if i, ok := captures[l]; ok {
			fmt.Fprintf(&fe.b, "    %s %s = env[%d]\n", binding, fe.local(l), i)
		} else if i, ok := params[l]; ok {
			arg := fmt.Sprintf("args[%d]", i)
			if !fe.frame {
				arg = fmt.Sprintf("p%d", i)
			}
			fmt.Fprintf(&fe.b, "    %s %s = GCell(%s)\n", binding, fe.local(l), arg)
		} else {
			fmt.Fprintf(&fe.b, "    %s %s = GCell(GTypes.zero(%d))\n", binding, fe.local(l), l.Type.ID)
		}
	}
	body := structure.Build(f, structure.Options{Inline: fe.async, Empty: func(b *ir.Block) bool { return fe.folds.Empty(b, fe.async) }})
	if fe.async {
		fe.asyncBody(body, sym, roots)
		return structure.Resolve(fe.b.String())
	}
	if !fe.frame {
		syn := swiftSyntax
		syn.DispatchOpen, syn.SetPC = []string{"var pc = 0", "%swhile true {", "\tswitch pc {"}, "pc = %d"
		syn.Default = []string{`default: throw GFault("invalid block")`}
		fe.b.WriteString(structure.Print(body, syn, swiftEmitter{fe}, 1))
		fmt.Fprintf(&fe.b, "}\n%sfunc %s(_ args: [GValue], _ env: [GCell]) -> GFrame {\n    _ = gTypeRegistration\n    return GFrame.plain(%d) { task in %s }\n}\n", position(f.Pos), sym, f.ID, arrayCall(f, "try "+sym+"_p"))
		return structure.Resolve(fe.b.String())
	}
	fmt.Fprintf(&fe.b, "    let frame = GFrame(%d)\n    frame.dynamicResults = true\n    frame.roots = { [%s] }\n    frame.results = { %s }\n    frame.step = { frame, task in\n", f.ID, strings.Join(roots, ", "), fe.results())
	if body.Dispatch == nil {
		fe.b.WriteString("        task.owner.safepoint()\n")
	}
	syn := swiftSyntax
	syn.LoopStart = "task.owner.safepoint()"
	fe.b.WriteString(structure.Print(body, syn, swiftEmitter{fe}, 2))
	fe.b.WriteString("    }\n    return frame\n}\n")
	return structure.Resolve(fe.b.String())
}

// arrayCall calls fn with f's parameters taken from args and env, and
// returns its results as an array.
func arrayCall(f *ir.Func, fn string) string {
	as := []string{"task"}
	for i := range f.Params {
		as = append(as, fmt.Sprintf("args[%d]", i))
	}
	if len(f.Env) > 0 {
		as = append(as, "env")
	}
	call := fn + "(" + strings.Join(as, ", ") + ")"
	switch len(f.Results) {
	case 0:
		return call + "; return []"
	case 1:
		return "[" + call + "]"
	}
	var rs []string
	for i := range f.Results {
		rs = append(rs, fmt.Sprintf("r.%d", i))
	}
	return "let r = " + call + "; return [" + strings.Join(rs, ", ") + "]"
}

// asyncBody writes the body of an async function, then its frame-convention
// adapters: sym_n takes and returns arrays, and sym starts a native frame.
func (fe *fnEmitter) asyncBody(body *structure.Body, sym string, roots []string) {
	f := fe.f
	if len(roots) > 0 || fe.defers {
		fmt.Fprintf(&fe.b, "    let frame = task.enter(%d) { [%s] }\n    defer { task.leave(frame) }\n", f.ID, strings.Join(roots, ", "))
	}
	// The body is a local function: it borrows the cells it captures, while
	// cells the async function owned would be released on every throwing
	// path of every resumption, which makes large bodies very slow to build.
	fmt.Fprintf(&fe.b, "    func run() async throws%s {\n        task.owner.safepoint()\n", plainResults(f))
	syn := swiftSyntax
	syn.DispatchOpen, syn.SetPC = []string{"var pc = 0", "%swhile true {", "\ttask.owner.safepoint()", "\tswitch pc {"}, "pc = %d"
	syn.Default = []string{`default: throw GFault("invalid block")`}
	syn.LoopStart = "task.owner.safepoint()"
	if fe.defers {
		fe.b.WriteString("        body: do {\n")
		fe.b.WriteString(structure.Print(body, syn, swiftEmitter{fe}, 3))
		fe.b.WriteString("        } catch let p as GPanic {\n            frame.panic = p\n        }\n        try await task.runDefers(frame)\n")
		if len(f.Results) > 0 {
			fmt.Fprintf(&fe.b, "        return %s\n", fe.results())
		}
	} else {
		fe.b.WriteString(structure.Print(body, syn, swiftEmitter{fe}, 2))
	}
	ret := "return "
	if len(f.Results) == 0 {
		ret = ""
	}
	fmt.Fprintf(&fe.b, "    }\n    %stry await run()\n", ret)
	fmt.Fprintf(&fe.b, "}\nfunc %s_n(_ task: GTask, _ args: [GValue], _ env: [GCell]) async throws -> [GValue] { %s }\n", sym, arrayCall(f, "try await "+sym+"_a"))
	fmt.Fprintf(&fe.b, "%sfunc %s(_ args: [GValue], _ env: [GCell]) -> GFrame {\n    _ = gTypeRegistration\n    return GFrame.native(%d, args, env, %s_n)\n}\n", position(f.Pos), sym, f.ID, sym)
}

var swiftSyntax = structure.Syntax{
	Indent: "    ", If: "if %s {", ElseIf: "} else if %s {", Else: "} else {", End: "}",
	Loop: "while true {", While: "while %s {", Block: "%s: do {", LoopLabel: "%s: ", LabelName: "l%d",
	Break: "break", BreakTo: "break %s", Continue: "continue", ContinueTo: "continue %s",
	Not: "!%s", And: "try %s && %s", Or: "try %s || %s",
	DispatchOpen: []string{"%swhile true {", "\ttask.owner.safepoint()", "\tswitch frame.pc {"}, CaseOpen: "case %d:",
	Default: []string{`default: throw GFault("invalid frame PC")`}, DispatchClose: []string{"\t}", "}"}, SetPC: "frame.pc = %d",
}

func (fe *fnEmitter) capture(fn func()) string {
	saved := fe.b
	fe.b = strings.Builder{}
	fn()
	out := fe.b.String()
	fe.b = saved
	return out
}

// swiftEmitter prints the target-specific statements of a structured body.
type swiftEmitter struct{ fe *fnEmitter }

func (x swiftEmitter) Code(b *ir.Block) string {
	fe := x.fe
	return fe.capture(func() {
		if b.ResumeOf != nil && !fe.async {
			fe.resumeInstr(b.ResumeOf)
		}
		for _, in := range b.Instrs {
			if !fe.folds.Folded(in) {
				fe.mark(in.Position())
				fe.instr(in)
			}
		}
	})
}

func (x swiftEmitter) Leaf(b *ir.Block, t ir.Terminator) string {
	return x.fe.capture(func() {
		x.fe.mark(t.Position())
		x.fe.term(t)
	})
}

func (x swiftEmitter) Pause(p *ir.Pause) string {
	if x.fe.folds.FoldedPause(p) {
		return ""
	}
	return x.fe.capture(func() {
		x.fe.mark(p.Position())
		x.fe.nativePause(p)
	})
}
func (x swiftEmitter) Cond(v ir.Value) string { return x.fe.val(v) + ".boolValue" }
func (x swiftEmitter) Mark(pos token.Pos) string {
	if !pos.IsValid() {
		return ""
	}
	return fmt.Sprintf("%s%d\x00", marker, int(pos))
}

// swiftProbe renders statements for fold analysis.
type swiftProbe struct{ fe *fnEmitter }

func (x swiftProbe) Instr(in ir.Instr) string { return x.fe.capture(func() { x.fe.instr(in) }) }
func (x swiftProbe) Pause(p *ir.Pause) string { return x.fe.capture(func() { x.fe.nativePause(p) }) }
func (x swiftProbe) Cond(v ir.Value) string   { return x.fe.val(v) }
func (x swiftProbe) Leaf(b *ir.Block, t ir.Terminator) string {
	return x.fe.capture(func() { x.fe.term(t) })
}
func (x swiftProbe) Assign(l *ir.Local) (string, string) { return x.fe.val(l) + " = ", "" }

const marker = "\x00@"

func (fe *fnEmitter) mark(p token.Pos) {
	if p.IsValid() {
		fe.b.WriteString(fmt.Sprintf("%s%d\x00", marker, int(p)))
	}
}
func (e *emitter) markers(src string) (string, map[int]token.Position) {
	m := map[int]token.Position{}
	ls := strings.Split(src, "\n")
	for i, l := range ls {
		for {
			j := strings.Index(l, marker)
			if j < 0 {
				break
			}
			k := strings.IndexByte(l[j+len(marker):], 0)
			n, _ := strconv.Atoi(l[j+len(marker) : j+len(marker)+k])
			l = l[:j] + l[j+len(marker)+k+1:]
			if e.p.Fset != nil {
				if p := e.p.Fset.Position(token.Pos(n)); p.IsValid() {
					m[i+1] = p
				}
			}
		}
		ls[i] = l
	}
	return strings.Join(ls, "\n"), m
}

func (fe *fnEmitter) assignResults(ds []*ir.Local) {
	for i, d := range ds {
		if d != nil {
			fe.w("%s = task.rv[%d]", fe.val(d), i)
		}
	}
}
func (fe *fnEmitter) call(c *ir.Call) string {
	args := fe.values(c.Args)
	switch c.Kind {
	case ir.CallStatic:
		return fmt.Sprintf("%s(%s, [])", fe.e.names.Symbol(c.Func.Sym), args)
	case ir.CallValue:
		return fmt.Sprintf("try GFunction.start(%s, %s)", fe.val(c.Fn), args)
	case ir.CallInterface:
		return fmt.Sprintf("try GInterface.start(%s, %s, %s)", fe.val(c.Recv), quote(c.Method), args)
	case ir.CallExtern:
		fe.e.use(c.Extern.Contract)
		symbol := fe.e.symbols[c.Extern.Contract]
		if symbol == "" {
			symbol = "GNative." + strings.ReplaceAll(c.Extern.Contract, ".", "_")
		}
		return fmt.Sprintf("%s(%s)", symbol, args)
	}
	panic("swift call")
}

// syncCall calls a function that cannot pause and returns its results: a
// plain function directly, any other through a synchronous frame run.
func (fe *fnEmitter) syncCall(c *ir.Call) string {
	if c.Kind == ir.CallStatic && plain(c.Func) {
		as := []string{"task"}
		for _, a := range c.Args {
			as = append(as, fe.val(a))
		}
		return fmt.Sprintf("try %s_p(%s)", fe.e.names.Symbol(c.Func.Sym), strings.Join(as, ", "))
	}
	return "try task.owner.callSync(task, " + fe.call(c) + ")"
}

func (fe *fnEmitter) root(p *ir.Place) string {
	switch r := p.Root.(type) {
	case ir.LocalRoot:
		return fe.local(r.Local)
	case ir.GlobalRoot:
		return fe.e.names.Symbol(r.Global.Sym)
	case ir.DerefRoot:
		return "try GPointer.cell(" + fe.val(r.Ptr) + ")"
	case ir.SliceRoot:
		fe.e.use("core.slice.index")
		return "try GSlice.cell(" + fe.val(r.Slice) + ", " + fe.val(r.Index) + ")"
	case ir.ValueRoot:
		return "GCell(" + fe.val(r.Value) + ")"
	}
	panic("swift place")
}
func (fe *fnEmitter) place(p *ir.Place) string {
	s := fe.root(p)
	for _, pr := range p.Path {
		if pr.Index != nil {
			s = "try GAggregate.element(" + s + ".value, " + fe.val(pr.Index) + ")"
		} else {
			s = fmt.Sprintf("try GAggregate.field(%s.value, %d)", s, pr.Field)
		}
	}
	return "(" + s + ")"
}

func (fe *fnEmitter) instr(in ir.Instr) {
	e := fe.e
	w := fe.w
	switch i := in.(type) {
	case *ir.DeclVar:
		z := fmt.Sprintf("GTypes.zero(%d)", i.L.Type.ID)
		if i.Init != nil {
			z = fe.val(i.Init)
		}
		w("%s = GCell(%s)", fe.local(i.L), z)
	case *ir.Assign:
		w("%s = %s", fe.val(i.Dst), fe.val(i.Src))
	case *ir.Zero:
		w("%s = GTypes.zero(%d)", fe.val(i.Dst), i.Dst.Type.ID)
	case *ir.Load:
		w("%s = GCopy(%s.value)", fe.val(i.Dst), fe.place(i.Place))
	case *ir.Store:
		w("GStore(%s, %s)", fe.place(i.Place), fe.val(i.V))
	case *ir.AddrOf:
		w("%s = .pointer(%s)", fe.val(i.Dst), fe.place(i.Place))
	case *ir.New:
		w("%s = .pointer(GCell(GTypes.zero(%d)))", fe.val(i.Dst), i.Dst.Type.U().Elem.ID)
	case *ir.UnOp:
		op := map[ir.UnOpKind]string{ir.Neg: "neg", ir.Not: "not", ir.BitNot: "bitnot"}[i.Op]
		if i.Op != ir.Not {
			kind := "integer"
			if i.Dst.Type.U().Kind == ir.KFloat {
				kind = "float"
			}
			c := op
			if c == "bitnot" {
				c = "not"
			}
			e.use("core." + kind + "." + c)
		}
		w("%s = try GNumeric.unary(%s, %s)", fe.val(i.Dst), quote(op), fe.val(i.X))
	case *ir.BinOp:
		u := i.X.IRType().U()
		if !i.Op.IsComparison() {
			kind := "integer"
			if u.Kind == ir.KFloat {
				kind = "float"
			}
			if u.Kind == ir.KString {
				kind = "string"
			}
			op := map[ir.BinOpKind]string{ir.Add: "add", ir.Sub: "sub", ir.Mul: "mul", ir.Div: "div", ir.Rem: "rem", ir.And: "and", ir.Or: "or", ir.Xor: "xor", ir.AndNot: "andnot", ir.Shl: "shl", ir.Shr: "shr", ir.Min: "min", ir.Max: "max"}[i.Op]
			if kind == "string" {
				op = "concat"
			}
			if (i.Op == ir.Min || i.Op == ir.Max) && kind != "float" {
				e.use("core." + kind + ".compare")
			} else {
				e.use("core." + kind + "." + op)
			}
		}
		w("%s = try GNumeric.binary(%s, %s, %s)", fe.val(i.Dst), quote(i.Op.String()), fe.val(i.X), fe.val(i.Y))
	case *ir.Convert:
		cs := map[ir.ConvKind]string{ir.ConvInt: "core.integer.convert", ir.ConvFloat: "core.float.convert", ir.ConvIntToString: "core.string.from_rune", ir.ConvStringToBytes: "core.string.to_bytes", ir.ConvBytesToString: "core.string.from_bytes", ir.ConvStringToRunes: "core.string.to_runes", ir.ConvRunesToString: "core.string.from_runes", ir.ConvSliceToArray: "core.slice.to_array"}
		if c := cs[i.Kind]; c != "" {
			e.use(c)
		}
		w("%s = try GConvert(%s, %d, %d)", fe.val(i.Dst), fe.val(i.X), i.Dst.Type.ID, i.Kind)
	case *ir.MakeInterface:
		w("%s = .interface(GInterface(%d, GCopy(%s)))", fe.val(i.Dst), i.X.IRType().ID, fe.val(i.X))
	case *ir.TypeAssert:
		n := fe.next
		fe.next++
		w("let asserted%d = try GInterface.assertType(%s, %d, %d, %t)", n, fe.val(i.X), i.T.ID, i.X.IRType().ID, i.Ok != nil)
		if i.Dst != nil {
			w("%s = asserted%d.0", fe.val(i.Dst), n)
		}
		if i.Ok != nil {
			w("%s = .bool(asserted%d.1)", fe.val(i.Ok), n)
		}
	case *ir.Call:
		fe.assignCall(fe.syncCall(i), i.Kind == ir.CallStatic && plain(i.Func), i.Dsts)
	case *ir.MakeClosure:
		var env []string
		for _, l := range i.Env {
			env = append(env, fe.local(l))
		}
		w("%s = .function(%s)", fe.val(i.Dst), e.functionValue(i.Func, "["+strings.Join(env, ", ")+"]"))
	case *ir.MakeBound:
		native := ""
		if e.async(i.Func) {
			native = ", native: " + e.names.Symbol(i.Func.Sym) + "_n"
		}
		w("%s = .function(GFunction.bound(%d, %s, %s%s))", fe.val(i.Dst), i.Func.ID, e.names.Symbol(i.Func.Sym), fe.val(i.Recv), native)
	case *ir.MakeIfaceBound:
		w("%s = try GInterface.bound(%s, %s)", fe.val(i.Dst), fe.val(i.Recv), quote(i.Method))
	case *ir.Len:
		w("%s = try GLength(%s, false)", fe.val(i.Dst), fe.val(i.X))
	case *ir.Cap:
		w("%s = try GLength(%s, true)", fe.val(i.Dst), fe.val(i.X))
	case *ir.MakeSlice:
		e.use("core.slice.make")
		w("%s = try GSlice.make(%s, %s, %d)", fe.val(i.Dst), fe.val(i.Len), fe.val(i.Cap), i.Dst.Type.U().Elem.ID)
	case *ir.MakeMap:
		e.use("core.map.make")
		w("%s = .map(GMap(%d, %d))", fe.val(i.Dst), i.Dst.Type.U().Key.ID, i.Dst.Type.U().Elem.ID)
	case *ir.Append:
		e.use("core.slice.append")
		vs := fe.values(i.Elems)
		if i.Spread != nil {
			vs = fe.val(i.Spread)
		}
		w("%s = try GSlice.append(%s, %s)", fe.val(i.Dst), fe.val(i.S), vs)
	case *ir.Copy:
		e.use("core.slice.copy")
		w("%s = try GSlice.copy(%s, %s)", fe.val(i.N), fe.val(i.Dst), fe.val(i.Src))
	case *ir.IndexString:
		e.use("core.string.index")
		w("%s = try GString.index(%s, %s)", fe.val(i.Dst), fe.val(i.S), fe.val(i.I))
	case *ir.SliceOp:
		c := "core.slice.slice"
		if i.X.IRType().U().Kind == ir.KString {
			c = "core.string.slice"
		}
		e.use(c)
		opt := func(v ir.Value) string {
			if v == nil {
				return "nil"
			}
			return fe.val(v)
		}
		w("%s = try GSlice.reslice(%s, %s, %s, %s)", fe.val(i.Dst), fe.val(i.X), opt(i.Lo), opt(i.Hi), opt(i.Max))
	case *ir.MapLookup:
		e.use("core.map.lookup")
		n := fe.next
		fe.next++
		w("let looked%d = try GMap.lookup(%s, %s, %d)", n, fe.val(i.M), fe.val(i.K), i.M.IRType().U().Elem.ID)
		if i.Dst != nil {
			w("%s = looked%d.0", fe.val(i.Dst), n)
		}
		if i.Ok != nil {
			w("%s = .bool(looked%d.1)", fe.val(i.Ok), n)
		}
	case *ir.MapStore:
		e.use("core.map.store")
		w("try GMap.store(%s, %s, %s)", fe.val(i.M), fe.val(i.K), fe.val(i.V))
	case *ir.MapDelete:
		e.use("core.map.delete")
		w("try GMap.delete(%s, %s)", fe.val(i.M), fe.val(i.K))
	case *ir.Clear:
		c := "core.slice.clear"
		if i.X.IRType().U().Kind == ir.KMap {
			c = "core.map.clear"
		}
		e.use(c)
		w("try GClear(%s)", fe.val(i.X))
	case *ir.DecodeRune:
		e.use("core.string.decode_rune")
		n := fe.next
		fe.next++
		w("let rune%d = try GString.decode(%s, %s)", n, fe.val(i.S), fe.val(i.I))
		if i.Rune != nil {
			w("%s = rune%d.0", fe.val(i.Rune), n)
		}
		if i.Width != nil {
			w("%s = rune%d.1", fe.val(i.Width), n)
		}
	case *ir.MapIterInit:
		e.use("core.map.iterate")
		w("%s = .iterator(GMapIterator(%s))", fe.val(i.Iter), fe.val(i.M))
	case *ir.MapIterNext:
		n := fe.next
		fe.next++
		w("let iterated%d = try GMapIterator.next(%s)", n, fe.val(i.Iter))
		w("%s = .bool(iterated%d.0)", fe.val(i.Ok), n)
		if i.Key != nil {
			w("%s = iterated%d.1", fe.val(i.Key), n)
		}
		if i.Val != nil {
			w("%s = iterated%d.2", fe.val(i.Val), n)
		}
	case *ir.Print:
		e.use("core.print")
		w("try GPrint(%s, %t)", fe.values(i.Args), i.Newline)
	case *ir.Defer:
		w("frame.defers.append(%s)", fe.call(i.Call))
	case *ir.Recover:
		w("%s = task.recover(%d)", fe.val(i.Dst), fe.f.ID)
	case *ir.Rebind:
		w("%s = GCell(GCopy(%s))", fe.local(i.L), fe.val(i.L))
	case *ir.BoxParam: // All locals already have stable cells.
	case *ir.MakeChan:
		e.use("core.chan.make")
		w("%s = try GChannel.make(%s, %d)", fe.val(i.Dst), fe.val(i.Size), i.Dst.Type.U().Elem.ID)
	case *ir.Close:
		e.use("core.chan.close")
		w("try GChannel.close(%s, task.owner)", fe.val(i.Ch))
	case *ir.Go:
		e.use("core.task.spawn")
		w("task.owner.spawn(%s)", fe.call(i.Call))
	default:
		panic(fmt.Sprintf("swift instruction %T", in))
	}
}

// assignCall assigns a call's results: typed calls return them as a value
// or a tuple, others as an array.
func (fe *fnEmitter) assignCall(call string, typed bool, dsts []*ir.Local) {
	index := func(k int) string {
		if typed {
			if len(dsts) == 1 {
				return ""
			}
			return fmt.Sprintf(".%d", k)
		}
		return fmt.Sprintf("[%d]", k)
	}
	switch {
	case len(dsts) == 0:
		fe.w("_ = %s", call)
	case len(dsts) == 1 && dsts[0] != nil:
		fe.w("%s = %s%s", fe.val(dsts[0]), call, index(0))
	default:
		n := fe.next
		fe.next++
		fe.w("let results%d = %s", n, call)
		for k, d := range dsts {
			if d != nil {
				fe.w("%s = results%d%s", fe.val(d), n, index(k))
			}
		}
	}
}

// nativePause emits a pause point of an async function: a suspending callee
// is awaited for its results, and a pause primitive either completes at once
// or waits for the scheduler to choose the task again, leaving its results
// in task.rv.
func (fe *fnEmitter) nativePause(p *ir.Pause) {
	switch i := p.Op.(type) {
	case *ir.Call:
		switch i.Kind {
		case ir.CallStatic:
			if !i.Func.MaySuspend {
				fe.assignCall(fe.syncCall(i), plain(i.Func), i.Dsts)
				return
			}
			as := []string{"task"}
			for _, a := range i.Args {
				as = append(as, fe.val(a))
			}
			fe.assignCall(fmt.Sprintf("try await %s_a(%s)", fe.e.names.Symbol(i.Func.Sym), strings.Join(as, ", ")), true, i.Dsts)
		case ir.CallValue:
			fe.assignCall(fmt.Sprintf("try await GFunction.call(task, %s, %s)", fe.val(i.Fn), fe.values(i.Args)), false, i.Dsts)
		case ir.CallInterface:
			fe.assignCall(fmt.Sprintf("try await GInterface.call(task, %s, %s, %s)", fe.val(i.Recv), quote(i.Method), fe.values(i.Args)), false, i.Dsts)
		case ir.CallExtern:
			fe.assignCall("try await task.call("+fe.call(i)+")", false, i.Dsts)
		}
	case *ir.Send:
		fe.e.use("core.chan.send")
		fe.w("try await task.pause(GChannel.send(%s, %s, task))", fe.val(i.Ch), fe.val(i.V))
	case *ir.Recv:
		fe.e.use("core.chan.recv")
		fe.w("try await task.pause(GChannel.receive(%s, task))", fe.val(i.Ch))
		fe.resumeInstr(i)
	case *ir.Select:
		fe.e.use("core.select")
		fe.w("try await task.pause(GChannel.select([%s], %t, task))", strings.Join(fe.selectCases(i), ", "), i.Default)
		fe.resumeInstr(i)
	default:
		panic(fmt.Sprintf("swift: pause on %T", p.Op))
	}
}

func (fe *fnEmitter) selectCases(i *ir.Select) []string {
	var cs []string
	for _, c := range i.Cases {
		v := ".nilValue"
		if c.Send {
			v = fe.val(c.V)
		}
		cs = append(cs, fmt.Sprintf("GSelectCase(%s, %t, %s)", fe.val(c.Ch), c.Send, v))
	}
	return cs
}

func (fe *fnEmitter) resumeInstr(in ir.Instr) {
	switch i := in.(type) {
	case *ir.Call:
		fe.assignResults(i.Dsts)
	case *ir.Recv:
		if i.Dst != nil {
			fe.w("%s = task.rv[0]", fe.val(i.Dst))
		}
		if i.Ok != nil {
			fe.w("%s = task.rv[1]", fe.val(i.Ok))
		}
	case *ir.Select:
		fe.w("%s = task.rv[0]", fe.val(i.Index))
		for k, c := range i.Cases {
			if c.Send {
				continue
			}
			fe.w("if task.rv[0].intValue == %d {", k)
			if c.Dst != nil {
				fe.w("    %s = task.rv[1]", fe.val(c.Dst))
			}
			if c.Ok != nil {
				fe.w("    %s = task.rv[2]", fe.val(c.Ok))
			}
			fe.w("}")
		}
	}
}
func (fe *fnEmitter) term(t ir.Terminator) {
	switch t := t.(type) {
	case *ir.Return:
		if fe.defers {
			fe.w("break body")
			return
		}
		if !fe.frame {
			fe.w("return %s", fe.results())
			return
		}
		fe.w("return .complete(%s)", fe.results())
	case *ir.Panic:
		fe.w("throw GPanic.source(%s)", fe.val(t.X))
	case *ir.PanicRuntime:
		fe.w("throw GPanic.plain(%s)", quote(t.Msg))
	case *ir.Unreachable:
		fe.w("throw GFault(\"unreachable\")")
	case *ir.Pause:
		fe.w("frame.pc = %d", fe.labels[t.Next])
		switch i := t.Op.(type) {
		case *ir.Call:
			fe.w("return .call(%s)", fe.call(i))
		case *ir.Send:
			fe.e.use("core.chan.send")
			fe.w("return try GChannel.send(%s, %s, task)", fe.val(i.Ch), fe.val(i.V))
		case *ir.Recv:
			fe.e.use("core.chan.recv")
			fe.w("return try GChannel.receive(%s, task)", fe.val(i.Ch))
		case *ir.Select:
			fe.e.use("core.select")
			fe.w("return try GChannel.select([%s], %t, task)", strings.Join(fe.selectCases(i), ", "), i.Default)
		}
	default:
		panic(fmt.Sprintf("swift term %T", t))
	}
}
