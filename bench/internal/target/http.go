package target

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// maxResponseBytes bounds what Info reads from a target: its config and metrics
// pages are well under this, and an endpoint that streams forever must not
// exhaust memory.
const maxResponseBytes = 16 << 20

// errNotFound is returned for a 404, which for optional endpoints (vLLM
// /server_info outside dev mode) means "not exposed", not a failure.
var errNotFound = errors.New("not found")

// rootURL is the target's server root: the RunSpec endpoint without a trailing
// slash or /v1 (the endpoints Info reads are under both).
func rootURL(baseURL string) string {
	return strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/v1")
}

// get fetches path under the server root. Errors carry the path and status,
// never the response body or the Authorization header.
func (e Endpoint) get(ctx context.Context, path string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rootURL(e.BaseURL)+path, nil)
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", path, err)
	}
	if e.Authorization != "" {
		request.Header.Set("Authorization", e.Authorization)
	}
	response, err := e.Client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("GET %s: %w", path, errNotFound)
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, fmt.Errorf("GET %s: status %d", path, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read response of GET %s: %w", path, err)
	}
	return body, nil
}

// getJSON fetches path and decodes the body into a generic document.
func (e Endpoint) getJSON(ctx context.Context, path string) (any, error) {
	body, err := e.get(ctx, path)
	if err != nil {
		return nil, err
	}
	var document any
	if err := json.Unmarshal(body, &document); err != nil {
		// The decoder error can quote the offending bytes, which may be secret.
		return nil, fmt.Errorf("decode response of GET %s: not valid JSON", path)
	}
	return document, nil
}

// modelIDs reads the ids from an OpenAI model list.
func modelIDs(document any) []string {
	listing, _ := document.(map[string]any)
	entries, _ := listing["data"].([]any)
	var ids []string
	for _, entry := range entries {
		model, _ := entry.(map[string]any)
		if id, ok := model["id"].(string); ok && id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// fetchModels reads /v1/models into info.
func fetchModels(ctx context.Context, endpoint Endpoint, info *Info) error {
	document, err := endpoint.getJSON(ctx, "/v1/models")
	if err != nil {
		return err
	}
	info.TargetInfo.Models = modelIDs(document)
	return nil
}
