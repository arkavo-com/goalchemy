package ir

import (
	"go/constant"
	"go/token"
)

// Program is a whole lowered Goalchemy program.
type Program struct {
	// Names selects private identifiers during target emission: "source"
	// (the default when empty), "readable" or "compact". Source identities
	// and public export names are independent of this option.
	Names string
	// NameCollisions is "rename" (the default) or "fail": under source names,
	// fail reports each renamed source identifier instead of emitting.
	NameCollisions string
	// ReservedNames lists identifiers the target runtime declares or uses
	// unqualified; source names avoid them. ReservedGlobals lists names only
	// top-level source names avoid, such as host functions the runtime calls.
	ReservedNames   []string
	ReservedGlobals []string
	// PauseModel selects how suspending functions are emitted: "frames"
	// (resumable frames, the default) or "native" (target-native pausing).
	PauseModel string
	// NoLineDirectives omits the source annotations (line directives, source
	// maps and line tables) that map generated code back to the source.
	NoLineDirectives bool
	Types            *Types
	// Packages preserves included source packages in dependency order. External
	// capability packages remain runtime contracts rather than source owners.
	Packages []Package
	Funcs    []*Func
	Globals  []*Global
	// Init runs package initialization in dependency order.
	Init *Func
	// Main is the executable entry point, if any.
	Main *Func
	// Library is set when the root packages are not package main; Exports
	// lists their exported top-level functions in declaration order.
	Library bool
	Exports []*Func
	// Externals lists capability contracts called by the program.
	Externals map[string]*Extern
	// Cooperative is set when the program uses tasks, channels, or other
	// suspending operations, so targets must link their scheduler.
	Cooperative bool
	// Entry runs Init and then Main; cooperative targets start it as the
	// first task.
	Entry *Func
	// SuspTypes holds function types (underlying) whose values use the
	// resumable calling convention; SuspMethods holds such method identities.
	SuspTypes   map[*Type]bool
	SuspMethods map[string]bool
	Fset        *token.FileSet
}

// Package describes an included source package independently of target layout.
type Package struct {
	Path    string   `json:"path"`
	Name    string   `json:"name"`
	Imports []string `json:"imports"`
	Root    bool     `json:"root"`
}

// Extern describes a call target bound to an external capability contract.
type Extern struct {
	Contract string
	Symbol   string
	Sig      *Type
	// MaySuspend comes from the contract's suspension classification.
	MaySuspend bool
}

type Global struct {
	ID   int
	Name string // source name
	Pkg  string
	Sym  string // stable symbol
	Type *Type
	// AddrTaken is set when the global's address is taken.
	AddrTaken bool
	Pos       token.Pos
}

type LocalKind uint8

const (
	LParam LocalKind = iota + 1
	LResult
	LVar
	LTemp
	// LEnv is a captured variable received from an enclosing function.
	LEnv
)

// Local is a function-scoped storage cell. Boxed locals need fresh heap
// storage on every DeclVar because their address is taken or they are
// captured by a closure.
type Local struct {
	ID    int
	Name  string
	Type  *Type
	Kind  LocalKind
	Boxed bool
	Pos   token.Pos
}

func (l *Local) IRType() *Type { return l.Type }

// Value is an instruction operand.
type Value interface {
	IRType() *Type
}

// Const is a typed constant. Nil constants represent the zero value of
// slices, maps, pointers, functions, channels, and interfaces.
type Const struct {
	Type *Type
	Val  constant.Value // nil for Nil
	Nil  bool
}

func (c *Const) IRType() *Type { return c.Type }

// FuncRef is the function value of a top-level function or method.
type FuncRef struct {
	Func *Func
	Type *Type
}

func (f *FuncRef) IRType() *Type { return f.Type }

type Func struct {
	ID      int
	Name    string // source name, e.g. main.T.M or main.main$1
	Sym     string // stable symbol
	Pkg     string
	Sig     *Type // function type, receiver included as the first param for methods
	Params  []*Local
	Results []*Local
	// Env lists captured variables for closures, in MakeClosure order.
	Env    []*Local
	Locals []*Local
	Blocks []*Block

	// Recv is set for methods; it is also Params[0].
	Recv *Local
	// Method identity for methods and wrappers.
	MethodID string
	RecvType *Type

	HasDefer   bool
	HasRecover bool
	// MaySuspend is computed by effect analysis: the function can block or
	// yield, directly or through a call.
	MaySuspend bool
	// Wrapper marks compiler-generated method wrappers.
	Wrapper bool
	// Closure marks function literals.
	Closure bool
	Pos     token.Pos

	nextLocal int
}

func (f *Func) NewLocal(name string, t *Type, kind LocalKind) *Local {
	l := &Local{ID: f.nextLocal, Name: name, Type: t, Kind: kind}
	f.nextLocal++
	f.Locals = append(f.Locals, l)
	return l
}

func (f *Func) NewBlock(comment string) *Block {
	b := &Block{ID: len(f.Blocks), Comment: comment}
	f.Blocks = append(f.Blocks, b)
	return b
}

