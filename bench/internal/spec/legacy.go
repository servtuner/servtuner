package spec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// servtuner 0.1 wrote RunSpecs as servtuner.dev/v1, with tls.mode mtls, tls or
// platform on every hop, and recorded each hop's transport in environment.json
// as that mode string (bundle format version 1). 0.2 replaced the mode with
// tls.enabled and tls.clientCert (RunSpec servtuner.dev/v2, bundle format 2).
//
// Two rules follow. A RunSpec someone submits must be v2: ParseRunSpec rejects
// v1 and says what to change (legacyRunSpecError). Data servtuner wrote before
// is still read: a bundle's RunSpec and environment.json are upgraded when
// they are loaded (UpgradeLegacyRunSpec, UpgradeLegacyEnvironment), and raw
// files are never rewritten.

// APIVersionV1 is servtuner 0.1's RunSpec apiVersion.
const APIVersionV1 = "servtuner.dev/v1"

// legacyTransports maps a 0.1 mode to the hop transport it recorded.
var legacyTransports = map[string]HopTransport{
	"mtls":     {Tls: true, ClientCert: true},
	"tls":      {Tls: true, ClientCert: false},
	"platform": {Tls: false, ClientCert: false},
}

// upgradeLegacyTLS turns a 0.1 tls block into 0.2's: mtls with secretRef S
// becomes enabled true with clientCert {secretRef: S}, tls becomes enabled
// true (caSecretRef kept), platform becomes enabled false. Other keys are kept,
// so a block that was invalid stays invalid. false for an unknown mode.
func upgradeLegacyTLS(block map[string]any) (map[string]any, bool) {
	mode, _ := block["mode"].(string)
	if _, known := legacyTransports[mode]; !known {
		return nil, false
	}
	upgraded := map[string]any{"enabled": mode != "platform"}
	for key, value := range block {
		switch {
		case key == "mode":
		case key == "secretRef" && mode == "mtls":
			upgraded["clientCert"] = map[string]any{"secretRef": value}
		default:
			upgraded[key] = value
		}
	}
	return upgraded, true
}

// upgradeTLSBlocks rewrites every 0.1 tls block under value in place and
// returns the paths it changed, with the blocks before and after.
func upgradeTLSBlocks(value any, path string, changes *[]tlsChange) {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			if block, isBlock := child.(map[string]any); isBlock && key == "tls" {
				if _, hasMode := block["mode"]; hasMode {
					if upgraded, ok := upgradeLegacyTLS(block); ok {
						*changes = append(*changes, tlsChange{path: childPath, before: block, after: upgraded})
						node[key] = upgraded
						continue
					}
				}
			}
			upgradeTLSBlocks(child, childPath, changes)
		}
	case []any:
		for index, child := range node {
			upgradeTLSBlocks(child, fmt.Sprintf("%s[%d]", path, index), changes)
		}
	}
}

type tlsChange struct {
	path          string
	before, after map[string]any
}

// UpgradeLegacyRunSpec returns a RunSpec read from data servtuner wrote (a
// bundle's spec, a stored run) in the current shape: a v1 document becomes v2,
// with each tls block upgraded. Any other document is returned unchanged. It
// is never applied to a RunSpec someone submits.
func UpgradeLegacyRunSpec(document []byte) ([]byte, error) {
	upgraded, _, err := upgradeRunSpec(document)
	return upgraded, err
}

func upgradeRunSpec(document []byte) ([]byte, []tlsChange, error) {
	if !isLegacyRunSpec(document) {
		return document, nil, nil
	}
	var tree map[string]any
	if err := json.Unmarshal(document, &tree); err != nil {
		return nil, nil, fmt.Errorf("read servtuner.dev/v1 RunSpec: %w", err)
	}
	tree["apiVersion"] = APIVersionV2
	var changes []tlsChange
	upgradeTLSBlocks(tree, "", &changes)
	upgraded, err := json.Marshal(tree)
	if err != nil {
		return nil, nil, fmt.Errorf("write upgraded RunSpec: %w", err)
	}
	return upgraded, changes, nil
}

// APIVersionV2 is the RunSpec apiVersion this servtuner reads.
const APIVersionV2 = "servtuner.dev/v2"

func isLegacyRunSpec(document []byte) bool {
	var header struct {
		APIVersion string `json:"apiVersion"`
	}
	return json.Unmarshal(document, &header) == nil && header.APIVersion == APIVersionV1
}

// legacyRunSpecError explains a v1 RunSpec someone submitted, naming each tls
// block's replacement. nil for any other document.
func legacyRunSpecError(document []byte) error {
	if !isLegacyRunSpec(document) {
		return nil
	}
	_, changes, err := upgradeRunSpec(document)
	if err != nil {
		return err
	}
	var message strings.Builder
	fmt.Fprintf(&message, "apiVersion %s is servtuner 0.1's RunSpec; this servtuner reads %s, where tls.mode was replaced by tls.enabled and tls.clientCert. Set apiVersion: %s", APIVersionV1, APIVersionV2, APIVersionV2)
	sort.Slice(changes, func(i, j int) bool { return changes[i].path < changes[j].path })
	for _, change := range changes {
		before, _ := json.Marshal(change.before)
		after, _ := json.Marshal(change.after)
		fmt.Fprintf(&message, "; %s: %s instead of %s", change.path, after, before)
	}
	return fmt.Errorf("%s", message.String())
}

// UpgradeLegacyEnvironment returns an environment.json from a bundle of format
// version 1, where each hop's transport was a mode string, with each mode
// turned into {tls, client_cert}. Any other document is returned unchanged.
func UpgradeLegacyEnvironment(document []byte) ([]byte, error) {
	var tree map[string]any
	if err := json.Unmarshal(document, &tree); err != nil {
		return document, nil // not JSON: schema validation reports it
	}
	hops, isMap := tree["transport"].(map[string]any)
	if !isMap {
		return document, nil
	}
	changed := false
	for hop, value := range hops {
		mode, isString := value.(string)
		if !isString {
			continue
		}
		transport, known := legacyTransports[mode]
		if !known {
			continue // left for schema validation to reject
		}
		hops[hop] = map[string]any{"tls": transport.Tls, "client_cert": transport.ClientCert}
		changed = true
	}
	if !changed {
		return document, nil
	}
	var upgraded bytes.Buffer
	encoder := json.NewEncoder(&upgraded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(tree); err != nil {
		return nil, fmt.Errorf("write upgraded environment: %w", err)
	}
	return bytes.TrimSpace(upgraded.Bytes()), nil
}

// UpgradeBundleDocument upgrades a document read from a bundle to the current
// shape, by schema: a servtuner.dev/v1 RunSpec and a format-1 environment.json.
// Every other document is returned unchanged. Readers of bundles call it before
// Parse; raw files are never rewritten.
func UpgradeBundleDocument(schemaName string, document []byte) ([]byte, error) {
	switch schemaName {
	case SchemaRunSpec:
		return UpgradeLegacyRunSpec(document)
	case SchemaEnvironment:
		return UpgradeLegacyEnvironment(document)
	}
	return document, nil
}
