package spec

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
)

// catalogFileName matches <name>.v<version>.yaml, the file name of every catalog entry (workload
// presets, starting search spaces).
var catalogFileName = regexp.MustCompile(`^([a-z][a-z0-9-]*)\.v([1-9][0-9]*)\.yaml$`)

// loadCatalog reads every entry of an embedded catalog directory, sorted by name then version.
// Each file is validated against its schema, and the name and version it declares must match its
// file name. Other files (a README) are skipped.
func loadCatalog[Entry any](files embed.FS, root, schema string, identity func(Entry) (string, int)) ([]Entry, error) {
	entries, err := fs.ReadDir(files, root)
	if err != nil {
		return nil, fmt.Errorf("read embedded %s: %w", root, err)
	}
	var catalog []Entry
	for _, entry := range entries {
		match := catalogFileName.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		document, err := files.ReadFile(path.Join(root, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s %s: %w", schema, entry.Name(), err)
		}
		asJSON, err := ToJSON(document)
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", schema, entry.Name(), err)
		}
		parsed, err := Parse[Entry](schema, asJSON)
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", schema, entry.Name(), err)
		}
		name, version := identity(parsed)
		if fileVersion, _ := strconv.Atoi(match[2]); name != match[1] || version != fileVersion {
			return nil, fmt.Errorf("%s %s declares %s v%d", schema, entry.Name(), name, version)
		}
		catalog = append(catalog, parsed)
	}
	sort.SliceStable(catalog, func(i, j int) bool {
		nameI, versionI := identity(catalog[i])
		nameJ, versionJ := identity(catalog[j])
		if nameI != nameJ {
			return nameI < nameJ
		}
		return versionI < versionJ
	})
	return catalog, nil
}
