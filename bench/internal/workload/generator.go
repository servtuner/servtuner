// Package workload generates the synthetic requests the loadgen engine sends.
// Generation is seeded and deterministic: the
// same RunSpec workload and seed give the same prompts in the same order and
// the same hash. Prompts are sized in characters from a per-model
// chars-per-token ratio that the Calibrator learns from server-reported usage,
// because bench carries no tokenizer.
//
// Cross-run prefix caching: a given seed yields byte-identical prompts on
// every run, so vLLM's automatic prefix caching (on by default) or a router's
// response cache would serve a repeat run from the previous run's warm cache.
// That lowers TTFT silently and makes runs incomparable. Options.CacheSalt
// (the pipeline passes the run ID) puts a run-specific prefix on every prompt
// so no two runs share one; the run stays reproducible from its run ID. The
// salt is not part of the workload hash: it changes no distribution.
package workload

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// Request paths relative to the target endpoint (which ends in /v1).
const (
	PathChat       = "/chat/completions"
	PathCompletion = "/completions"
	PathEmbeddings = "/embeddings"
)

// Request is one generated request, ready for the engine to send.
type Request struct {
	// Index is the position in the deterministic sequence, from 0.
	Index int
	Path  string
	// Body is the JSON request body.
	Body []byte
	// Streaming says whether the body asks for an SSE response.
	Streaming bool
	// PromptChars is the length of the generated prompt text. The engine
	// passes it with the reported prompt_tokens to Calibrator.Observe.
	PromptChars int
	// ExpectedInputTokens and ExpectedOutputTokens are the sampled targets.
	// Output is 0 for embeddings. They are targets, not guarantees: see
	// Options.SetMinTokens.
	ExpectedInputTokens  int
	ExpectedOutputTokens int
}

// Source is what the loadgen engine calls to get the next request. It is safe
// for concurrent use.
type Source interface {
	Next() Request
}

// Options tunes request bodies for the target.
type Options struct {
	// SetMinTokens also sends min_tokens equal to max_tokens, so the output
	// length hits its target. Only vLLM (and servers that copy its sampling
	// params) honour it; others ignore or reject an unknown field, so this is
	// best-effort and off by default. The actual output length is recorded
	// from usage regardless.
	SetMinTokens bool
	// CacheSalt, when set, prefixes every prompt with a short line derived
	// from it, so prompts never share a prefix with another run's (see the
	// package comment). The salt's characters count toward PromptChars and are
	// subtracted from the prompt body, keeping the input token target
	// accurate. It is not hashed.
	CacheSalt string
	// ShardIndex and ShardCount give this generator every ShardCount-th
	// request, starting at ShardIndex (engine.shards): the shards together
	// send exactly the unsharded sequence. Zero ShardCount means 1. Not hashed:
	// the workload is the same however it is split.
	ShardIndex, ShardCount int
}

// Generator is the synthetic Source.
type Generator struct {
	api        spec.WorkloadApi
	streaming  bool
	model      string
	seed       uint64
	input      spec.TokenDistribution
	output     spec.TokenDistribution
	padding    int
	calibrator *Calibrator
	options    Options
	hash       string
	saltPrefix string
	next       atomic.Int64
	// conversations is nil unless workload.conversation is set.
	conversations *conversations
	// prefixes is nil unless dataset.shared_prefix is set.
	prefixes *sharedPrefixes
}

// errNoDataset: a preset is expanded when the RunSpec is loaded, so a workload
// that reaches the generator without a dataset was never expanded.
var errNoDataset = errors.New("workload has no dataset: expand its preset first (spec.LoadRunSpec)")

// sharedPrefixes holds each group's prefix text. A group's text is one seeded
// word stream, and a request's prefix is the first tokens × chars-per-token
// characters of it, at the ratio of that moment. Every cut is a prefix of the
// same stream, so warmup requests, sized before calibration, share their
// leading bytes with the stage requests, which are sized after it and share
// all of theirs: the prefix cache sees real reuse, and the stage prefix has
// its calibrated length (sizing it once, at first use, left it at the
// uncalibrated ratio for the whole run).
type sharedPrefixes struct {
	tokens int
	seed   uint64
	groups []sharedPrefix
}

type sharedPrefix struct {
	mutex sync.Mutex
	// text is the longest cut generated so far; shorter cuts are its prefixes.
	text string
}

// prefixSeedSalt keeps the prefix text streams apart from the per-request
// streams, which are seeded with the request index.
const prefixSeedSalt = 0x5052454649580000 // "PREFIX" in ASCII, then zeros

func newSharedPrefixes(config *spec.SharedPrefix, seed uint64) *sharedPrefixes {
	if config == nil {
		return nil
	}
	return &sharedPrefixes{tokens: config.Tokens, seed: seed ^ prefixSeedSalt, groups: make([]sharedPrefix, config.Groups)}
}

