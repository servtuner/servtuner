package workload

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// GeneratorName and GeneratorVersion identify the generator in
// environment.json. Bump GeneratorVersion whenever a change alters the
// requests produced for the same workload definition: the version is part of
// the hash, so RPS is never compared across different generators.
const (
	GeneratorName    = "servtuner-synthetic"
	GeneratorVersion = "1"
)

// generatorID returns the "name/version" string recorded in environment.json.
func generatorID() string { return GeneratorName + "/" + GeneratorVersion }

// canonicalWorkload is the hashed form. It is a struct, not a map, so the
// field order is fixed; it covers exactly what shapes the requests and
// nothing that is calibrated at run time (the chars-per-token ratio).
type canonicalWorkload struct {
	Generator    string  `json:"generator"`
	API          string  `json:"api"`
	Streaming    bool    `json:"streaming"`
	DatasetType  string  `json:"dataset_type"`
	InputMean    float64 `json:"input_tokens_mean"`
	InputStddev  float64 `json:"input_tokens_stddev"`
	OutputMean   float64 `json:"output_tokens_mean"`
	OutputStddev float64 `json:"output_tokens_stddev"`
	Seed         int     `json:"seed"`
	SetMinTokens bool    `json:"set_min_tokens"`
	// PaddingBytes is omitted at 0, so workloads without padding keep the
	// hashes they had before it existed.
	PaddingBytes int `json:"padding_bytes,omitempty"`
	// Conversation is omitted for single-turn workloads, which keep their
	// hashes.
	Conversation *canonicalConversation `json:"conversation,omitempty"`
	// SharedPrefix is omitted without one, so existing workloads keep their
	// hashes.
	SharedPrefix *spec.SharedPrefix `json:"shared_prefix,omitempty"`
}

type canonicalConversation struct {
	Turns      int `json:"turns"`
	Concurrent int `json:"concurrent"`
}

// Hash returns the hex sha256 of the canonical JSON of the workload
// definition plus the generator version. The same definition always gives the
// same hash; RPS is compared only between runs with equal hashes.
// Options.SetMinTokens is hashed because it changes the output lengths the
// server produces; the cache salt is not, since it changes no distribution.
func Hash(workload spec.Workload, options Options) (string, error) {
	api, err := resolveAPI(workload.Api)
	if err != nil {
		return "", err
	}
	if workload.Dataset == nil {
		return "", errNoDataset
	}
	canonical, err := json.Marshal(canonicalWorkload{
		Generator:    generatorID(),
		API:          string(api),
		Streaming:    workload.Streaming,
		DatasetType:  string(workload.Dataset.Type),
		InputMean:    workload.Dataset.InputTokens.Mean,
		InputStddev:  workload.Dataset.InputTokens.Stddev,
		OutputMean:   workload.Dataset.OutputTokens.Mean,
		OutputStddev: workload.Dataset.OutputTokens.Stddev,
		Seed:         workload.Seed,
		SetMinTokens: options.SetMinTokens,
		PaddingBytes: workload.Dataset.PaddingBytes,
		Conversation: canonicalConversationOf(workload.Conversation),
		SharedPrefix: workload.Dataset.SharedPrefix,
	})
	if err != nil {
		return "", fmt.Errorf("canonicalize workload: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

func canonicalConversationOf(config *spec.Conversation) *canonicalConversation {
	if config == nil {
		return nil
	}
	concurrent := config.Concurrent
	if concurrent <= 0 {
		concurrent = defaultConcurrentConversations
	}
	return &canonicalConversation{Turns: config.Turns, Concurrent: concurrent}
}
