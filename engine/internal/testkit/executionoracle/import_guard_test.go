// SPDX-License-Identifier: Apache-2.0

package executionoracle

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
	oracleImportPath = engineModulePath + "/internal/testkit/executionoracle"
)

var allowedOracleImports = map[string]bool{
	"errors":    true,
	"math/bits": true,
	"slices":    true,
	"sync":      true,
}

func TestStaticExecutionOracleImportAndPlatformGuards(t *testing.T) {
	packageRoot, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	engineRoot := filepath.Clean(filepath.Join(packageRoot, "..", "..", ".."))
	err = filepath.WalkDir(engineRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" {
			return nil
		}
		relative, err := filepath.Rel(engineRoot, path)
		if err != nil {
			return err
		}
		insideOracle := filepath.Dir(path) == packageRoot
		testSource := strings.HasSuffix(path, "_test.go")
		imports, platformMarker, err := sourceImports(path)
		if err != nil {
			return err
		}
		if insideOracle && !testSource {
			for _, imported := range imports {
				if !allowedOracleImports[imported] {
					t.Errorf("oracle production source %s imports authority/effect package %s", filepath.Base(path), imported)
				}
			}
			if platformMarker {
				t.Errorf("oracle production source %s contains a non-Linux platform build marker", filepath.Base(path))
			}
		}
		for _, imported := range imports {
			if insideOracle && strings.HasPrefix(imported, engineModulePath+"/") {
				t.Errorf("oracle source %s imports production or reciprocal testkit package %s", filepath.Base(path), imported)
			}
			if !insideOracle && imported == oracleImportPath {
				if !testSource {
					t.Errorf("runtime source %s imports test-only execution oracle", filepath.ToSlash(relative))
				}
				if strings.HasPrefix(filepath.ToSlash(relative), "internal/testkit/") || strings.HasPrefix(filepath.ToSlash(relative), "internal/planningtestkit/") {
					t.Errorf("reciprocal testkit source %s imports execution oracle", filepath.ToSlash(relative))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan Go source: %v", err)
	}
}

func TestStaticGuardMutationsAreRejected(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		source string
	}{
		{name: "runtime import", path: "application/execution/runtime.go", source: "package execution\nimport _ \"" + oracleImportPath + "\"\n"},
		{name: "reciprocal testkit", path: "internal/testkit/other/oracle_test.go", source: "package other\nimport _ \"" + oracleImportPath + "\"\n"},
		{name: "platform build", path: "internal/testkit/executionoracle/model.go", source: "//go:build windows\npackage executionoracle\n"},
		{name: "host authority", path: "internal/testkit/executionoracle/fake.go", source: "package executionoracle\nimport _ \"os/exec\"\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if errors := staticGuardErrors(test.path, test.source); len(errors) == 0 {
				t.Fatalf("static guard mutation survived: %s", test.source)
			}
		})
	}
	if errors := staticGuardErrors("application/execution/oracle_test.go", "package execution\nimport _ \""+oracleImportPath+"\"\n"); len(errors) != 0 {
		t.Fatalf("exact test-source consumer rejected: %v", errors)
	}
}

func staticGuardErrors(relative, source string) []string {
	parsed, err := parser.ParseFile(token.NewFileSet(), relative, source, parser.ImportsOnly)
	if err != nil {
		return []string{"parse"}
	}
	insideOracle := filepath.ToSlash(filepath.Dir(relative)) == "internal/testkit/executionoracle"
	testSource := strings.HasSuffix(relative, "_test.go")
	var result []string
	for _, specification := range parsed.Imports {
		imported, err := strconv.Unquote(specification.Path.Value)
		if err != nil {
			return append(result, "unquote")
		}
		if insideOracle && !testSource && !allowedOracleImports[imported] {
			result = append(result, "oracle authority import")
		}
		if insideOracle && strings.HasPrefix(imported, engineModulePath+"/") {
			result = append(result, "oracle reciprocal import")
		}
		if !insideOracle && imported == oracleImportPath && (!testSource || strings.HasPrefix(filepath.ToSlash(relative), "internal/testkit/") || strings.HasPrefix(filepath.ToSlash(relative), "internal/planningtestkit/")) {
			result = append(result, "invalid oracle consumer")
		}
	}
	if insideOracle && !testSource {
		for _, prohibited := range []string{"//go:build windows", "runtime.GOOS", "golang.org/x/sys/windows", "os/exec", "database/sql", "@getpaseo", "github.com/getpaseo"} {
			if strings.Contains(source, prohibited) {
				result = append(result, "scope or authority marker")
			}
		}
	}
	return result
}

func sourceImports(path string) ([]string, bool, error) {
	parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
	if err != nil {
		return nil, false, err
	}
	imports := make([]string, 0, len(parsed.Imports))
	for _, specification := range parsed.Imports {
		value, err := strconv.Unquote(specification.Path.Value)
		if err != nil {
			return nil, false, err
		}
		imports = append(imports, value)
	}
	platformMarker := false
	for _, group := range parsed.Comments {
		for _, comment := range group.List {
			if strings.Contains(comment.Text, "go:build windows") {
				platformMarker = true
			}
		}
	}
	ast.Inspect(parsed, func(node ast.Node) bool {
		literal, ok := node.(*ast.BasicLit)
		if ok && strings.Contains(literal.Value, `:\\`) {
			platformMarker = true
		}
		return true
	})
	return imports, platformMarker, nil
}
