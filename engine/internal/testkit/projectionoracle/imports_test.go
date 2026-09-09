// SPDX-License-Identifier: Apache-2.0

package projectionoracle

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const oracleImportPath = "github.com/mcuadros/director-engine/internal/testkit/projectionoracle"

func TestNoRuntimeImportsAndNoProductionConsumer(t *testing.T) {
	packageDir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("locate oracle package: %v", err)
	}
	wantSuffix := filepath.Join("engine", "internal", "testkit", "projectionoracle")
	if !strings.HasSuffix(packageDir, wantSuffix) {
		t.Fatalf("oracle directory = %q, want suffix %q", packageDir, wantSuffix)
	}
	engineDir := filepath.Clean(filepath.Join(packageDir, "..", "..", ".."))

	err = filepath.WalkDir(engineDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" {
			return nil
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Errorf("parse %s: %v", path, err)
			return nil
		}
		for _, spec := range parsed.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Errorf("unquote import in %s: %v", path, err)
				continue
			}
			insideOracle := filepath.Clean(filepath.Dir(path)) == filepath.Clean(packageDir)
			if insideOracle && !strings.HasSuffix(path, "_test.go") && strings.HasPrefix(importPath, "github.com/mcuadros/director-engine/") {
				t.Errorf("oracle runtime source %s imports engine package %q", path, importPath)
			}
			if !insideOracle && !strings.HasSuffix(path, "_test.go") && importPath == oracleImportPath {
				t.Errorf("production source %s imports test-only oracle", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestOracleRuntimeFilesHavePackageDocumentation(t *testing.T) {
	packageDir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("locate oracle package: %v", err)
	}
	docPath := filepath.Join(packageDir, "doc.go")
	parsed, err := parser.ParseFile(token.NewFileSet(), docPath, nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Doc == nil || !strings.Contains(parsed.Doc.Text(), "test-only") {
		t.Fatal("package documentation does not declare the test-only boundary")
	}
	if parsed.Name.Name != "projectionoracle" {
		t.Fatal("unexpected oracle package identity")
	}
}
