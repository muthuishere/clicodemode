package js

// Node is any AST node.
type Node interface{ P() Pos }

type base struct{ pos Pos }

func (b base) P() Pos { return b.pos }

// ---- expressions ----

type (
	NumberLit struct {
		base
		V float64
	}
	StringLit struct {
		base
		V string
	}
	BoolLit struct {
		base
		V bool
	}
	NullLit  struct{ base }
	UndefLit struct{ base }
	Ident    struct {
		base
		Name string
	}
	TemplateLit struct {
		base
		Quasis []string
		Exprs  []Node
	}
	ArrayLit struct {
		base
		Elems []Node // *Spread allowed
	}
	Property struct {
		Key      string
		Computed Node // non-nil for [expr]: value
		Value    Node
		Spread   bool
	}
	ObjectLit struct {
		base
		Props []Property
	}
	Spread struct {
		base
		X Node
	}
	Unary struct {
		base
		Op string
		X  Node
	}
	Update struct {
		base
		Op     string // ++ or --
		Prefix bool
		X      Node
	}
	Binary struct {
		base
		Op   string
		L, R Node
	}
	Logical struct {
		base
		Op   string // && || ??
		L, R Node
	}
	Assign struct {
		base
		Op     string // = += -= ...
		Target Node
		Value  Node
	}
	Cond struct {
		base
		Test, Then, Else Node
	}
	Member struct {
		base
		Obj      Node
		Name     string
		Computed Node // obj[expr]
		Optional bool // ?.
	}
	Call struct {
		base
		Callee   Node
		Args     []Node
		Optional bool
	}
	Await struct {
		base
		X Node
	}
	Func struct {
		base
		Name   string
		Params []Param
		Body   []Node // statements; for expression-bodied arrows a single *Return
		Arrow  bool
		Async  bool
	}
	Seq struct {
		base
		List []Node
	}
)

// Param is a function parameter: a name or destructuring pattern, optional
// default, optional rest.
type Param struct {
	Target  Node // *Ident, *ArrayPattern, *ObjectPattern
	Default Node
	Rest    bool
}

// Patterns for destructuring.
type (
	ArrayPattern struct {
		base
		Elems []Param // nil Target = hole
	}
	ObjectPattern struct {
		base
		Props []PatternProp
		Rest  Node // *Ident
	}
	PatternProp struct {
		Key     string
		Target  Node
		Default Node
	}
)

// ---- statements ----

type (
	VarDecl struct {
		base
		Kind  string // const let var
		Decls []Declarator
	}
	Declarator struct {
		Target Node
		Init   Node
	}
	ExprStmt struct {
		base
		X Node
	}
	Block struct {
		base
		Body []Node
	}
	If struct {
		base
		Test       Node
		Then, Else Node
	}
	For struct {
		base
		Init   Node
		Test   Node
		Update Node
		Body   Node
	}
	ForOf struct {
		base
		Kind  string // const let var or "" for existing binding
		Left  Node
		Right Node
		Body  Node
		In    bool // for-in (iterates keys)
	}
	While struct {
		base
		Test Node
		Body Node
		Do   bool
	}
	Break struct {
		base
		Label string
	}
	Continue struct {
		base
		Label string
	}
	Return struct {
		base
		X Node
	}
	Try struct {
		base
		Body    *Block
		Param   Node // may be nil
		Catch   *Block
		Finally *Block
	}
	Empty  struct{ base }
	Import struct {
		base
		Source    string
		Default   string
		Names     map[string]string // local -> imported
		Namespace string
	}
	Export struct {
		base
		Decl    Node              // VarDecl or Func statement
		Names   map[string]string // exported -> local
		Default Node
	}
	Program struct {
		base
		Body []Node
	}
)
