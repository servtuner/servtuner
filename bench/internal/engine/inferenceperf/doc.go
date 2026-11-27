// Package inferenceperf drives inference-perf, the pinned external Python CLI
// (bench/CLAUDE.md), as a child process. The engine only generates
// load and reports raw results: the harness owns Prometheus, summaries and the
// bundle.
//
// # What the adapter does
//
//   - Prepare checks the RunSpec against what inference-perf can do and
//     renders its config (config.go). A feature it cannot honour is an error
//     naming the field; nothing is silently dropped.
//   - Run execs the CLI from its own virtualenv and leaves its stdout, stderr
//     and report files untouched in the output directory, which the harness
//     copies to engine/ in the bundle.
//   - Run then normalizes the per-request report into request rows
//     (normalize.go) and streams them to the sink.
//
// # Mapping from the RunSpec (inference-perf 0.7.0)
//
//	target.endpoint (minus the /v1 suffix)  server.base_url
//	target.model                            server.model_name
//	target.type                             server.type is always "vllm": it is the
//	                                        generic OpenAI client; the type only
//	                                        selects which server metrics it scrapes,
//	                                        and the harness does not use those
//	workload.api = completion               api.type = completion
//	workload.streaming                      api.streaming
//	workload.seed                           load.base_seed
//	dataset.input_tokens / output_tokens    data.type random, data.input_distribution / output_distribution
//	dataset.shared_prefix                   data.type shared_prefix: num_groups, system_prompt_len = tokens,
//	                                        question_len = input_tokens less tokens, output_len, and
//	                                        num_prompts_per_group from the planned requests (none repeat)
//	load.pacing constant | poisson          load.type constant | poisson
//	load.stages[i].rate / duration          load.stages[i+offset].rate / duration (whole seconds)
//	load.warmup                             one extra leading stage at the first stage's rate
//	tls.clientCert: its files               server.cert_path / server.key_path
//
// Rejected at Prepare: workload.api chat and embeddings (the random data
// generator is completion-only; use engine.type loadgen for chat), pacing
// linear and sine, target.auth (the CLI takes its API key only in the config
// file or argv, which would put a secret in the bundle), stage or warmup
// durations that are not whole seconds, a fixed length with a non-integer
// mean, and the engine.options listed below.
//
// # Data generator
//
// The dataset is rendered as inference-perf's "random" generator: prompts of
// random token ids at exact lengths. The "synthetic" generator slices one
// shared Shakespeare text, so prompts overlap across requests and across runs
// and a prefix cache would flatter the target. loadgen salts its prompts per
// run for the same reason; random token ids give the same isolation here.
//
// # engine.options (engine-native passthrough)
//
// The options are deep-merged into the rendered config, with these refusals,
// each an error naming the key: the sections the harness owns (server, load,
// api, data: the workload must come from the RunSpec or the workload hash
// lies; report, storage: the harness owns the output; metrics: engines must
// not query Prometheus), and any key, at any
// depth, whose name looks like a path or a secret (path, secret, token,
// password, key, cert, credential, auth). "tokenizer" is the one special key:
// a string naming the tokenizer, below.
//
// # Transport security
//
// With a client certificate the config carries server.cert_path and server.key_path.
// inference-perf has no CA option: its aiohttp client verifies the server
// against the system trust store, which Python's ssl module extends from the
// SSL_CERT_FILE environment variable. bench therefore sets SSL_CERT_FILE, on
// the child process only, to the combined system-plus-private-CA bundle
// (Options.CABundleFile). Verification is never skipped. Without TLS the
// config has no certificate settings and SSL_CERT_FILE is not set. With TLS and no client certificate
// the config has no certificate settings either, and SSL_CERT_FILE is
// set when the hop has a private CA (caSecretRef), as with a client certificate.
//
// # Tokenizer
//
// The generator builds prompts of exact token lengths with a Hugging Face
// tokenizer. The user supplies it: engine.options.tokenizer is a local
// directory (no network needed) or a model name (needs Hugging Face access
// from the bench pod); without the option the target.model name is used.
// Nothing is shipped in the image.
package inferenceperf