// text returns group's prefix at charsPerToken. randomText draws words in
// order from the group's own stream and stops at the length asked for, so a
// longer cut regenerated from the same seed starts with every shorter one.
func (p *sharedPrefixes) text(group int, charsPerToken float64) string {
	chars := int(math.Round(float64(p.tokens) * charsPerToken))
	entry := &p.groups[group]
	entry.mutex.Lock()
	defer entry.mutex.Unlock()
	if len(entry.text) < chars {
		entry.text = randomText(rand.New(rand.NewPCG(p.seed, uint64(group))), chars)
	}
	return entry.text[:chars]
}

var _ Source = (*Generator)(nil)

// New builds a Generator for the model. calibrator may be shared with the
// engine so warmup observations steer later prompts; nil gives a private one
// with the default ratio.
func New(workload spec.Workload, model string, calibrator *Calibrator, options Options) (*Generator, error) {
	api, err := resolveAPI(workload.Api)
	if err != nil {
		return nil, err
	}
	if workload.Dataset == nil {
		return nil, errNoDataset
	}
	if workload.Dataset.Type != spec.DatasetTypeSynthetic {
		return nil, fmt.Errorf("workload dataset type %q: only %q is supported", workload.Dataset.Type, spec.DatasetTypeSynthetic)
	}
	if workload.Dataset.InputTokens.Mean <= 0 {
		return nil, fmt.Errorf("workload input_tokens mean %v: must be positive", workload.Dataset.InputTokens.Mean)
	}
	if api != spec.WorkloadApiEmbeddings && workload.Dataset.OutputTokens.Mean <= 0 {
		return nil, fmt.Errorf("workload output_tokens mean %v: must be positive", workload.Dataset.OutputTokens.Mean)
	}
	if workload.Conversation != nil && api != spec.WorkloadApiChat {
		return nil, fmt.Errorf("workload.conversation needs api chat, not %q", api)
	}
	if prefix := workload.Dataset.SharedPrefix; prefix != nil {
		switch {
		case workload.Conversation != nil:
			return nil, errors.New("workload.dataset.shared_prefix cannot be combined with workload.conversation")
		case api == spec.WorkloadApiEmbeddings:
			return nil, errors.New("workload.dataset.shared_prefix needs api chat or completion")
		case workload.Dataset.InputTokens.Mean <= float64(prefix.Tokens):
			return nil, fmt.Errorf("workload.dataset.shared_prefix.tokens %d must be below input_tokens mean %v", prefix.Tokens, workload.Dataset.InputTokens.Mean)
		}
	}
	hash, err := Hash(workload, options)
	if err != nil {
		return nil, err
	}
	if calibrator == nil {
		calibrator = NewCalibrator(0)
	}
	return &Generator{
		api:        api,
		streaming:  workload.Streaming && api != spec.WorkloadApiEmbeddings, // embeddings cannot stream
		model:      model,
		seed:       uint64(int64(workload.Seed)),
		input:      workload.Dataset.InputTokens,
		output:     workload.Dataset.OutputTokens,
		padding:    workload.Dataset.PaddingBytes,
		calibrator: calibrator,
		options:    options,
		hash:       hash,
		saltPrefix: saltPrefix(options.CacheSalt),

		conversations: newConversations(workload.Conversation),
		prefixes:      newSharedPrefixes(workload.Dataset.SharedPrefix, uint64(int64(workload.Seed))),
	}, nil
}

// Info returns the workload block of environment.json. chars_per_token is
// set only when it was calibrated from usage.
func (g *Generator) Info() spec.WorkloadInfo {
	info := spec.WorkloadInfo{Hash: g.hash, Generator: generatorID()}
	if ratio, isCalibrated := g.calibrator.Calibrated(g.model); isCalibrated {
		info.CharsPerToken = &ratio
	}
	return info
}

