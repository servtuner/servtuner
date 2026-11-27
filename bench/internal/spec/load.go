package spec

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/goccy/go-yaml"
)

// LoadRunSpec reads a RunSpec given as YAML or JSON. YAML is converted to JSON
// first, and the JSON then goes through ParseRunSpec, the only way to read a
// RunSpec (schema/CLAUDE.md). A workload preset is expanded (ExpandPreset), so
// the caller always sees one explicit workload; an unknown preset, version or
// engine is an error. The returned JSON is the converted document, unexpanded.
func LoadRunSpec(document []byte) (RunSpec, []byte, error) {
	asJSON, err := ToJSON(document)
	if err != nil {
		return RunSpec{}, nil, err
	}
	parsed, err := ParseRunSpec(asJSON)
	if err != nil {
		return RunSpec{}, nil, err
	}
	expanded, err := ExpandPreset(parsed)
	if err != nil {
		return RunSpec{}, nil, err
	}
	return expanded, asJSON, nil
}

// ToJSON turns a YAML or JSON document into JSON.
func ToJSON(document []byte) ([]byte, error) {
	// JSON is YAML, but a document that already is JSON is passed through
	// untouched so that number formatting is never altered.
	if json.Valid(document) {
		return document, nil
	}
	converted, err := yaml.YAMLToJSON(document)
	if err != nil {
		return nil, fmt.Errorf("convert from YAML to JSON: %w", err)
	}
	return bytes.TrimSpace(converted), nil
}
