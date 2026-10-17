package naming

import (
	"go/token"
	"regexp"
	"strings"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/ir"
)

func TestCanonicalTypeAndCompactNames(t *testing.T) {
	u := &ir.Type{ID: 3, Kind: ir.KStruct}
	z := &ir.Type{ID: 4, Kind: ir.KNamed, Name: "z.Widget", Underlying: u}
	a := &ir.Type{ID: 5, Kind: ir.KNamed, Name: "a.Widget", Underlying: u}
	p := &ir.Program{Names: "readable", Types: &ir.Types{All: []*ir.Type{u, z, a}}}
	for _, all := range [][]*ir.Type{{u, z, a}, {a, z, u}} {
		p.Types.All = all
		if got := New(p, Rules{}).Type(u, "S_"); got != "S_a_Widget_3" {
			t.Fatalf("canonical type = %q", got)
		}
		if got := New(p, Rules{}).Type(z, "S_"); got != "S_z_Widget_4" {
			t.Fatalf("named type = %q", got)
		}
	}
	p.Names = "compact"
	if got := New(p, Rules{}).Type(u, "S_"); got != "S_3" {
		t.Fatalf("compact type = %q", got)
	}
}

func TestSafeReadableNames(t *testing.T) {
	p := &ir.Program{Names: "readable"}
	names := New(p, Rules{})
	valid := regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)
	seen := map[string]bool{}
	for id, name := range []string{"class", "λ", "a-b", "a_b", "λ", "", "_"} {
		s := names.Local(&ir.Local{ID: id, Name: name, Kind: ir.LTemp}, "_")
		if !valid.MatchString(s) || seen[s] {
			t.Fatalf("unsafe or duplicate local %q", s)
		}
		seen[s] = true
	}
	if got := names.Local(&ir.Local{ID: 8, Kind: ir.LParam}, "_"); got != "v_param_8" {
		t.Fatalf("unnamed param = %q", got)
	}
	if got := names.Local(&ir.Local{ID: 9, Kind: ir.LResult}, "_"); got != "v_result_9" {
		t.Fatalf("unnamed result = %q", got)
	}
	if got := Identifier("λ"); got != "_u3bb_" {
		t.Fatalf("unicode encoding = %q", got)
	}
}

func TestPrivateSymbolsAndFields(t *testing.T) {
	f := &ir.Func{ID: 2, Sym: "main_Do", Name: "main.Do", Pos: 1}
	g := &ir.Global{ID: 2, Sym: "main_Count", Name: "Count", Pkg: "main"}
	u := &ir.Type{Kind: ir.KStruct, Fields: []ir.Field{{Name: "class"}, {Name: "λ"}, {Name: "_"}, {Name: "x_1"}}}
	for _, tc := range []struct{ mode, function, global, field0, field3 string }{
		{"readable", "fn_main_Do_2", "global_main_Count_2", "f_class_0", "f_x_1_3"},
		{"compact", "f2", "g2", "f_0", "f_3"},
		{"source", "Do", "Count", "f_class", "f_x_1_3"},
	} {
		p := &ir.Program{Names: tc.mode, Funcs: []*ir.Func{f}, Globals: []*ir.Global{g}}
		names := New(p, Rules{})
		if got := names.Symbol(f.Sym); got != tc.function {
			t.Errorf("%s function = %q", tc.mode, got)
		}
		if got := names.Symbol(g.Sym); got != tc.global {
			t.Errorf("%s global = %q", tc.mode, got)
		}
		if got := names.Symbol("rt.external"); got != "rt.external" {
			t.Errorf("%s external = %q", tc.mode, got)
		}
		if got := names.Field(u, 0, "f_"); got != tc.field0 {
			t.Errorf("%s field = %q", tc.mode, got)
		}
		if got := names.Field(u, 3, "f_"); got != tc.field3 {
			t.Errorf("%s field = %q", tc.mode, got)
		}
		if tc.mode != "compact" && !strings.Contains(names.Field(u, 1, "f_"), "u3bb") {
			t.Errorf("%s lost Unicode field identity", tc.mode)
		}
		if names.Field(u, 2, "f_") == names.Field(u, 3, "f_") {
			t.Errorf("%s fields collide", tc.mode)
		}
	}
	if f.Sym != "main_Do" || g.Sym != "main_Count" || u.Fields[0].Name != "class" {
		t.Fatal("naming modified source identities")
	}
}

// program declares, in source order, a type, a global, functions and the
// locals of the last function.
func program(locals ...string) (*ir.Program, *ir.Func) {
	task := &ir.Type{ID: 1, Kind: ir.KNamed, Name: "main.Task", Pkg: "example.com/main", Underlying: &ir.Type{ID: 2, Kind: ir.KStruct}}
	f := &ir.Func{ID: 1, Name: "main.fetch", Sym: "main_fetch", Pkg: "example.com/main", Pos: 10}
	for i, name := range locals {
		l := f.NewLocal(name, task, ir.LVar)
		l.Pos = token.Pos(100 + i)
	}
	p := &ir.Program{
		Types:   &ir.Types{All: []*ir.Type{task.Underlying, task}},
		Globals: []*ir.Global{{ID: 0, Name: "count", Pkg: "example.com/main", Sym: "main_count", Pos: 5}},
		Funcs: []*ir.Func{
			{ID: 0, Name: "main.(*Task).Run", Sym: "main_Task_Run", Pkg: "example.com/main", Pos: 7, MethodID: "Run"},
			f,
			{ID: 2, Name: "main.fetch$1", Sym: "main_fetch_func1", Pkg: "example.com/main", Closure: true},
			{ID: 3, Name: "$init", Sym: "goalchemy_init"},
		},
	}
	return p, f
}

