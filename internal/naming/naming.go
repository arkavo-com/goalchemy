// Package naming assigns deterministic private identifiers to emitted IR.
// It never changes source identities, public APIs, or the shared IR.
package naming

import (
	"fmt"
	"go/token"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/eugenioenko/goalchemy/internal/ir"
)

// Prefix starts every compiler-generated identifier. Source names never
// start with it: a source name that does is renamed.
const Prefix = "_"

// Mode selects how private identifiers are spelled.
type Mode string

const (
	// Source keeps source identifiers and renames only on a collision.
	Source Mode = "source"
	// Readable decorates every name with its category and identity.
	Readable Mode = "readable"
	// Compact uses short category letters and identities.
	Compact Mode = "compact"
)

// Policies under source names: rename on a collision, or report it.
const (
	Rename = "rename"
	Fail   = "fail"
)

// ParseMode accepts a mode name; the empty string is the default, Source.
func ParseMode(s string) (Mode, error) {
	switch Mode(s) {
	case "", Source:
		return Source, nil
	case Readable, Compact:
		return Mode(s), nil
	}
	return "", fmt.Errorf("unknown names mode %q (choose source, readable or compact)", s)
}

// ParsePolicy accepts a collision policy; the empty string is Rename.
func ParsePolicy(s string) (string, error) {
	switch s {
	case "", Rename:
		return Rename, nil
	case Fail:
		return Fail, nil
	}
	return "", fmt.Errorf("unknown name collision policy %q (choose rename or fail)", s)
}

func modeOf(p *ir.Program) Mode {
	if p == nil {
		return Source
	}
	m, err := ParseMode(p.Names)
	if err != nil {
		return Source
	}
	return m
}

// Rules are a target's constraints on source names.
type Rules struct {
	// Reserved lists names a source identifier never takes on the target:
	// keywords, predeclared and builtin names, import aliases, fixed runtime
	// names and public library names.
	Reserved []string
	// Members lists names a source local never takes because frames store
	// locals as fields next to these runtime members.
	Members []string
	// TypePrefix is the category prefix of a type's own declaration. Under
	// source names that declaration keeps the source type name.
	TypePrefix string
	// Lower starts every module-level source name with a lowercase letter,
	// for targets where an initial capital exports a name.
	Lower bool
}

// Collision describes a source name that could not be kept.
type Collision struct {
	Pos     token.Pos
	Name    string
	Emitted string
	// Reason says why: "reserved word", "package clash", "shadowed local",
	// "duplicate name" or "not a plain identifier".
	Reason string
	// Other is the earlier declaration holding the name, when there is one.
	Other     token.Pos
	OtherName string
}

// Identifier encodes a source fragment using ASCII identifier characters.
// Callers add a category prefix and an identity suffix, so reserved words,
// source punctuation, and names from different scopes cannot collide.
func Identifier(source string) string {
	var b strings.Builder
	for _, r := range source {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		case r == '.', r == '/', r == '-':
			b.WriteByte('_')
		default:
			fmt.Fprintf(&b, "_u%x_", r)
		}
	}
	return b.String()
}

