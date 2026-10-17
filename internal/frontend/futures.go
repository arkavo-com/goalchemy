package frontend

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"

	"golang.org/x/tools/go/ast/astutil"
)

const TaskPackage = "github.com/eugenioenko/goalchemy/lib/task"

// ParseFutureFile gives the polymorphic Await intrinsic its inferred Go result
// list. The reference runner uses the same normalization before invoking gc.
func ParseFutureFile(fset *token.FileSet, filename string, src []byte, reference bool) (*ast.File, error) {
	f, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	alias := ""
	for _, imp := range f.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		if path == TaskPackage {
			alias = "task"
			if imp.Name != nil {
				alias = imp.Name.Name
			}
		}
	}
	if alias == "" {
		return f, nil
	}
	astutil.Apply(f, nil, func(c *astutil.Cursor) bool {
		call, ok := c.Node().(*ast.CallExpr)
		if !ok {
			return true
		}
		isAwait := false
		switch fn := ast.Unparen(call.Fun).(type) {
		case *ast.SelectorExpr:
			if id, ok := fn.X.(*ast.Ident); ok {
				isAwait = alias != "." && id.Name == alias && id.Obj == nil && fn.Sel.Name == "Await"
			}
		case *ast.Ident:
			isAwait = alias == "." && fn.Obj == nil && fn.Name == "Await"
		}
		if isAwait {
			c.Replace(&ast.CallExpr{Fun: call, Lparen: call.Lparen, Rparen: call.Rparen})
		}
		return true
	})
	if reference && f.Name.Name == "main" {
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "main" && fn.Recv == nil && fn.Body != nil {
				var check ast.Expr = &ast.Ident{Name: "CheckUnawaited"}
				if alias != "." {
					check = &ast.SelectorExpr{X: &ast.Ident{Name: alias}, Sel: &ast.Ident{Name: "CheckUnawaited"}}
				}
				fn.Body.List = append([]ast.Stmt{&ast.DeferStmt{Call: &ast.CallExpr{Fun: check}}}, fn.Body.List...)
			}
		}
	}
	return f, nil
}
