// Command contractcheck validates contracts/v1 JSON Schemas and the fixture catalog.
// It is a dev tool. The pod-rightsizer runtime does not import it.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

type catalog struct {
	Fixtures []fixture `json:"fixtures"`
}

type fixture struct {
	ID            string   `json:"id"`
	Schema        string   `json:"schema"`
	Document      string   `json:"document"`
	Expect        string   `json:"expect"`
	ErrorContains []string `json:"errorContains"`
}

func main() {
	rootFlag := flag.String("root", "", "repository root (default: walk up from the working directory)")
	flag.Parse()

	root, err := findRoot(*rootFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "contractcheck: %v\n", err)
		os.Exit(2)
	}

	schemaDir := filepath.Join(root, "contracts", "v1")
	fixtureRoot := filepath.Join(root, "testdata", "contracts", "v1")
	if err := checkLocalRefs(schemaDir); err != nil {
		fmt.Fprintf(os.Stderr, "contractcheck: %v\n", err)
		os.Exit(1)
	}

	schemas, err := compileSchemas(schemaDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "contractcheck: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("compiled %d schemas with local $ref only\n", len(schemas))

	cat, err := readCatalog(filepath.Join(fixtureRoot, "catalog.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "contractcheck: %v\n", err)
		os.Exit(1)
	}
	if err := checkCatalogCoverage(fixtureRoot, cat); err != nil {
		fmt.Fprintf(os.Stderr, "contractcheck: %v\n", err)
		os.Exit(1)
	}

	failed := 0
	for _, item := range cat.Fixtures {
		if err := checkFixture(fixtureRoot, schemas, item); err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "FAIL %s: %v\n", item.ID, err)
			continue
		}
		fmt.Printf("ok   %s (%s)\n", item.ID, item.Expect)
	}
	if failed > 0 {
		fmt.Fprintf(os.Stderr, "contractcheck: %d fixture(s) failed\n", failed)
		os.Exit(1)
	}
	fmt.Printf("contract fixtures passed (%d)\n", len(cat.Fixtures))
}

func findRoot(explicit string) (string, error) {
	if explicit != "" {
		return filepath.Abs(explicit)
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "contracts", "v1")); statErr == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("cannot find contracts/v1 from %s", dir)
		}
		dir = parent
	}
}

func checkLocalRefs(schemaDir string) error {
	entries, err := os.ReadDir(schemaDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".schema.json") {
			continue
		}
		path := filepath.Join(schemaDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var doc any
		if err := json.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("%s: %w", entry.Name(), err)
		}
		if err := walkRefs(entry.Name(), doc); err != nil {
			return err
		}
	}
	return nil
}

func walkRefs(name string, doc any) error {
	switch typed := doc.(type) {
	case map[string]any:
		for key, value := range typed {
			if key == "$ref" {
				ref, ok := value.(string)
				if !ok {
					return fmt.Errorf("%s: $ref must be a string", name)
				}
				if strings.Contains(ref, "://") {
					return fmt.Errorf("%s: remote $ref %q is not allowed", name, ref)
				}
			}
			if err := walkRefs(name, value); err != nil {
				return err
			}
		}
	case []any:
		for _, value := range typed {
			if err := walkRefs(name, value); err != nil {
				return err
			}
		}
	}
	return nil
}

func compileSchemas(schemaDir string) (map[string]*jsonschema.Schema, error) {
	entries, err := os.ReadDir(schemaDir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".schema.json") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)

	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	compiler.UseLoader(jsonschema.SchemeURLLoader{
		"file": jsonschema.FileLoader{},
	})
	for _, name := range names {
		path := filepath.Join(schemaDir, name)
		doc, err := readSchema(path)
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", name, err)
		}
		if err := compiler.AddResource(path, doc); err != nil {
			return nil, fmt.Errorf("add %s: %w", name, err)
		}
	}
	compiled := make(map[string]*jsonschema.Schema, len(names))
	for _, name := range names {
		schema, err := compiler.Compile(filepath.Join(schemaDir, name))
		if err != nil {
			return nil, fmt.Errorf("compile %s: %w", name, err)
		}
		compiled[name] = schema
	}
	return compiled, nil
}

func readSchema(path string) (any, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return jsonschema.UnmarshalJSON(file)
}

func readCatalog(path string) (catalog, error) {
	file, err := os.Open(path)
	if err != nil {
		return catalog{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var cat catalog
	if err := decoder.Decode(&cat); err != nil {
		return catalog{}, fmt.Errorf("catalog: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return catalog{}, fmt.Errorf("catalog: trailing data")
	}
	seen := map[string]struct{}{}
	for _, item := range cat.Fixtures {
		if item.ID == "" || item.Schema == "" || item.Document == "" {
			return catalog{}, fmt.Errorf("catalog entry missing id, schema, or document")
		}
		if item.Expect != "valid" && item.Expect != "invalid" {
			return catalog{}, fmt.Errorf("%s: expect must be valid or invalid", item.ID)
		}
		if _, ok := seen[item.ID]; ok {
			return catalog{}, fmt.Errorf("duplicate fixture id %s", item.ID)
		}
		seen[item.ID] = struct{}{}
		if item.Expect == "invalid" && len(item.ErrorContains) == 0 {
			return catalog{}, fmt.Errorf("%s: invalid fixtures must set errorContains", item.ID)
		}
		if item.Expect == "valid" && len(item.ErrorContains) != 0 {
			return catalog{}, fmt.Errorf("%s: valid fixtures must not set errorContains", item.ID)
		}
	}
	return cat, nil
}

func checkCatalogCoverage(fixtureRoot string, cat catalog) error {
	listed := map[string]struct{}{}
	for _, item := range cat.Fixtures {
		listed[filepath.Clean(item.Document)] = struct{}{}
	}
	var missing []string
	err := filepath.WalkDir(fixtureRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			return nil
		}
		rel, err := filepath.Rel(fixtureRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "catalog.json" {
			return nil
		}
		if _, ok := listed[rel]; !ok {
			missing = append(missing, rel)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		return fmt.Errorf("JSON fixtures missing from catalog.json: %s", strings.Join(missing, ", "))
	}
	for _, item := range cat.Fixtures {
		if _, err := os.Stat(filepath.Join(fixtureRoot, item.Document)); err != nil {
			return fmt.Errorf("%s: %w", item.ID, err)
		}
	}
	return nil
}

func checkFixture(fixtureRoot string, schemas map[string]*jsonschema.Schema, item fixture) error {
	schema, ok := schemas[item.Schema]
	if !ok {
		return fmt.Errorf("unknown schema %s", item.Schema)
	}
	file, err := os.Open(filepath.Join(fixtureRoot, item.Document))
	if err != nil {
		return err
	}
	defer file.Close()
	instance, err := jsonschema.UnmarshalJSON(file)
	if err != nil {
		return err
	}
	err = schema.Validate(instance)
	if item.Expect == "valid" {
		if err != nil {
			return fmt.Errorf("expected valid, got: %v", err)
		}
		return nil
	}
	if err == nil {
		return fmt.Errorf("expected invalid, but document is valid")
	}
	message := err.Error()
	for _, fragment := range item.ErrorContains {
		if !strings.Contains(message, fragment) {
			return fmt.Errorf("error does not contain %q\n%s", fragment, message)
		}
	}
	return nil
}
