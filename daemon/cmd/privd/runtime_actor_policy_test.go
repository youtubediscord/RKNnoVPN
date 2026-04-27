package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestMutatingHandlersDoNotBypassRuntimeActor(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || !strings.HasPrefix(fn.Name.Name, "handle") {
			continue
		}

		parents := map[ast.Node]ast.Node{}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if n == nil {
				return true
			}
			for _, child := range childNodes(n) {
				parents[child] = n
			}
			return true
		})

		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if !isForbiddenRuntimeMutation(call) {
				return true
			}
			if insideRuntimeActorScope(call, parents) {
				return true
			}
			pos := fset.Position(call.Pos())
			t.Errorf("%s bypasses runtime actor at %s", fn.Name.Name, pos)
			return true
		})
	}
}

func isForbiddenRuntimeMutation(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	switch receiverName(sel.X) {
	case "coreMgr":
		switch sel.Sel.Name {
		case "Start", "Stop", "HotSwap", "RescueReset", "ResetState":
			return true
		}
	case "d":
		switch sel.Sel.Name {
		case "resetNetworkState", "resetNetworkStateReport":
			return true
		}
	}
	return false
}

func receiverName(expr ast.Expr) string {
	switch x := expr.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return x.Sel.Name
	default:
		return ""
	}
}

func insideRuntimeActorScope(n ast.Node, parents map[ast.Node]ast.Node) bool {
	for current := parents[n]; current != nil; current = parents[current] {
		lit, ok := current.(*ast.FuncLit)
		if !ok {
			continue
		}
		call, ok := parents[lit].(*ast.CallExpr)
		if !ok || !isRuntimeActorRunOperation(call) {
			continue
		}
		return true
	}
	return false
}

func isRuntimeActorRunOperation(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "RunOperation" && receiverName(sel.X) == "runtimeV2"
}

func childNodes(n ast.Node) []ast.Node {
	var children []ast.Node
	ast.Inspect(n, func(child ast.Node) bool {
		if child == nil || child == n {
			return true
		}
		children = append(children, child)
		return false
	})
	return children
}
