package workload

import "sync"

// DefaultCharsPerToken is the ratio used before any observation. Four
// characters per token is the usual rule of thumb for English text.
const DefaultCharsPerToken = 4.0

// Calibrator learns a chars-per-token ratio per model from the prompt_tokens
// that the server reports in `usage` during warmup.
// Tokenizers differ by model, and bench carries none, so prompts are sized by
// characters and corrected by this ratio. It is safe for concurrent use.
type Calibrator struct {
	defaultRatio float64

	mu     sync.Mutex
	totals map[string]observationTotals
}

type observationTotals struct {
	promptChars  int64
	promptTokens int64
}

// NewCalibrator returns a Calibrator that answers defaultCharsPerToken for a
// model with no observations. A value that is not positive selects
// DefaultCharsPerToken.
func NewCalibrator(defaultCharsPerToken float64) *Calibrator {
	if defaultCharsPerToken <= 0 {
		defaultCharsPerToken = DefaultCharsPerToken
	}
	return &Calibrator{defaultRatio: defaultCharsPerToken, totals: map[string]observationTotals{}}
}

// Observe records one request: the characters of the prompt that was sent and
// the prompt_tokens the server reported for it. Observations that are not
// positive are ignored. The ratio is total characters over total tokens, so
// long prompts weigh more and the estimate converges as warmup proceeds.
// A chat template adds a few tokens the characters do not account for; that
// bias is small against the prompt sizes of a benchmark and is left in.
func (c *Calibrator) Observe(model string, promptChars, promptTokens int) {
	if promptChars <= 0 || promptTokens <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	totals := c.totals[model]
	totals.promptChars += int64(promptChars)
	totals.promptTokens += int64(promptTokens)
	c.totals[model] = totals
}

// CharsPerToken returns the calibrated ratio for model, or the default when
// the model has no observations yet.
func (c *Calibrator) CharsPerToken(model string) float64 {
	ratio, _ := c.Calibrated(model)
	return ratio
}

// Calibrated returns the ratio for model and whether it came from
// observations (true) or is the default (false). environment.json records the
// ratio only when it was calibrated.
func (c *Calibrator) Calibrated(model string) (ratio float64, isCalibrated bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	totals, ok := c.totals[model]
	if !ok {
		return c.defaultRatio, false
	}
	return float64(totals.promptChars) / float64(totals.promptTokens), true
}
