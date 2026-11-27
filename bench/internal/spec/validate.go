package spec

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// schemasFS is a copy of schema/json made by `just gen`, because go:embed
// cannot reach outside this module. It is generated, never edited.
//
//go:embed schemas
var schemasFS embed.FS

const schemasRoot = "schemas"

// schemaSuffix ends every schema file name: runspec.schema.json is the schema
// named "runspec". The same names are the directory names in schema/examples.
const schemaSuffix = ".schema.json"

// SchemaNames lists the schemas that Validate accepts.
func SchemaNames() []string {
	var names []string
	err := fs.WalkDir(schemasFS, schemasRoot, func(filePath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name, isSchema := strings.CutSuffix(path.Base(filePath), schemaSuffix)
		if isSchema && name != "common" {
			names = append(names, name)
		}
		return nil
	})
	if err != nil {
		panic(fmt.Sprintf("walk embedded schemas: %v", err)) // the embed is fixed at build time
	}
	return names
}

var (
	compileOnce     sync.Once
	compiledSchemas map[string]*jsonschema.Schema
	errCompile      error
)

// compileAll loads every embedded schema once. Schemas reference each other
// (common.schema.json) by $id, so all of them are registered before any is compiled.
func compileAll() (map[string]*jsonschema.Schema, error) {
	compileOnce.Do(func() {
		compiledSchemas, errCompile = compileEmbeddedSchemas()
	})
	return compiledSchemas, errCompile
}

func compileEmbeddedSchemas() (map[string]*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft7)

	idByName := map[string]string{}
	err := fs.WalkDir(schemasFS, schemasRoot, func(filePath string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		content, err := schemasFS.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("read embedded schema %s: %w", filePath, err)
		}
		document, err := jsonschema.UnmarshalJSON(bytes.NewReader(content))
		if err != nil {
			return fmt.Errorf("parse embedded schema %s: %w", filePath, err)
		}
		id := document.(map[string]any)["$id"].(string)
		if err := compiler.AddResource(id, document); err != nil {
			return fmt.Errorf("register embedded schema %s: %w", filePath, err)
		}
		if name, isSchema := strings.CutSuffix(path.Base(filePath), schemaSuffix); isSchema {
			idByName[name] = id
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	compiled := map[string]*jsonschema.Schema{}
	for name, id := range idByName {
		schema, err := compiler.Compile(id)
		if err != nil {
			return nil, fmt.Errorf("compile schema %s: %w", name, err)
		}
		compiled[name] = schema
	}
	return compiled, nil
}

// Parse is the only way to read a schema document into its generated type:
// it validates against the named schema first and unmarshals second. Never
// call json.Unmarshal on a document from a user, a file or the network
// directly: the generated types accept things the schema forbids (a disabled
// TLS block carrying a certificate, unknown fields, object knob values).
func Parse[T any](schemaName string, document []byte) (T, error) {
	var parsed T
	if err := Validate(schemaName, document); err != nil {
		return parsed, err
	}
	if err := json.Unmarshal(document, &parsed); err != nil {
		return parsed, fmt.Errorf("decode %s document: %w", schemaName, err)
	}
	return parsed, nil
}

// ParseRunSpec validates and decodes a RunSpec given as JSON. Callers must
// use it instead of a bare json.Unmarshal into RunSpec. A servtuner 0.1
// (servtuner.dev/v1) RunSpec is an error that says what to change; data
// servtuner wrote itself goes through UpgradeLegacyRunSpec first.
func ParseRunSpec(document []byte) (RunSpec, error) {
	if err := legacyRunSpecError(document); err != nil {
		return RunSpec{}, err
	}
	return Parse[RunSpec](SchemaRunSpec, document)
}

// Validate checks one JSON document against the named schema (SchemaRunSpec,
// "manifest", ...). The returned error lists every violation.
func Validate(schemaName string, document []byte) error {
	schemas, err := compileAll()
	if err != nil {
		return err
	}
	schema, found := schemas[schemaName]
	if !found {
		return fmt.Errorf("unknown schema %q", schemaName)
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(document))
	if err != nil {
		return fmt.Errorf("parse %s document: %w", schemaName, err)
	}
	if err := schema.Validate(value); err != nil {
		return fmt.Errorf("validate %s document: %w", schemaName, err)
	}
	return nil
}
