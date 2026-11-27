package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func metricsText(t *testing.T, address string) string {
	t.Helper()
	response, err := newClient(nil).Get("http://" + address + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	return string(body)
}

func TestFaultKnobsAreValidated(t *testing.T) {
	for name, change := range map[string]func(*FaultConfig){
		"negative latency":       func(f *FaultConfig) { f.ExtraLatency = -time.Second },
		"negative concurrency":   func(f *FaultConfig) { f.MaxConcurrency = -1 },
		"unknown over-capacity":  func(f *FaultConfig) { f.OverCapacity = "drop" },
		"error rate above one":   func(f *FaultConfig) { f.ErrorRate = 1.5 },
		"error status not error": func(f *FaultConfig) { f.ErrorStatus = 200 },
		"ramp without a maximum": func(f *FaultConfig) { f.MemoryRampBytesPerSecond = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := plainConfig()
			change(&cfg.Faults)
			if cfg.Validate() == nil {
				t.Error("accepted")
			}
		})
	}
	if err := plainConfig().Validate(); err != nil {
		t.Errorf("the default knobs are invalid: %v", err)
	}
}

func TestFaultKnobsComeFromFlagsAndEnvironment(t *testing.T) {
	env := map[string]string{"OPENAI_MOCK_TLS": "false", "OPENAI_MOCK_ERROR_RATE": "0.25", "OPENAI_MOCK_MAX_CONCURRENCY": "4"}
	cfg, err := loadConfig([]string{"--over-capacity", "reject", "--extra-latency", "50ms"}, func(key string) string { return env[key] }, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	want := FaultConfig{ExtraLatency: 50 * time.Millisecond, MaxConcurrency: 4, OverCapacity: overCapacityReject, ErrorRate: 0.25, ErrorStatus: http.StatusInternalServerError}
	if cfg.Faults != want {
		t.Errorf("faults = %+v, want %+v", cfg.Faults, want)
	}
}

// Every fourth request fails at 0.25, so the share is exact and reproducible.
func TestErrorRateIsSpreadEvenly(t *testing.T) {
	cfg := plainConfig()
	cfg.Faults.ErrorRate = 0.25
	cfg.Faults.ErrorStatus = http.StatusServiceUnavailable
	address := startMock(t, cfg)
	var codes []int
	for range 8 {
		response, _ := postChat(t, address, `{"messages":[]}`)
		codes = append(codes, response.StatusCode)
	}
	want := []int{200, 200, 200, 503, 200, 200, 200, 503}
	for i := range want {
		if codes[i] != want[i] {
			t.Fatalf("codes = %v, want %v", codes, want)
		}
	}
	if body := metricsText(t, address); !strings.Contains(body, `openai_mock_faults_total{kind="error"} 2`) {
		t.Errorf("metrics lack the injected errors:\n%s", body)
	}
}

func TestExtraLatencyDelaysTheFirstToken(t *testing.T) {
	cfg := plainConfig()
	cfg.Faults.ExtraLatency = 150 * time.Millisecond
	address := startMock(t, cfg)
	response, sent := postChat(t, address, `{"messages":[]}`)
	_, _ = io.ReadAll(response.Body)
	if elapsed := time.Since(sent); elapsed < cfg.Faults.ExtraLatency {
		t.Errorf("answered after %s, before the extra latency", elapsed)
	}
}

// startSlowRequests holds n requests in service (each streams for hold) and
// returns once they are all admitted.
func startSlowRequests(t *testing.T, address string, n int) *sync.WaitGroup {
	t.Helper()
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response, err := newClient(nil).Post("http://"+address+chatCompletionsPath, "application/json",
				strings.NewReader(`{"messages":[],"max_tokens":20,"stream":true}`))
			if err == nil {
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
			}
		}()
	}
	return &wg
}

func waitForMetric(t *testing.T, address, line string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(metricsText(t, address), line) {
		if time.Now().After(deadline) {
			t.Fatalf("metrics never showed %q:\n%s", line, metricsText(t, address))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func slowConfig() Config {
	cfg := plainConfig()
	cfg.InterTokenLatency = 25 * time.Millisecond // 20 tokens: each request is served for about 0.5s
	return cfg
}

func TestOverCapacityRejectsWith429(t *testing.T) {
	cfg := slowConfig()
	cfg.Faults.MaxConcurrency = 2
	cfg.Faults.OverCapacity = overCapacityReject
	address := startMock(t, cfg)
	busy := startSlowRequests(t, address, 2)
	waitForMetric(t, address, "openai_mock_requests_in_flight 2")
	response, _ := postChat(t, address, `{"messages":[]}`)
	if response.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status %d, want 429 over capacity", response.StatusCode)
	}
	busy.Wait()
	if body := metricsText(t, address); !strings.Contains(body, `openai_mock_faults_total{kind="rejected"} 1`) {
		t.Errorf("metrics lack the rejection:\n%s", body)
	}
}

func TestOverCapacityQueuesUntilASlotFrees(t *testing.T) {
	cfg := slowConfig()
	cfg.Faults.MaxConcurrency = 1
	address := startMock(t, cfg)
	busy := startSlowRequests(t, address, 1)
	waitForMetric(t, address, "openai_mock_requests_in_flight 1")
	queued := startSlowRequests(t, address, 1)
	waitForMetric(t, address, "openai_mock_requests_queued 1")
	busy.Wait()
	queued.Wait()
	waitForMetric(t, address, "openai_mock_requests_queued 0")
	if body := metricsText(t, address); !strings.Contains(body, `openai_mock_requests_total{code="200",path="/v1/chat/completions",stream="true"} 2`) {
		t.Errorf("the queued request was not served:\n%s", body)
	}
}

// With one request already in service, the next waits LatencyPerInFlight more.
func TestLatencyGrowsWithRequestsInFlight(t *testing.T) {
	cfg := slowConfig()
	cfg.Faults.LatencyPerInFlight = 200 * time.Millisecond
	address := startMock(t, cfg)
	busy := startSlowRequests(t, address, 1)
	waitForMetric(t, address, "openai_mock_requests_in_flight 1")
	response, sent := postChat(t, address, `{"messages":[]}`)
	_, _ = io.ReadAll(response.Body)
	if elapsed := time.Since(sent); elapsed < cfg.Faults.LatencyPerInFlight {
		t.Errorf("answered after %s with another request in flight", elapsed)
	}
	busy.Wait()
}

func TestMemoryRampGrowsToItsMaximumAndStops(t *testing.T) {
	cfg := FaultConfig{MemoryRampBytesPerSecond: 3 * pageSize, MemoryRampMaxBytes: 7 * pageSize}
	tick := make(chan time.Time)
	ramp := startMemoryRamp(context.Background(), cfg, newMetrics(), tick)
	for range 3 {
		tick <- time.Now()
	}
	deadline := time.Now().Add(time.Second)
	for ramp.size() != 7*pageSize && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if ramp.size() != 7*pageSize {
		t.Fatalf("ramp = %d bytes, want %d", ramp.size(), 7*pageSize)
	}
	select {
	case tick <- time.Now():
		t.Error("the ramp kept ticking after reaching its maximum")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestTouchedMakesEveryPageResident(t *testing.T) {
	buffer := touched(3*pageSize + 1)
	for i := 0; i < len(buffer); i += pageSize {
		if buffer[i] != 1 {
			t.Fatalf("page at %d untouched", i)
		}
	}
	if touched(0) != nil {
		t.Error("zero bytes allocated something")
	}
}
