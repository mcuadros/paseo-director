// SPDX-License-Identifier: Apache-2.0

package planningtestkit

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestTestkitIsUnreachableFromProductAndImportsNoProductionPackage(t *testing.T) {
	packageDirectory, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("locate testkit: %v", err)
	}
	moduleRoot := filepath.Clean(filepath.Join(packageDirectory, "..", ".."))
	testkitImport := "github.com/mcuadros/director-engine/internal/planningtestkit"

	err = filepath.WalkDir(moduleRoot, func(path string, entry fs.DirEntry, walkErr error) error {
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
		insideTestkit := filepath.Dir(path) == packageDirectory
		productionSource := !strings.HasSuffix(path, "_test.go")
		for _, spec := range parsed.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Errorf("unquote import in %s: %v", path, err)
				continue
			}
			if insideTestkit && productionSource && strings.HasPrefix(importPath, "github.com/mcuadros/director-engine/") {
				t.Errorf("testkit production file %s imports Director production package %s", filepath.Base(path), importPath)
			}
			if !insideTestkit && productionSource && importPath == testkitImport {
				t.Errorf("product runtime source %s imports the test-only planning kit", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk engine module: %v", err)
	}
}
