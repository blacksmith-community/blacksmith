package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"testing"
)

// TestMakefileLdflagsNameMainVariables guards against -X flags that name a
// variable main does not declare, which the linker ignores without a word.
func TestMakefileLdflagsNameMainVariables(t *testing.T) {
	makefile, err := os.ReadFile("Makefile")
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}

	targets := regexp.MustCompile(`-X main\.(\w+)=`).FindAllStringSubmatch(string(makefile), -1)
	if len(targets) == 0 {
		t.Fatal("Makefile sets no -X main.<name> flags")
	}

	file, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}

	declared := map[string]bool{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			for _, name := range spec.(*ast.ValueSpec).Names {
				declared[name.Name] = true
			}
		}
	}

	for _, target := range targets {
		if !declared[target[1]] {
			t.Errorf("Makefile sets main.%s, but main.go declares no such variable", target[1])
		}
	}
}