type Block struct {
	ID      int
	Comment string
	Instrs  []Instr
	Term    Terminator
	// ResumeOf is the paused operation whose results this continuation
	// block receives when the task resumes.
	ResumeOf Instr
}

type Instr interface{ Position() token.Pos }

type Terminator interface{ Position() token.Pos }

type At struct{ Pos token.Pos }

func (a At) Position() token.Pos { return a.Pos }

// Places designate storage. Root is one of LocalRoot, GlobalRoot, DerefRoot,
// SliceRoot, or ValueRoot (read-only). Path applies field and array index
// projections in order.
type Place struct {
	Root Root
	Path []Proj
	Type *Type // type of the designated storage
}

type Root interface{ root() }

type LocalRoot struct{ Local *Local }
type GlobalRoot struct{ Global *Global }

// DerefRoot designates *Ptr; dereferencing nil panics.
type DerefRoot struct{ Ptr Value }

// SliceRoot designates element Index of slice Slice, bounds-checked.
type SliceRoot struct {
	Slice Value
	Index Value
}

// ValueRoot designates a non-addressable value, for reads only.
type ValueRoot struct{ Value Value }

func (LocalRoot) root()  {}
func (GlobalRoot) root() {}
func (DerefRoot) root()  {}
func (SliceRoot) root()  {}
func (ValueRoot) root()  {}

type Proj struct {
	// Field index when Index is nil; otherwise an array element.
	Field int
	Index Value
	// Type is the type after the projection.
	Type *Type
}

// Addressable reports whether the place contains no element projections.
func (p *Place) HasElement() bool {
	if _, ok := p.Root.(SliceRoot); ok {
		return true
	}
	for _, pr := range p.Path {
		if pr.Index != nil {
			return true
		}
	}
	return false
}

// ---- Instructions ----

// DeclVar allocates fresh storage for L, initialized with Init or the zero value.
type DeclVar struct {
	At
	L    *Local
	Init Value
}

// Assign copies a scalar or reference value into a temp or unboxed local.
type Assign struct {
	At
	Dst *Local
	Src Value
}

// Zero sets Dst to a fresh zero value of its type.
type Zero struct {
	At
	Dst *Local
}

// Load reads the value at Place into Dst, copying value aggregates.
type Load struct {
	At
	Dst   *Local
	Place *Place
}

// Store writes V into Place, copying value aggregates.
type Store struct {
	At
	Place *Place
	V     Value
}

// AddrOf takes the address of an addressable place.
type AddrOf struct {
	At
	Dst   *Local
	Place *Place
}

// New allocates a zero value of Dst's element type.
type New struct {
	At
	Dst *Local
}

type UnOpKind uint8

const (
	Neg UnOpKind = iota + 1
	Not
	BitNot
)

type UnOp struct {
	At
	Dst *Local
	Op  UnOpKind
	X   Value
}

type BinOpKind uint8

const (
	Add BinOpKind = iota + 1
	Sub
	Mul
	Div
	Rem
	And
	Or
	Xor
	AndNot
	Shl
	Shr
	Eq
	Ne
	Lt
	Le
	Gt
	Ge
	Min
	Max
)

var binOpNames = [...]string{"?", "+", "-", "*", "/", "%", "&", "|", "^", "&^", "<<", ">>", "==", "!=", "<", "<=", ">", ">=", "min", "max"}

func (k BinOpKind) String() string { return binOpNames[k] }

func (k BinOpKind) IsComparison() bool { return k >= Eq && k <= Ge }

// BinOp operates on X and Y. For comparisons the operand type is X's type;
// for shifts Y may be any integer type.
type BinOp struct {
	At
	Dst *Local
	Op  BinOpKind
	X   Value
	Y   Value
}

type ConvKind uint8

const (
	ConvNop ConvKind = iota + 1 // identical underlying representation
	ConvInt                     // integer width/signedness change
	ConvIntToString
	ConvStringToBytes
	ConvBytesToString
	ConvStringToRunes
	ConvRunesToString
	ConvSliceToArray
	ConvIfaceToIface // interface to interface (no check)
	ConvFloat        // numeric conversion involving a float; destination width rounds explicitly
)

type Convert struct {
	At
	Dst  *Local
	X    Value
	Kind ConvKind
}

// MakeInterface boxes concrete value X into interface type Dst.Type.
type MakeInterface struct {
	At
	Dst *Local
	X   Value
}

// TypeAssert asserts X (interface) to T. With Ok, failure yields the zero
// value and false; without, failure panics.
type TypeAssert struct {
	At
	Dst *Local
	Ok  *Local
	X   Value
	T   *Type
}

type CallKind uint8

const (
	CallStatic CallKind = iota + 1
	CallValue
	CallInterface
	CallExtern
)

type Call struct {
	At
	Dsts []*Local
	Kind CallKind
	Func *Func // CallStatic
	Fn   Value // CallValue
	Recv Value // CallInterface: interface value
	// Method identity for CallInterface.
	Method string
	Extern *Extern
	Args   []Value
	// Suspends is set by effect analysis when some possible callee may suspend.
	Suspends bool
}