// Next returns the next request. Request i depends only on the seed and i, so
// the sequence is identical across runs however many goroutines call Next;
// only the prompt length depends on the calibrated ratio at that moment.
func (g *Generator) Next() Request {
	index := int(g.next.Add(1) - 1)
	if g.options.ShardCount > 1 {
		index = index*g.options.ShardCount + g.options.ShardIndex
	}
	if g.conversations != nil {
		return g.conversationRequest(index)
	}
	rng := rand.New(rand.NewPCG(g.seed, uint64(index)))

	inputTokens := sampleTokens(rng, g.input)
	outputTokens := 0
	if g.api != spec.WorkloadApiEmbeddings {
		outputTokens = sampleTokens(rng, g.output)
	}
	charsPerToken := g.calibrator.CharsPerToken(g.model)
	prompt := g.saltPrefix
	uniqueTokens := inputTokens
	if g.prefixes != nil {
		// input_tokens is the whole prompt: the group's prefix, then the
		// request's own part, at least one token. The run's salt stays first,
		// so the prefix is shared within the run and never across runs.
		group := rng.IntN(len(g.prefixes.groups))
		prompt += g.prefixes.text(group, charsPerToken) + " "
		uniqueTokens = max(inputTokens-g.prefixes.tokens, 1)
	}
	// The salt counts toward the prompt, so it comes out of the request's part.
	uniqueChars := int(math.Round(float64(uniqueTokens)*charsPerToken)) - len(g.saltPrefix)
	prompt += randomText(rng, max(uniqueChars, 0))
	if g.padding > 0 {
		// The same vocabulary as the prompt, so the calibrated chars-per-token
		// ratio still holds for the whole text the server counts.
		prompt += " " + randomText(rng, g.padding-1)
	}

	request := Request{
		Index:                index,
		Streaming:            g.streaming,
		PromptChars:          len(prompt),
		ExpectedInputTokens:  inputTokens,
		ExpectedOutputTokens: outputTokens,
	}
	var body any
	switch g.api {
	case spec.WorkloadApiChat:
		request.Path = PathChat
		body = chatBody{
			Model:    g.model,
			Messages: []chatMessage{{Role: "user", Content: prompt}},
			sampling: g.sampling(outputTokens),
		}
	case spec.WorkloadApiCompletion:
		request.Path = PathCompletion
		body = completionBody{Model: g.model, Prompt: prompt, sampling: g.sampling(outputTokens)}
	default:
		request.Path = PathEmbeddings
		body = embeddingsBody{Model: g.model, Input: prompt}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		panic(fmt.Sprintf("workload: marshal request %d: %v", index, err)) // only strings and ints: cannot fail
	}
	request.Body = encoded
	return request
}

// conversationRequest is turn index of a multi-turn conversation.
func (g *Generator) conversationRequest(index int) Request {
	messages, inputTokens, outputTokens := g.history(index)
	chars := 0
	for _, message := range messages {
		chars += len(message.Content)
	}
	encoded, err := json.Marshal(chatBody{Model: g.model, Messages: messages, sampling: g.sampling(outputTokens)})
	if err != nil {
		panic(fmt.Sprintf("workload: marshal request %d: %v", index, err)) // only strings and ints: cannot fail
	}
	return Request{
		Index: index, Path: PathChat, Body: encoded, Streaming: g.streaming,
		PromptChars: chars, ExpectedInputTokens: inputTokens, ExpectedOutputTokens: outputTokens,
	}
}

func resolveAPI(api spec.WorkloadApi) (spec.WorkloadApi, error) {
	switch api {
	case "":
		return spec.WorkloadApiChat, nil // the schema default
	case spec.WorkloadApiChat, spec.WorkloadApiCompletion, spec.WorkloadApiEmbeddings:
		return api, nil
	}
	return "", fmt.Errorf("workload api %q: want chat, completion or embeddings", api)
}

// sampleTokens draws a token count from a normal distribution, rounded and
// floored at 1. Flooring only distorts the mean when stddev is large relative
// to the mean.
func sampleTokens(rng *rand.Rand, distribution spec.TokenDistribution) int {
	tokens := int(math.Round(distribution.Mean + distribution.Stddev*rng.NormFloat64()))
	return max(tokens, 1)
}

// vocabulary is common English words, each about one token in most
// tokenizers, so characters track tokens at roughly the default ratio.
var vocabulary = strings.Fields(`
the of and to in is you that it he was for on are as with his they at be this from
have or by one had not but what all were when we there can an your which their said
if do will each about how up out them then she many some so these would other into
has more her two like him see time could no make than first been its who now people
my made over did down only way find use may water long little very after words called
just where most know get through back much before go good new write our used me man
too any day same right look think also around another came come work three word must
because does part even place well such here take why help put different away again
off went old number great tell men say small every found still between name should
home big give air line set own under read last never us left end along while might
next sound below saw something thought both few those always looked show large often
together asked house don't world going want school important until form food keep
children feet land side without boy once animal life enough took sometimes four head
above kind began almost live page got earth need far hand high year mother light
country father let night picture being study second soon story since white ever
paper hard near sentence better best across during today however sure knew try told
young sun thing whole hear example heard several change answer room sea against top
turned learn point city play toward five using himself usually
`)

// randomText returns space-separated vocabulary words of exactly chars bytes.
func randomText(rng *rand.Rand, chars int) string {
	var text strings.Builder
	text.Grow(chars + 16)
	for text.Len() < chars {
		if text.Len() > 0 {
			text.WriteByte(' ')
		}
		text.WriteString(vocabulary[rng.IntN(len(vocabulary))])
	}
	return text.String()[:chars]
}

// saltPrefix turns the cache salt into a short fixed-width prompt prefix. It
// is hashed so a long run ID costs few characters and the prefix has the same
// length for every salt, which keeps prompt lengths identical across runs.
func saltPrefix(salt string) string {
	if salt == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(salt))
	return "[" + hex.EncodeToString(sum[:8]) + "] "
}
