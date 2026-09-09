// SPDX-License-Identifier: Apache-2.0

package configoracle

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	engineModulePath = "github.com/mcuadros/director-engine"
	oracleImportPath = engineModulePath + "/internal/testkit/configoracle"
)

func parsedImports(t *testing.T, path string) []string {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	imports := make([]string, 0, len(parsed.Imports))
	for _, specification := range parsed.Imports {
		value, err := strconv.Unquote(specification.Path.Value)
		if err != nil {
			t.Fatalf("unquote import in %s: %v", path, err)
		}
		imports = append(imports, value)
	}
	return imports
}

func TestStaticNoRuntimeImports(t *testing.T) {
	packageRoot, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	engineRoot := filepath.Clean(filepath.Join(packageRoot, "..", "..", ".."))
	err = filepath.WalkDir(engineRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		relative, err := filepath.Rel(engineRoot, path)
		if err != nil {
			return err
		}
		insideOracle := filepath.Dir(path) == packageRoot
		productionSource := !strings.HasSuffix(path, "_test.go")
		for _, imported := range parsedImports(t, path) {
			if insideOracle && (imported == engineModulePath || strings.HasPrefix(imported, engineModulePath+"/")) {
				t.Errorf("oracle source %s imports production runtime package %s", filepath.Base(path), imported)
			}
			if !insideOracle && productionSource && imported == oracleImportPath {
				t.Errorf("production runtime source %s imports test-only oracle", filepath.ToSlash(relative))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan production Go source: %v", err)
	}
}

func TestStaticGuardRejectsRuntimeImportSyntax(t *testing.T) {
	source := `package mutation
import oracle "github.com/mcuadros/director-engine/internal/testkit/configoracle"
var _ = oracle.ScopeTask
`
	parsed, err := parser.ParseFile(token.NewFileSet(), "mutation.go", source, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, specification := range parsed.Imports {
		value, err := strconv.Unquote(specification.Path.Value)
		if err != nil {
			t.Fatal(err)
		}
		if value == oracleImportPath {
			found = true
		}
	}
	if !found {
		t.Fatal("static guard mutation survived: oracle import was not detected")
	}
	if len(parsed.Decls) == 0 || !hasImportDeclaration(parsed) {
		t.Fatal("static guard mutation did not retain import declaration")
	}
}

func hasImportDeclaration(file *ast.File) bool {
	for _, declaration := range file.Decls {
		if general, ok := declaration.(*ast.GenDecl); ok && general.Tok == token.IMPORT {
			return true
		}
	}
	return false
}
