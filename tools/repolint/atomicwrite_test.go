package main

import (
	"go/parser"
	"go/token"
	"testing"
)

func atomicWriteFindings(t *testing.T, rel, src string) []Finding {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, rel, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	return checkAtomicWrite(&sourceFile{rel: rel, fset: fset, file: file, src: splitLines([]byte(src))})
}

func TestAtomicWriteFlagsEveryReplacingCall(t *testing.T) {
	src := `package sessionstore
import "os"
func save(path string, b []byte) error {
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return err
	}
	f, err := os.Create(path + ".run")
	if f != nil {
		_ = f.Close()
	}
	return err
}`
	got := atomicWriteFindings(t, "internal/state/sessionstore/x.go", src)
	if len(got) != 2 || got[0].Rule != ruleAtomicWrite {
		t.Fatalf("findings = %+v, want one per replacing call", got)
	}
}

func TestAtomicWriteLeavesAppendsAlone(t *testing.T) {
	src := `package sessionstore
import "os"
func appendLine(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(b)
	return err
}`
	if got := atomicWriteFindings(t, "internal/state/sessionstore/x.go", src); len(got) != 0 {
		t.Fatalf("findings = %+v, want none: a torn tail is the reader's problem", got)
	}
}

func TestAtomicWriteFlagsTruncatingOpen(t *testing.T) {
	src := `package sessionstore
import "os"
func reset(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}`
	if got := atomicWriteFindings(t, "internal/state/sessionstore/x.go", src); len(got) != 1 {
		t.Fatalf("findings = %+v, want the truncating open", got)
	}
}

func TestAtomicWriteHonoursThePackageExemption(t *testing.T) {
	src := `package v4fixture
import "os"
func Session(path string, b []byte) { _ = os.WriteFile(path, b, 0o600) }`
	if got := atomicWriteFindings(t, "internal/state/sessionv4/v4fixture/fixture.go", src); len(got) != 0 {
		t.Fatalf("findings = %+v, want none in a fixture package", got)
	}
}

func TestAtomicWriteHonoursTheFunctionExemption(t *testing.T) {
	src := `package storage
import "os"
func WriteMarker(path string, b []byte) error { return os.WriteFile(path, b, 0o600) }`
	if got := atomicWriteFindings(t, "internal/state/storage/marker.go", src); len(got) != 0 {
		t.Fatalf("findings = %+v, want the allow-listed function", got)
	}
}

func TestAtomicWriteIgnoresTreesOutsideState(t *testing.T) {
	src := `package agent
import "os"
func save(path string, b []byte) error { return os.WriteFile(path, b, 0o600) }`
	if got := atomicWriteFindings(t, "internal/runtime/agent/x.go", src); len(got) != 0 {
		t.Fatalf("findings = %+v, want none outside internal/state", got)
	}
}

func TestAtomicWriteSkipsTestFiles(t *testing.T) {
	src := `package sessionstore
import "os"
func save(path string, b []byte) error { return os.WriteFile(path, b, 0o600) }`
	if got := atomicWriteFindings(t, "internal/state/sessionstore/x_test.go", src); len(got) != 0 {
		t.Fatalf("findings = %+v, want none in a test file", got)
	}
}

func TestAtomicWriteReadsTheNameTheFileImports(t *testing.T) {
	// An alias is how a package gets called something else; matching the
	// selector rather than the import path keeps the rule honest either way.
	src := `package sessionstore
import o "os"
func save(path string, b []byte) error { return o.WriteFile(path, b, 0o600) }`
	if got := atomicWriteFindings(t, "internal/state/sessionstore/x.go", src); len(got) != 1 {
		t.Fatalf("findings = %+v, want the aliased call", got)
	}
}

func TestAtomicWriteLeavesAShadowedPackageAlone(t *testing.T) {
	src := `package sessionstore
import "os"
type writer struct{}
func (w writer) WriteFile(path string, b []byte, mode int) error { return nil }
func save(os writer, path string, b []byte) error { return os.WriteFile(path, b, 0o600) }`
	if got := atomicWriteFindings(t, "internal/state/sessionstore/x.go", src); len(got) != 0 {
		t.Fatalf("findings = %+v, want none when os is a parameter", got)
	}
}