func TestSourceNames(t *testing.T) {
	p, f := program("tasks", "", "tasks", "count", "class", "_hidden", "δ", "tasks_2")
	names := New(p, Rules{Reserved: []string{"class"}, TypePrefix: "S$"})
	task := p.Types.All[1]
	for _, tc := range []struct{ got, want string }{
		{names.Type(task, "S$"), "Task"},
		{names.Type(task.Underlying, "S$"), "Task"},
		{names.Type(task, "zero$"), "zero$Task"},
		{names.Type(task, "z_"), "_z_Task"},
		{names.Symbol("main_count"), "count"},
		{names.Symbol("main_Task_Run"), "Task_Run"},
		{names.Symbol("main_fetch"), "fetch"},
		{names.Symbol("main_fetch_func1"), "_fetch_1"},
		{names.Symbol("goalchemy_init"), "_init"},
		{names.Derived("F$", "fetch"), "F$fetch"},
		{names.Derived("a", "fetch"), "_a_fetch"},
		{names.Derived("a", "_fetch_1"), "_a_fetch_1"},
	} {
		if tc.got != tc.want {
			t.Errorf("got %q, want %q", tc.got, tc.want)
		}
	}
	var got []string
	for _, l := range f.Locals {
		got = append(got, names.Local(l, "$"))
	}
	want := []string{"tasks", "_1", "tasks_2", "count_2", "class_2", "x_hidden", "x_u3b4_", "tasks_2_2"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("locals = %v, want %v", got, want)
	}
	reasons := map[string]string{}
	for _, c := range names.Collisions() {
		reasons[c.Name+"@"+c.Emitted] = c.Reason
	}
	for key, reason := range map[string]string{
		"tasks@tasks_2":     "shadowed local",
		"count@count_2":     "shadows a top-level name",
		"class@class_2":     "reserved word",
		"_hidden@x_hidden":  "not a plain identifier",
		"δ@x_u3b4_":         "not a plain identifier",
		"tasks_2@tasks_2_2": "shadowed local",
	} {
		if reasons[key] != reason {
			t.Errorf("collision %s = %q, want %q (all: %v)", key, reasons[key], reason, reasons)
		}
	}
	if len(reasons) != 6 {
		t.Errorf("collisions = %v", reasons)
	}
	again := New(p, Rules{Reserved: []string{"class"}, TypePrefix: "S$"})
	for _, l := range f.Locals {
		if again.Local(l, "$") != names.Local(l, "$") {
			t.Fatal("source names are not deterministic")
		}
	}
}

func TestSourceNamesAcrossPackages(t *testing.T) {
	left := &ir.Type{ID: 1, Kind: ir.KNamed, Name: "left.Binding", Pkg: "example.com/left", Underlying: &ir.Type{ID: 3, Kind: ir.KStruct}}
	right := &ir.Type{ID: 2, Kind: ir.KNamed, Name: "right.Binding", Pkg: "example.com/right", Underlying: &ir.Type{ID: 4, Kind: ir.KStruct}}
	p := &ir.Program{
		Types: &ir.Types{All: []*ir.Type{left, right}},
		Funcs: []*ir.Func{
			{ID: 0, Name: "left.New", Sym: "left_New", Pkg: "example.com/left", Pos: 1},
			{ID: 1, Name: "right.New", Sym: "right_New", Pkg: "example.com/right", Pos: 2},
			{ID: 2, Name: "main.left_New", Sym: "main_left_New", Pkg: "example.com/main", Pos: 3},
			{ID: 3, Name: "main.Only", Sym: "main_Only", Pkg: "example.com/main", Pos: 4},
		},
	}
	names := New(p, Rules{TypePrefix: "type_"})
	for _, tc := range []struct{ got, want string }{
		{names.Type(left, "type_"), "left_Binding"},
		{names.Type(right, "type_"), "right_Binding"},
		{names.Symbol("left_New"), "left_New"},
		{names.Symbol("right_New"), "right_New"},
		{names.Symbol("main_left_New"), "left_New_2"},
		{names.Symbol("main_Only"), "Only"},
	} {
		if tc.got != tc.want {
			t.Errorf("got %q, want %q", tc.got, tc.want)
		}
	}
	var clashes []string
	for _, c := range names.Collisions() {
		clashes = append(clashes, c.Name+":"+c.Reason)
	}
	if strings.Join(clashes, " ") != "Binding:package clash Binding:package clash New:package clash New:package clash left_New:duplicate name" {
		t.Fatalf("collisions = %v", clashes)
	}
}

func TestRuntimeNames(t *testing.T) {
	declared, called := map[string]bool{}, map[string]bool{}
	scanRuntime("pub(super) fn chan_send(t: &Task) -> V {\n    let local = 1;\n    helper(local); // fn ignored()\n    \"fn quoted\";\n}\nconst LIMIT: usize = 3;\n", declared, called)
	for _, name := range []string{"chan_send", "Task", "V", "LIMIT"} {
		if !declared[name] {
			t.Errorf("missing declared %s", name)
		}
	}
	if !called["helper"] || declared["local"] || called["local"] || declared["ignored"] || declared["quoted"] {
		t.Errorf("declared %v called %v", declared, called)
	}
}