// MakeClosure creates a function value for a closure function capturing Env.
type MakeClosure struct {
	At
	Dst  *Local
	Func *Func
	Env  []*Local
}

// MakeBound binds a receiver value to a method function, producing a
// function value (a method value).
type MakeBound struct {
	At
	Dst  *Local
	Func *Func
	Recv Value
}

// MakeIfaceBound produces a method value from an interface receiver.
type MakeIfaceBound struct {
	At
	Dst    *Local
	Recv   Value
	Method string
}

type Len struct {
	At
	Dst *Local
	X   Value
}

type Cap struct {
	At
	Dst *Local
	X   Value
}

type MakeSlice struct {
	At
	Dst *Local
	Len Value
	Cap Value
}

type MakeMap struct {
	At
	Dst *Local
}

// Append appends Elems, or the elements of Spread when non-nil (a slice or
// a string for []byte).
type Append struct {
	At
	Dst    *Local
	S      Value
	Elems  []Value
	Spread Value
}

// Copy copies elements from Src (slice or string) into Dst slice.
type Copy struct {
	At
	N   *Local
	Dst Value
	Src Value
}

type IndexString struct {
	At
	Dst *Local
	S   Value
	I   Value
}

// SliceOp slices X (slice, string, or pointer to array). Nil bounds use
// defaults. Max is only valid for slices and arrays.
type SliceOp struct {
	At
	Dst         *Local
	X           Value
	Lo, Hi, Max Value
}

type MapLookup struct {
	At
	Dst *Local
	Ok  *Local
	M   Value
	K   Value
}

type MapStore struct {
	At
	M Value
	K Value
	V Value
}

type MapDelete struct {
	At
	M Value
	K Value
}

// Clear clears a map or zeroes the elements of a slice.
type Clear struct {
	At
	X Value
}

// DecodeRune decodes the rune at byte offset I of S.
type DecodeRune struct {
	At
	Rune  *Local
	Width *Local
	S     Value
	I     Value
}

// MapIterInit snapshots the entries of M into a new iterator.
type MapIterInit struct {
	At
	Iter *Local
	M    Value
}

// MapIterNext advances Iter, setting Ok, and Key/Val when non-nil.
type MapIterNext struct {
	At
	Ok   *Local
	Key  *Local
	Val  *Local
	Iter *Local
}

type Print struct {
	At
	Args    []Value
	Newline bool
}

// Defer registers a deferred call. Callee and arguments are already evaluated.
type Defer struct {
	At
	Call *Call
}

type Recover struct {
	At
	Dst *Local
}

// Rebind gives a boxed loop variable fresh storage holding its current value.
type Rebind struct {
	At
	L *Local
}

// MakeChan creates a channel with buffer capacity Size (an int).
type MakeChan struct {
	At
	Dst  *Local
	Size Value
}

// Send sends V on Ch, suspending until the send can proceed.
type Send struct {
	At
	Ch Value
	V  Value
}

// Recv receives from Ch into Dst (and Ok when non-nil), suspending until a
// value or closure is available. Dst may be nil to discard the value.
type Recv struct {
	At
	Dst *Local
	Ok  *Local
	Ch  Value
}

// Close closes a channel.
type Close struct {
	At
	Ch Value
}

// SelectCase is one communication of a select statement. Operands are
// evaluated before the Select instruction in source order.
type SelectCase struct {
	Send bool
	Ch   Value
	V    Value  // send value
	Dst  *Local // receive value, may be nil
	Ok   *Local // receive ok, may be nil
}

// Select commits exactly one ready case, choosing uniformly among ready
// cases with the scheduler's choice source, or the default when Default is
// set and nothing is ready; it suspends otherwise. Index receives the chosen
// case index, or -1 for the default.
type Select struct {
	At
	Cases   []SelectCase
	Default bool
	Index   *Local
}

// Go starts a new task running Call; callee and arguments are evaluated.
type Go struct {
	At
	Call *Call
}

// BoxParam moves a parameter that arrived as a plain value into boxed
// storage. It appears at function entry for boxed parameters.
type BoxParam struct {
	At
	L *Local
}

// ---- Terminators ----

type Jump struct {
	At
	Target *Block
}

type If struct {
	At
	Cond Value
	Then *Block
	Else *Block
}

// Return returns the current values of the function's result locals.
type Return struct{ At }

type Panic struct {
	At
	X Value
}

// Unreachable marks control flow that cannot continue, such as the end of a
// function whose last statement cannot complete normally.
type Unreachable struct{ At }

// Pause ends a block at a pause point. Op is the suspending operation; the
// task resumes at Next, which first receives Op's results.
type Pause struct {
	At
	Op   Instr
	Next *Block
}

// PanicRuntime panics with a runtime.Error whose Error() text is Msg.
type PanicRuntime struct {
	At
	Msg string
}
