package loadtest

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

func assertPackageImports(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	fileSet := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		parsed, err := parser.ParseFile(fileSet, entry.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		for _, imported := range parsed.Imports {
			path, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatalf("import path %s: %v", imported.Path.Value, err)
			}
			if forbiddenImport(path) {
				t.Errorf("%s imports %s", entry.Name(), path)
			}
		}
	}
}

func forbiddenImport(path string) bool {
	switch {
	case path == "github.com/HelixThinker/pod-rightsizer/pkg/loadtest":
		return true
	case strings.HasSuffix(path, "/providers/loadtest/local"):
		return true
	case path == "os/exec":
		return true
	case strings.Contains(strings.ToLower(path), "k6"):
		return true
	default:
		return false
	}
}
