package main

import (
	"fmt"
	"go/ast"
	"path"
	"strconv"
	"strings"
)

const ruleAtomicWrite = "atomic-write"

// stateRoot is the tree this rule speaks for: packages whose files are user
// state, where a torn write is a lost session rather than a lost cache.
const stateRoot = "internal/state/"

// atomicWriteAllowedPackages exempts whole packages whose files are inputs to a
// test rather than a user's only copy.
var atomicWriteAllowedPackages = map[string]string{
	"internal/state/sessionv4/v4fixture": "builds fixture trees for tests; a torn fixture fails the test that asked for it, never a user's session",
}

// atomicWriteAllowed names functions that replace a file in place on purpose.
// The key is package and function, never file and line: a line number is a fact
// about this week's formatting, while the function is where the decision lives.
// Each value states the property that makes the in-place write safe; "it works
// today" is not one, and neither is "the file is small".
var atomicWriteAllowed = map[string]string{
	"internal/state/sessionstore.writeImportMarkers": "the content is empty; a torn write and a complete write leave the same zero bytes",
	"internal/state/storage.WriteMarker":             "a half-written marker reads as no claim, the same answer as an absent one (see WriteMarker)",
	"internal/state/trajectory.New":                  "the path belongs to one run, so truncation cannot cut an earlier run; a kill loses the run in flight by design",
}

// wholeFileCalls replace a file's entire contents. Appends are absent on
// purpose: a torn tail is what the reader already tolerates, and doctor
// compacts it away.
var wholeFileCalls = map[string]bool{"WriteFile": true, "Create": true}

// checkAtomicWrite reports whole-file replacements under internal/state that do
// not go through fileutil. That helper fsyncs then renames; os.WriteFile does
// neither, and a kill between the truncate and the last byte leaves a file that
// parses as something the user never wrote.
func checkAtomicWrite(s *sourceFile) []Finding {
	if s.isTest() || s.file == nil {
		return nil
	}
	rel := strings.ReplaceAll(s.rel, "\\", "/")
	if !strings.HasPrefix(rel, stateRoot) {
		return nil
	}
	dir := path.Dir(rel)
	if _, ok := atomicWriteAllowedPackages[dir]; ok {
		return nil
	}
	osName := importName(s.file, "os")
	ioutilName := importName(s.file, "ioutil")
	if osName == "" && ioutilName == "" {
		return nil
	}
	var out []Finding
	for _, decl := range s.file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		name := qualifiedFuncName(dir, fn)
		if _, ok := atomicWriteAllowed[name]; ok {
			continue
		}
		// A local named os shadows the package, so the call here is not that package's.
		if declaresIdent(fn, osName) || declaresIdent(fn, ioutilName) {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			callName, ok := rawWriteCall(call, osName, ioutilName)
			if !ok {
				return true
			}
			out = append(out, Finding{
				File:   s.rel,
				Line:   s.line(call.Pos()),
				Rule:   ruleAtomicWrite,
				Msg:    fmt.Sprintf("%s replaces a whole file in internal/state with a raw %s; use fileutil.AtomicWriteFile (AtomicOverwriteFile when the file may exist), or list this function in tools/repolint/atomicwrite.go with the property that makes an in-place write safe", name, callName),
				Weight: 1,
			})
			return true
		})
	}
	return out
}

// importName returns the name a file refers to importPath by, or "" when it does
// not import it or imports it as _ / . where a selector cannot name it.
func importName(f *ast.File, importPath string) string {
	for _, spec := range f.Imports {
		p, err := strconv.Unquote(spec.Path.Value)
		if err != nil || p != importPath {
			continue
		}
		if spec.Name == nil {
			return path.Base(importPath)
		}
		if spec.Name.Name == "_" || spec.Name.Name == "." {
			return ""
		}
		return spec.Name.Name
	}
	return ""
}

func qualifiedFuncName(dir string, fn *ast.FuncDecl) string {
	if recv := receiverTypeName(fn); recv != "" {
		return dir + "." + recv + "." + fn.Name.Name
	}
	return dir + "." + fn.Name.Name
}

// rawWriteCall names the call when it replaces a whole file, directly or by
// opening it for truncation.
func rawWriteCall(call *ast.CallExpr, osName, ioutilName string) (string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return "", false
	}
	if ioutilName != "" && pkg.Name == ioutilName && sel.Sel.Name == "WriteFile" {
		return "ioutil.WriteFile", true
	}
	if osName == "" || pkg.Name != osName {
		return "", false
	}
	if wholeFileCalls[sel.Sel.Name] {
		return "os." + sel.Sel.Name, true
	}
	if sel.Sel.Name == "OpenFile" && len(call.Args) > 1 && hasTruncFlag(call.Args[1]) {
		return "os.OpenFile(..., O_TRUNC)", true
	}
	return "", false
}

func hasTruncFlag(expr ast.Expr) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "O_TRUNC" {
			found = true
		}
		return !found
	})
	return found
}

// declaresIdent reports whether fn declares name as a parameter, result, or
// local. Function literals are not searched: a capture that renames os is a
// different function's business.
func declaresIdent(fn *ast.FuncDecl, name string) bool {
	if name == "" {
		return false
	}
	for _, list := range []*ast.FieldList{fn.Type.Params, fn.Type.Results} {
		if list == nil {
			continue
		}
		for _, field := range list.List {
			for _, id := range field.Names {
				if id.Name == name {
					return true
				}
			}
		}
	}
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		switch v := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.AssignStmt:
			for _, lhs := range v.Lhs {
				if isIdent(lhs, name) {
					found = true
				}
			}
		case *ast.ValueSpec:
			for _, id := range v.Names {
				if id.Name == name {
					found = true
				}
			}
		case *ast.RangeStmt:
			if isIdent(v.Key, name) || isIdent(v.Value, name) {
				found = true
			}
		}
		return true
	})
	return found
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}
