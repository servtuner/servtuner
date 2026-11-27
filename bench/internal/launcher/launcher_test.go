package launcher

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeVLLM prints a version for --version, logs its args and the API key it got, fails like vLLM on --fail,
// and otherwise serves until
// stopped. It does not exec, so /proc/<pid>/cmdline stays `sh <script> serve ...`.
const fakeVLLM = `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "INFO a log line before the version"
  echo "0.34.0"
  exit 0
fi
echo "args: $*"
echo "key: $VLLM_API_KEY"
echo "mock env: $OPENAI_MOCK_ITL"
for arg in "$@"; do
  if [ "$arg" = "--fail" ]; then
    echo "ValueError: No available memory for the cache blocks."
    exit 1
  fi
done
trap 'exit 0' TERM
sleep 300 &
wait
`

const token = "launcher-token"

func newTestLauncher(t *testing.T) (*Launcher, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "vllm")
	if err := os.WriteFile(script, []byte(fakeVLLM), 0o700); err != nil {
		t.Fatal(err)
	}
	l := New(Options{VLLM: script, LogDir: dir, Token: token, APIKey: "sk-vllm", BaseEnv: []string{"PATH=" + os.Getenv("PATH")}})
	server := httptest.NewServer(l.Handler())
	t.Cleanup(func() {
		l.Stop()
		server.Close()
	})
	return l, server
}

func call(t *testing.T, server *httptest.Server, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, _ := json.Marshal(body)
		reader = bytes.NewReader(encoded)
	}
	request, _ := http.NewRequest(method, server.URL+path, reader)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	content, _ := io.ReadAll(response.Body)
	return response, content
}

func status(t *testing.T, server *httptest.Server) Status {
	t.Helper()
	_, body := call(t, server, http.MethodGet, "/vllm", nil)
	var got Status
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return got
}

func eventually(t *testing.T, condition func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestEveryRequestNeedsTheToken(t *testing.T) {
	_, server := newTestLauncher(t)
	response, err := http.Get(server.URL + "/vllm")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Errorf("status %d without a token", response.StatusCode)
	}
}

func TestStartReportsTheArgsVLLMGotAndKeepsTheKeyOutOfArgv(t *testing.T) {
	_, server := newTestLauncher(t)
	args := []string{"tiny", "--port=8000", "--max-num-seqs=64"}
	response, _ := call(t, server, http.MethodPut, "/vllm", Request{Args: args, Env: map[string]string{"OPENAI_MOCK_ITL": "2ms"}})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status %d", response.StatusCode)
	}
	running := status(t, server)
	if running.State != StateRunning || running.PID == 0 || !slices.Equal(running.Args, args) {
		t.Fatalf("status = %+v", running)
	}
	eventually(t, func() bool {
		_, log := call(t, server, http.MethodGet, "/vllm/log", nil)
		return strings.Contains(string(log), "mock env: 2ms")
	}, "the log")
	_, log := call(t, server, http.MethodGet, "/vllm/log", nil)
	if !strings.Contains(string(log), "key: sk-vllm") || !strings.Contains(string(log), "args: serve tiny --port=8000 --max-num-seqs=64") {
		t.Errorf("log = %q", log)
	}
	cmdline, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(running.PID), "cmdline"))
	if strings.Contains(string(cmdline), "sk-vllm") {
		t.Error("the API key is in vLLM's argv")
	}
}

func TestAFailedBootIsReportedWithItsLog(t *testing.T) {
	_, server := newTestLauncher(t)
	call(t, server, http.MethodPut, "/vllm", Request{Args: []string{"tiny", "--fail"}})
	eventually(t, func() bool { return status(t, server).State == StateExited }, "the exit")
	exited := status(t, server)
	if exited.ExitCode == nil || *exited.ExitCode != 1 || !slices.Equal(exited.Args, []string{"tiny", "--fail"}) {
		t.Fatalf("status = %+v", exited)
	}
	_, log := call(t, server, http.MethodGet, "/vllm/log?tail=1", nil)
	if string(log) != "ValueError: No available memory for the cache blocks." {
		t.Errorf("tail = %q", log)
	}
}

func TestStopAndRestartEndTheProcessGroup(t *testing.T) {
	_, server := newTestLauncher(t)
	call(t, server, http.MethodPut, "/vllm", Request{Args: []string{"one"}})
	first := status(t, server).PID
	call(t, server, http.MethodPut, "/vllm", Request{Args: []string{"two"}})
	if second := status(t, server); second.PID == first || second.State != StateRunning || second.Args[0] != "two" {
		t.Fatalf("restart = %+v", second)
	}
	if err := syscall.Kill(first, 0); err == nil {
		t.Errorf("the first vLLM (pid %d) is still alive", first)
	}
	call(t, server, http.MethodDelete, "/vllm", nil)
	if stopped := status(t, server); stopped.State != StateStopped {
		t.Errorf("after DELETE: %+v", stopped)
	}
}

func TestEnvIsLimitedToSafePrefixes(t *testing.T) {
	_, server := newTestLauncher(t)
	for _, env := range []map[string]string{{"HF_TOKEN": "x"}, {"VLLM_API_KEY": "x"}, {"LD_PRELOAD": "/x.so"}, {"bad name": "x"}} {
		response, body := call(t, server, http.MethodPut, "/vllm", Request{Args: []string{"tiny"}, Env: env})
		if response.StatusCode != http.StatusBadRequest {
			t.Errorf("%v: status %d %s", env, response.StatusCode, body)
		}
	}
	if got := status(t, server).State; got != StateNone {
		t.Errorf("a refused request started vLLM: %s", got)
	}
}

// TestVersionReportsTheInstalledVLLM: the version is known before vLLM boots, from the
// last line of vllm --version, and needs the token like every other call.
func TestVersionReportsTheInstalledVLLM(t *testing.T) {
	_, server := newTestLauncher(t)
	response, body := call(t, server, http.MethodGet, "/vllm/version", nil)
	if response.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != `{"version":"0.34.0"}` {
		t.Fatalf("GET /vllm/version: %d %s", response.StatusCode, body)
	}
	anonymous, _ := http.Get(server.URL + "/vllm/version")
	if anonymous.StatusCode != http.StatusUnauthorized {
		t.Errorf("without the token: %d", anonymous.StatusCode)
	}
	_ = anonymous.Body.Close()
}