// plain reports whether s can be emitted unchanged: ASCII identifier
// characters, outside the generated namespace, not ending in an identity
// suffix.
func plain(s string) bool {
	if s == "" || strings.HasPrefix(s, Prefix) || s[0] >= '0' && s[0] <= '9' {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

// sourceIdent spells a source name outside the generated namespace.
func sourceIdent(s string) string {
	id := Identifier(s)
	if id == "" || strings.HasPrefix(id, Prefix) || id[0] >= '0' && id[0] <= '9' {
		id = "x" + id
	}
	return id
}

type owner struct {
	pos  token.Pos
	name string
}

// Names is an immutable naming index for one emission, except for the memo
// of generated names, which are allocated on first use.
type Names struct {
	p          *ir.Program
	mode       Mode
	rules      Rules
	reserved   map[string]bool
	globals    map[string]bool
	members    map[string]bool
	symbols    map[string]string
	types      map[*ir.Type]*ir.Type
	typeNames  map[*ir.Type]string
	locals     map[*ir.Local]string
	used       map[string]owner
	generated  map[string]string
	collisions []Collision
}

// New indexes private symbols and canonical representatives without modifying p.
func New(p *ir.Program, rules Rules) *Names {
	n := &Names{p: p, mode: modeOf(p), rules: rules, reserved: map[string]bool{}, globals: map[string]bool{}, members: map[string]bool{},
		symbols: map[string]string{}, types: map[*ir.Type]*ir.Type{}, typeNames: map[*ir.Type]string{},
		locals: map[*ir.Local]string{}, used: map[string]owner{}, generated: map[string]string{}}
	for _, r := range rules.Reserved {
		n.reserved[r] = true
	}
	if p != nil {
		for _, r := range p.ReservedNames {
			n.reserved[r] = true
		}
		for _, r := range p.ReservedGlobals {
			n.globals[r] = true
		}
	}
	for _, m := range rules.Members {
		n.members[m] = true
	}
	if p == nil {
		return n
	}
	if p.Types != nil {
		for _, t := range p.Types.All {
			if t.Kind == ir.KNamed {
				u := t.U()
				old := n.types[u]
				if old == nil || t.Name+"\x00"+t.Pkg < old.Name+"\x00"+old.Pkg {
					n.types[u] = t
				}
			}
		}
	}
	if n.mode == Source {
		n.assignModule()
		for _, f := range p.Funcs {
			n.assignLocals(f)
		}
		return n
	}
	for _, f := range p.Funcs {
		n.symbols[f.Sym] = function(n.mode, f)
	}
	for _, g := range p.Globals {
		n.symbols[g.Sym] = global(n.mode, g)
	}
	return n
}

// Mode reports the naming mode of this emission.
func (n *Names) Mode() Mode { return n.mode }

// Collisions lists the source names that were renamed, in source order.
func (n *Names) Collisions() []Collision {
	out := append([]Collision(nil), n.collisions...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Pos < out[j].Pos })
	return out
}

func (n *Names) taken(name string) bool {
	_, used := n.used[name]
	return used || n.reserved[name] || n.globals[name]
}

func claim(want string, taken func(string) bool) string {
	name := want
	for i := 2; taken(name); i++ {
		name = want + "_" + strconv.Itoa(i)
	}
	return name
}

type declaration struct {
	key  any
	pos  token.Pos
	pkg  string
	name string
	// quiet declarations are renamed without a report: entry points and
	// package initializers, which the source never names.
	quiet bool
}

func (n *Names) assignModule() {
	p := n.p
	var decls []declaration
	if p.Types != nil {
		for _, t := range p.Types.All {
			if sourceType(t) {
				decls = append(decls, declaration{key: t, pkg: t.Pkg, name: typeBase(t)})
			}
		}
	}
	for _, g := range p.Globals {
		if g.Name != "" {
			decls = append(decls, declaration{key: g, pos: g.Pos, pkg: g.Pkg, name: g.Name})
		}
	}
	for _, f := range p.Funcs {
		if sourceFunc(f) {
			base := funcBase(f.Name)
			quiet := f == p.Main || base == "init" && f.MethodID == ""
			decls = append(decls, declaration{key: f, pos: f.Pos, pkg: f.Pkg, name: base, quiet: quiet})
		}
	}
	spell := func(s string) string {
		if n.rules.Lower {
			return LowerInitial(s)
		}
		return s
	}
	sort.SliceStable(decls, func(i, j int) bool {
		return !lowered(spell, decls[i].name) && lowered(spell, decls[j].name)
	})
	packages := map[string]map[string]bool{}
	for _, d := range decls {
		id := spell(sourceIdent(d.name))
		if packages[id] == nil {
			packages[id] = map[string]bool{}
		}
		packages[id][d.pkg] = true
	}
	for _, d := range decls {
		id := sourceIdent(d.name)
		reason := ""
		if id != d.name || !plain(d.name) {
			reason = "not a plain identifier"
		}
		quiet := d.quiet || lowered(spell, d.name)
		id = spell(id)
		if len(packages[id]) > 1 {
			id = spell(sourceIdent(path.Base(d.pkg)) + "_" + id)
			reason = "package clash"
		}
		name := claim(id, n.taken)
		if (name != id || reason != "") && !quiet {
			c := Collision{Pos: d.pos, Name: d.name, Emitted: name, Reason: reason}
			if o, ok := n.used[id]; ok {
				c.Reason, c.Other, c.OtherName = "duplicate name", o.pos, o.name
			} else if n.reserved[id] || n.globals[id] {
				c.Reason = "reserved word"
			}
			n.collisions = append(n.collisions, c)
		}
		n.used[name] = owner{d.pos, d.name}
		switch k := d.key.(type) {
		case *ir.Type:
			n.typeNames[k] = name
		case *ir.Global:
			n.symbols[k.Sym] = name
		case *ir.Func:
			n.symbols[k.Sym] = name
		}
	}
	for _, g := range p.Globals {
		if _, ok := n.symbols[g.Sym]; !ok {
			n.symbols[g.Sym] = n.gen("global\x00"+g.Sym, Prefix+Identifier(flatten(g.Sym)))
		}
	}
	for _, f := range p.Funcs {
		if _, ok := n.symbols[f.Sym]; !ok {
			base := flatten(funcBase(f.Name))
			if base == "" {
				base = flatten(f.Sym)
			}
			n.symbols[f.Sym] = n.gen("func\x00"+f.Sym, Prefix+Identifier(base))
		}
	}
}

func lowered(spell func(string) string, name string) bool {
	id := sourceIdent(name)
	return spell(id) != id
}

// LowerInitial lowercases the leading capitals of s, keeping the last one
// of a run that starts a following word: ErrMissing becomes errMissing, UTC
// utc and HTTPClient httpClient.
func LowerInitial(s string) string {
	i := 0
	for i < len(s) && s[i] >= 'A' && s[i] <= 'Z' {
		i++
	}
	if i > 1 && i < len(s) && s[i] >= 'a' && s[i] <= 'z' {
		i--
	}
	return strings.ToLower(s[:i]) + s[i:]
}

// sourceType reports whether t is a type the source declares by name.
func sourceType(t *ir.Type) bool { return t.Kind == ir.KNamed && t.Pkg != "" }

// sourceFunc reports whether f is a function or method the source declares.
func sourceFunc(f *ir.Func) bool {
	return !f.Closure && !f.Wrapper && f.Pos.IsValid() && !strings.HasPrefix(f.Name, "$")
}

func typeBase(t *ir.Type) string {
	head, args, generic := strings.Cut(t.Name, "[")
	if i := strings.LastIndexByte(head, '.'); i >= 0 {
		head = head[i+1:]
	}
	if generic {
		head += "_" + flatten(args)
	}
	return head
}

// funcBase drops the package of a function name and joins a method's
// receiver to its name: main.(*T).M becomes T_M.
func funcBase(name string) string {
	head, args, generic := strings.Cut(name, "[")
	if i := strings.LastIndexByte(head, '/'); i >= 0 {
		head = head[i+1:]
	}
	if i := strings.IndexByte(head, '.'); i >= 0 {
		head = head[i+1:]
	}
	head = strings.NewReplacer("(*", "", ")", "", ".", "_").Replace(head)
	if generic {
		head += "_" + flatten(args)
	}
	return head
}

// flatten keeps letters and digits and turns every other run into one '_'.
func flatten(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r > 0x7f {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "_") {
			b.WriteByte('_')
		}
	}
	return strings.TrimSuffix(b.String(), "_")
}

