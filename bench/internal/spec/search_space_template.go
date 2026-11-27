package spec

import "embed"

// spacesFS is a copy of schema/search-spaces made by `just gen`, because go:embed cannot reach
// outside this module. It is generated, never edited.
//
//go:embed search-spaces
var spacesFS embed.FS

const spacesRoot = "search-spaces"

// SearchSpaceTemplates returns the starting search spaces built into bench, sorted by
// name then version, each validated against search-space-template.schema.json. servtuner propose's
// own rules are checked by a test in internal/propose, so a template always starts a session.
func SearchSpaceTemplates() ([]SearchSpaceTemplate, error) {
	return loadCatalog(spacesFS, spacesRoot, SchemaSearchSpaceTemplate,
		func(template SearchSpaceTemplate) (string, int) { return template.Name, template.Version })
}
