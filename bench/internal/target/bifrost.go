package target

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// Bifrost management API paths for config capture. /api/version needs no authentication; the others need the admin
// credentials when Bifrost's auth_config is enabled.
const (
	bifrostVersionPath   = "/api/version"
	bifrostConfigPath    = "/api/config"
	bifrostProvidersPath = "/api/providers"

	// Names of the redacted documents for target/config/.
	BifrostConfigDocument    = "bifrost-config.json"
	BifrostProvidersDocument = "bifrost-providers.json"
)

type bifrostAdapter struct {
	kubeReader
}

func newBifrostAdapter() TargetAdapter {
	return bifrostAdapter{kubeReader{spec.TargetTypeBifrost}}
}

// DefaultCatalogs implements TargetAdapter.
func (bifrostAdapter) DefaultCatalogs() []string { return []string{CatalogBifrost, CatalogKubernetes} }

// KnobsFromWorkload implements TargetAdapter. Bifrost is configured through its
// config API, not container args, so there is nothing to read here.
func (bifrostAdapter) KnobsFromWorkload(spec.Workload_2) Knobs { return Knobs{} }

// Info implements TargetAdapter. It runs before and after the load, so a config
// change during the run shows up as a difference between the two (config
// drift). Knobs are read from the raw responses and the stored
// documents are redacted copies.
func (bifrostAdapter) Info(ctx context.Context, endpoint Endpoint) (Info, error) {
	info := Info{Knobs: Knobs{}, ConfigDocuments: map[string][]byte{}}
	var problems []error

	if err := fetchBifrostVersion(ctx, endpoint, &info); err != nil {
		problems = append(problems, err)
	}
	configuration, err := endpoint.getJSON(ctx, bifrostConfigPath)
	if err != nil {
		problems = append(problems, err)
	} else {
		readBifrostClientKnobs(configuration, info.Knobs)
		problems = append(problems, storeRedacted(&info, BifrostConfigDocument, configuration))
	}
	providers, err := endpoint.getJSON(ctx, bifrostProvidersPath)
	if err != nil {
		problems = append(problems, err)
	} else {
		readBifrostProviderKnobs(providers, info.Knobs)
		problems = append(problems, storeRedacted(&info, BifrostProvidersDocument, providers))
	}
	return info, errors.Join(problems...)
}

// fetchBifrostVersion accepts the version as a bare JSON string, an object with
// a "version" field, or plain text: the response shape is not pinned in the
// research.
func fetchBifrostVersion(ctx context.Context, endpoint Endpoint, info *Info) error {
	body, err := endpoint.get(ctx, bifrostVersionPath)
	if err != nil {
		return err
	}
	var decoded any
	version := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &decoded) == nil {
		switch typed := decoded.(type) {
		case string:
			version = typed
		case map[string]any:
			version, _ = typed["version"].(string)
		}
	}
	if version != "" {
		info.TargetInfo.Version = &version
	}
	return nil
}

func storeRedacted(info *Info, name string, document any) error {
	encoded, err := json.MarshalIndent(RedactJSON(document), "", "  ")
	if err != nil {
		return err
	}
	info.ConfigDocuments[name] = append(encoded, '\n')
	return nil
}

// readBifrostClientKnobs reads client_config.initial_pool_size.
func readBifrostClientKnobs(configuration any, knobs Knobs) {
	root, _ := configuration.(map[string]any)
	client, _ := root["client_config"].(map[string]any)
	if value, ok := knobFromJSON(client["initial_pool_size"], knobNumber); ok {
		knobs[KnobBifrostInitialPoolSize] = value
	}
}

// readBifrostProviderKnobs reads concurrency and buffer size, which Bifrost
// sets per provider (concurrency_and_buffer_size). A knob exists only when all
// providers agree: with different values the RunSpec's single knob has no
// single actual value, so it stays unverified.
func readBifrostProviderKnobs(providers any, knobs Knobs) {
	var perProvider []Knobs
	for _, provider := range providerEntries(providers) {
		sizing, _ := provider["concurrency_and_buffer_size"].(map[string]any)
		set := Knobs{}
		if value, ok := knobFromJSON(sizing["concurrency"], knobNumber); ok {
			set[KnobBifrostConcurrency] = value
		}
		if value, ok := knobFromJSON(sizing["buffer_size"], knobNumber); ok {
			set[KnobBifrostBufferSize] = value
		}
		perProvider = append(perProvider, set)
	}
	for name, value := range requireOnEvery(perProvider) {
		knobs[name] = value
	}
}

// providerEntries accepts the provider list as {"providers": [..]}, a bare
// array, or a map keyed by provider name.
func providerEntries(document any) []map[string]any {
	if root, ok := document.(map[string]any); ok {
		if listed, ok := root["providers"]; ok {
			return providerEntries(listed)
		}
		var entries []map[string]any
		for _, name := range sortedKeys(root) {
			if entry, ok := root[name].(map[string]any); ok {
				entries = append(entries, entry)
			}
		}
		return entries
	}
	var entries []map[string]any
	if list, ok := document.([]any); ok {
		for _, element := range list {
			if entry, ok := element.(map[string]any); ok {
				entries = append(entries, entry)
			}
		}
	}
	return entries
}