func (n *Names) assignLocals(f *ir.Func) {
	seen := map[*ir.Local]bool{}
	var all []*ir.Local
	for _, group := range [][]*ir.Local{f.Params, f.Results, f.Env, f.Locals} {
		for _, l := range group {
			if !seen[l] {
				seen[l] = true
				all = append(all, l)
			}
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	scope := map[string]*ir.Local{}
	taken := func(s string) bool {
		_, top := n.used[s]
		return scope[s] != nil || top || n.reserved[s] || n.members[s]
	}
	for _, l := range all {
		if l.Name == "" || l.Name == "_" {
			n.locals[l] = Prefix + strconv.Itoa(l.ID)
			continue
		}
		id := sourceIdent(l.Name)
		name := claim(id, taken)
		if name != id || !plain(l.Name) {
			c := Collision{Pos: l.Pos, Name: l.Name, Emitted: name, Reason: "not a plain identifier"}
			if o := scope[id]; o != nil {
				c.Reason, c.Other, c.OtherName = "shadowed local", o.Pos, o.Name
			} else if o, ok := n.used[id]; ok {
				c.Reason, c.Other, c.OtherName = "shadows a top-level name", o.pos, o.name
			} else if n.reserved[id] || n.members[id] {
				c.Reason = "reserved word"
			}
			n.collisions = append(n.collisions, c)
		}
		scope[name] = l
		n.locals[l] = name
	}
}

// gen allocates a generated name once per key; candidate must start with
// Prefix or contain '$', so it never takes a source name.
func (n *Names) gen(key, candidate string) string {
	if s, ok := n.generated[key]; ok {
		return s
	}
	s := claim(candidate, n.taken)
	n.used[s] = owner{}
	n.generated[key] = s
	return s
}

// Derived names a module-level helper derived from another private name,
// such as a frame class or an async body. kind is a short tag without
// underscores; a kind containing '$' is used as a plain prefix.
func (n *Names) Derived(kind, base string) string {
	return n.gen(kind+"\x00"+base, affix(kind, base))
}

// Type names a target representation of t. Pass the target's category prefix,
// such as T_, S$, or S. For shared underlying representations, the smallest
// source-qualified named type is the deterministic readable representative.
// Under source names a type declared with the target's TypePrefix keeps its
// source name, shared with its underlying struct; other prefixes derive a
// generated name per type.
func (n *Names) Type(t *ir.Type, prefix string) string {
	representative := t
	if t.Kind != ir.KNamed && n.types[t] != nil {
		representative = n.types[t]
	}
	if n.mode != Source {
		return typeName(n.mode, t, representative, prefix)
	}
	base, ok := n.typeNames[t]
	if !ok && t.Kind == ir.KStruct {
		base, ok = n.typeNames[representative]
	}
	if ok && prefix == n.rules.TypePrefix {
		return base
	}
	if !ok {
		base = Identifier(typeDescription(t, 0))
	}
	return n.gen("type\x00"+prefix+"\x00"+strconv.Itoa(t.ID), affix(prefix, base))
}

// affix joins a category prefix to a name in the generated namespace.
func affix(prefix, base string) string {
	if strings.Contains(prefix, "$") {
		return prefix + base
	}
	return Prefix + strings.TrimSuffix(prefix, "_") + "_" + strings.TrimPrefix(base, Prefix)
}

func typeName(mode Mode, t, representative *ir.Type, prefix string) string {
	id := strconv.Itoa(t.ID)
	if mode == Compact {
		return prefix + id
	}
	return prefix + Identifier(typeDescription(representative, 0)) + "_" + id
}

func typeDescription(t *ir.Type, depth int) string {
	if t.Name != "" {
		return t.Name
	}
	if t.Basic != "" {
		return t.Basic
	}
	if depth >= 3 {
		return t.Kind.String()
	}
	switch t.Kind {
	case ir.KArray:
		return "array_" + strconv.FormatInt(t.Len, 10) + "_" + typeDescription(t.Elem, depth+1)
	case ir.KSlice, ir.KPointer, ir.KChan, ir.KMapIter:
		return t.Kind.String() + "_" + typeDescription(t.Elem, depth+1)
	case ir.KMap:
		return "map_" + typeDescription(t.Key, depth+1) + "_" + typeDescription(t.Elem, depth+1)
	default:
		return t.Kind.String()
	}
}

// Local names a function-scoped cell. separator must be legal in the target
// language, normally "_" or "$"; readable names use it before the identity.
func (n *Names) Local(l *ir.Local, separator string) string {
	if n.mode == Source {
		if s, ok := n.locals[l]; ok {
			return s
		}
		return Prefix + strconv.Itoa(l.ID)
	}
	id := strconv.Itoa(l.ID)
	if n.mode == Compact {
		return "v" + id
	}
	name := l.Name
	if name == "" || name == "_" {
		switch l.Kind {
		case ir.LParam:
			name = "param"
		case ir.LResult:
			name = "result"
		case ir.LEnv:
			name = "capture"
		case ir.LVar:
			name = "var"
		default:
			name = "temp"
		}
	}
	return "v_" + Identifier(name) + separator + id
}

// Symbol maps a lowered function or global symbol to its private target name.
// Capability/runtime symbols and other unknown symbols pass through unchanged.
// Public wrappers must keep their established API names independently of this.
func (n *Names) Symbol(oldSym string) string {
	if s, ok := n.symbols[oldSym]; ok {
		return s
	}
	return oldSym
}

func function(mode Mode, f *ir.Func) string {
	id := strconv.Itoa(f.ID)
	if mode == Compact {
		return "f" + id
	}
	name := f.Name
	if name == "" {
		name = f.Sym
	}
	if name == "$init" {
		name = "init"
	}
	if name == "$entry" {
		name = "entry"
	}
	return "fn_" + Identifier(name) + "_" + id
}

func global(mode Mode, g *ir.Global) string {
	id := strconv.Itoa(g.ID)
	if mode == Compact {
		return "g" + id
	}
	name := g.Sym
	if g.Name != "" {
		name = g.Name
		if g.Pkg != "" {
			name = path.Base(g.Pkg) + "_" + name
		}
	}
	return "global_" + Identifier(name) + "_" + id
}

// Field names private aggregate storage. Public fields and Go embedded fields
// retain their API/embedding rules and should not use this helper directly.
// Under source names a field keeps its name unless it is blank, starts with
// the generated prefix, or is not plain; those add the field index.
func (n *Names) Field(t *ir.Type, index int, prefix string) string {
	id := strconv.Itoa(index)
	if n.mode == Compact {
		return prefix + id
	}
	name := t.U().Fields[index].Name
	if n.mode == Source && plain(name) && !identitySuffix(name) {
		return prefix + name
	}
	return prefix + Identifier(name) + "_" + id
}

// identitySuffix reports whether s ends in "_<digits>", the shape of an
// index suffix.
func identitySuffix(s string) bool {
	i := strings.LastIndexByte(s, '_')
	if i < 0 || i == len(s)-1 {
		return false
	}
	_, err := strconv.Atoi(s[i+1:])
	return err == nil
}
