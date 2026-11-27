package workload

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand/v2"
	"sync"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// defaultConcurrentConversations is the schema default of
// workload.conversation.concurrent.
const defaultConcurrentConversations = 64

// conversations builds multi-turn chat requests (workload.conversation). A
// conversation's messages are generated once and kept, so every turn repeats
// the earlier ones byte for byte even if the chars-per-token calibration
// moves during the run: the server must see the same prefix. Only each
// slot's current conversation is kept.
type conversations struct {
	turns, concurrent int
	mutex             sync.Mutex
	// current is, per slot, the conversation number and its messages so far.
	current map[int]*conversation
}

type conversation struct {
	number   int
	messages []chatMessage
	// tokens are each message's sampled token count, for the expected input.
	tokens []int
}

func newConversations(config *spec.Conversation) *conversations {
	if config == nil {
		return nil
	}
	concurrent := config.Concurrent
	if concurrent <= 0 {
		concurrent = defaultConcurrentConversations
	}
	return &conversations{turns: config.Turns, concurrent: concurrent, current: map[int]*conversation{}}
}

// position maps a request index to its slot, conversation number and turn.
func (c *conversations) position(index int) (slot, number, turn int) {
	slot = index % c.concurrent
	round := index / c.concurrent
	return slot, round / c.turns, round % c.turns
}

// messageRNG seeds one message from the run seed and its place, so a
// conversation's text never depends on the order requests were generated in.
func messageRNG(seed uint64, slot, number, turn int, role string) *rand.Rand {
	sum := sha256.Sum256(fmt.Appendf(nil, "%d/%d/%d/%s", slot, number, turn, role))
	return rand.New(rand.NewPCG(seed, binary.BigEndian.Uint64(sum[:8])))
}

// history returns the messages of a turn: every earlier user message and
// assistant reply, then this turn's user message, with their token counts.
func (g *Generator) history(index int) (messages []chatMessage, inputTokens int, outputTokens int) {
	c := g.conversations
	slot, number, turn := c.position(index)
	c.mutex.Lock()
	defer c.mutex.Unlock()
	current := c.current[slot]
	if current == nil || current.number != number {
		current = &conversation{number: number}
		c.current[slot] = current
	}
	ratio := g.calibrator.CharsPerToken(g.model)
	text := func(rng *rand.Rand, tokens int, prefix string) string {
		chars := int(math.Round(float64(tokens) * ratio))
		return prefix + randomText(rng, max(chars-len(prefix), 0))
	}
	// Messages 2j and 2j+1 are turn j's user message and assistant reply.
	for len(current.messages) < 2*turn+1 {
		next := len(current.messages)
		at := next / 2
		if next%2 == 0 {
			rng := messageRNG(g.seed, slot, number, at, "user")
			tokens := sampleTokens(rng, g.input)
			prefix := ""
			if at == 0 {
				prefix = g.saltPrefix
			}
			current.messages = append(current.messages, chatMessage{Role: "user", Content: text(rng, tokens, prefix)})
			current.tokens = append(current.tokens, tokens)
			continue
		}
		rng := messageRNG(g.seed, slot, number, at, "assistant")
		tokens := sampleTokens(rng, g.output)
		current.messages = append(current.messages, chatMessage{Role: "assistant", Content: text(rng, tokens, "")})
		current.tokens = append(current.tokens, tokens)
	}
	count := 2*turn + 1
	for _, tokens := range current.tokens[:count] {
		inputTokens += tokens
	}
	// This turn's reply length: the assistant message the next turn replays.
	outputTokens = sampleTokens(messageRNG(g.seed, slot, number, turn, "assistant"), g.output)
	return append([]chatMessage(nil), current.messages[:count]...), inputTokens, outputTokens
}
