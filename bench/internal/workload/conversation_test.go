package workload

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

func conversationWorkload(turns, concurrent int) spec.Workload {
	return spec.Workload{
		Api: spec.WorkloadApiChat, Seed: 7,
		Dataset:      &spec.Dataset{Type: spec.DatasetTypeSynthetic, InputTokens: spec.TokenDistribution{Mean: 20}, OutputTokens: spec.TokenDistribution{Mean: 10}},
		Conversation: &spec.Conversation{Turns: turns, Concurrent: concurrent},
	}
}

func messagesOf(t *testing.T, request Request) []chatMessage {
	t.Helper()
	var body chatBody
	if err := json.Unmarshal(request.Body, &body); err != nil {
		t.Fatal(err)
	}
	return body.Messages
}

func TestEachTurnRepeatsTheConversationSoFar(t *testing.T) {
	generator, err := New(conversationWorkload(3, 2), "m", nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var requests []Request
	for range 8 {
		requests = append(requests, generator.Next())
	}
	// Slot 0 is requests 0, 2, 4 (turns 0, 1, 2), then 6 starts its next conversation.
	turn0, turn1, turn2, next := messagesOf(t, requests[0]), messagesOf(t, requests[2]), messagesOf(t, requests[4]), messagesOf(t, requests[6])
	if len(turn0) != 1 || len(turn1) != 3 || len(turn2) != 5 || len(next) != 1 {
		t.Fatalf("message counts %d %d %d %d", len(turn0), len(turn1), len(turn2), len(next))
	}
	if !reflect.DeepEqual(turn1[:1], turn0) || !reflect.DeepEqual(turn2[:3], turn1) {
		t.Error("a turn does not repeat the earlier messages byte for byte")
	}
	if turn1[1].Role != "assistant" || turn2[3].Role != "assistant" {
		t.Errorf("roles %v", turn2)
	}
	if next[0] == turn0[0] {
		t.Error("the next conversation reuses the first one's text")
	}
	// Slot 1 is a different conversation.
	if messagesOf(t, requests[1])[0] == turn0[0] {
		t.Error("two slots share a conversation")
	}
	// The input grows: 20 + 10 + 20 tokens at turn 1, 20 + 10 + 20 + 10 + 20 at
	// turn 2, and turn 1's reply is
	// the assistant message turn 2 replays.
	if requests[2].ExpectedInputTokens != 50 || requests[4].ExpectedInputTokens != 80 || requests[2].ExpectedOutputTokens != 10 {
		t.Errorf("expected tokens %d %d %d", requests[2].ExpectedInputTokens, requests[4].ExpectedInputTokens, requests[2].ExpectedOutputTokens)
	}
}

func TestConversationTextDoesNotDependOnGenerationOrder(t *testing.T) {
	first, _ := New(conversationWorkload(4, 3), "m", nil, Options{})
	second, _ := New(conversationWorkload(4, 3), "m", nil, Options{})
	var a []Request
	for range 12 {
		a = append(a, first.Next())
	}
	// The second generator skips straight to slot 0's last turn: its history
	// is built from the seed, so it matches.
	second.next.Store(9)
	if got, want := messagesOf(t, second.Next()), messagesOf(t, a[9]); !reflect.DeepEqual(got, want) {
		t.Errorf("turn 3 differs when generated first:\n%v\n%v", got, want)
	}
}

func TestConversationIsHashedAndChatOnly(t *testing.T) {
	single := conversationWorkload(3, 2)
	single.Conversation = nil
	plain, _ := Hash(single, Options{})
	multi, _ := Hash(conversationWorkload(3, 2), Options{})
	other, _ := Hash(conversationWorkload(4, 2), Options{})
	if plain == multi || multi == other {
		t.Error("the conversation shape must change the workload hash")
	}
	completion := conversationWorkload(3, 2)
	completion.Api = spec.WorkloadApiCompletion
	if _, err := New(completion, "m", nil, Options{}); err == nil {
		t.Error("a multi-turn completion workload was accepted")
	}
}

func TestShardsSplitTheSequenceWithoutOverlap(t *testing.T) {
	workload := spec.Workload{
		Api:     spec.WorkloadApiChat,
		Seed:    7,
		Dataset: &spec.Dataset{Type: spec.DatasetTypeSynthetic, InputTokens: spec.TokenDistribution{Mean: 20}, OutputTokens: spec.TokenDistribution{Mean: 5}},
	}
	whole, err := New(workload, "m", nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	const shards = 3
	parts := make([]*Generator, shards)
	for index := range parts {
		if parts[index], err = New(workload, "m", nil, Options{ShardIndex: index, ShardCount: shards}); err != nil {
			t.Fatal(err)
		}
	}
	for index := range 12 {
		want := whole.Next()
		got := parts[index%shards].Next()
		if got.Index != want.Index || string(got.Body) != string(want.Body) {
			t.Fatalf("request %d: shard %d sent index %d, want the unsharded request %d", index, index%shards, got.Index, want.Index)
		}
	}
}
