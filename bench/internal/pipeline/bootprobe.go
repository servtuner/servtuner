package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/target"
	"github.com/servtuner/servtuner/bench/internal/tlsconfig"
)

// errNoCacheConfig: the target serves no vllm:cache_config_info, so its KV
// capacity is unknown. A probe must not guess it.
var errNoCacheConfig = errors.New("the target's /metrics has no vllm:cache_config_info with num_gpu_blocks and block_size")

// BootProbe reads the KV capacity of a vLLM that booted with no load:
// num_gpu_blocks and block_size from
// vllm:cache_config_info on its /metrics, fetched through the target's own
// hop and credentials as a run would, and
// c_kv = num_gpu_blocks x block_size / tokens per request. Only a booted target gets here: one that did not boot is the
// deployer's infeasible result, not a probe.
func BootProbe(ctx context.Context, secretsDir string, request spec.BootProbeRequest) (spec.TuningBootResult, error) {
	if request.Target.Type != spec.TargetTypeVllm {
		return spec.TuningBootResult{}, fmt.Errorf("target.type %s: a boot probe reads vLLM's cache config", request.Target.Type)
	}
	if secretsDir == "" {
		secretsDir = DefaultSecretsDir
	}
	stored := secrets{dir: secretsDir}
	hop, err := stored.hop("target", request.Target.Tls, request.Target.Endpoint)
	if err != nil {
		return spec.TuningBootResult{}, err
	}
	token, err := stored.bearerToken(request.Target.Auth)
	if err != nil {
		return spec.TuningBootResult{}, err
	}
	client, err := tlsconfig.NewHTTPClient(hop, checkRequestTimeout)
	if err != nil {
		return spec.TuningBootResult{}, err
	}
	adapter, err := target.AdapterFor(request.Target.Type)
	if err != nil {
		return spec.TuningBootResult{}, err
	}
	endpoint := target.Endpoint{Client: client, BaseURL: request.Target.Endpoint}
	if token != "" {
		endpoint.Authorization = "Bearer " + token
	}
	// Only the cache config matters here: a failing /version or /v1/models
	// does not make the KV capacity unknown, so their errors count only when
	// the cache config is missing too.
	info, infoErr := adapter.Info(ctx, endpoint)
	blocks, blockOK := wholeNumber(info.TargetInfo.CacheConfig["num_gpu_blocks"])
	blockSize, sizeOK := wholeNumber(info.TargetInfo.CacheConfig["block_size"])
	if !blockOK || !sizeOK {
		return spec.TuningBootResult{}, errors.Join(errNoCacheConfig, infoErr)
	}
	cKV := float64(blocks*blockSize) / request.TokensPerRequest
	return spec.TuningBootResult{
		Knobs:        request.Knobs,
		Outcome:      spec.TuningBootOutcomeFeasible,
		NumGpuBlocks: &blocks,
		CKv:          &cKV,
		// The version that measured this capacity: reusing the boot later needs the same one.
		VllmVersion: info.TargetInfo.Version,
	}, nil
}

// wholeNumber parses a cache_config_info label that holds an integer; vLLM
// leaves num_gpu_blocks as "None" until the KV cache is allocated.
func wholeNumber(text string) (int, bool) {
	value, err := strconv.Atoi(text)
	return value, err == nil && value >= 0
}
