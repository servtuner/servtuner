// Package launcher runs vLLM for a managed target on a plain host outside
// Kubernetes. `servtuner launcher` serves a small HTTP API on the host; servtuner's deployer calls it the way it
// calls Kubernetes on a cluster:
//
//	PUT    /vllm      {"args": [...], "env": {...}}  stop any vLLM it started, then start `vllm serve <args>`
//	GET    /vllm      the process: state, pid, exit code, and its argv as /proc/<pid>/cmdline has it
//	GET    /vllm/log  the last lines of its output (?tail=N, at most MaxLogTail)
//	GET    /vllm/version  the installed vLLM's version (`vllm --version`), known before it boots
//	GET    /machine   the host's GPUs, CPU and memory (HostMachine), for its host record
//	DELETE /vllm      stop it
//
// It can do nothing else: the vLLM binary is fixed when the launcher starts, there is no shell, only
// environment variables with an allowed prefix are passed, and every request needs the bearer token.
// Secrets stay on the host: vLLM's API key is read from a file and given as VLLM_API_KEY, so it is in
// neither the request nor the process's argv.
package launcher

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// MaxLogTail bounds GET /vllm/log: enough for the boot-failure classifier's window
	// (about 200 lines).
	MaxLogTail     = 1000
	defaultLogTail = 200
	// StopGrace is how long vLLM gets after SIGTERM before SIGKILL; it releases GPU memory on exit.
	StopGrace       = 30 * time.Second
	maxRequestBytes = 1048576 // 1 MiB: args and env are small
	// maxLogLineBytes: vLLM logs long tracebacks on one line at times; 1 MiB per line is plenty.
	maxLogLineBytes = 1048576
	logFileName     = "vllm.log"
)

// envNamePattern and allowedEnvPrefixes: what a request may set in vLLM's environment. servtuner-owned
// and secret variables are refused, and so is anything that changes how the process loads code.
var (
	envNamePattern     = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	allowedEnvPrefixes = []string{"VLLM_", "CUDA_", "PYTORCH_", "NCCL_", "OMP_", "TOKENIZERS_", "OPENAI_MOCK_"}
	refusedEnv         = map[string]bool{"VLLM_API_KEY": true, "HF_TOKEN": true}
)

// State of the vLLM process the launcher started.
const (
	StateNone    = "none"    // nothing started yet
	StateRunning = "running" // started and not exited
	StateExited  = "exited"  // exited on its own; ExitCode says how
	StateStopped = "stopped" // stopped through DELETE or a new PUT
)

// Options configure a launcher. Token and APIKey are values read from files by the caller.
type Options struct {
	VLLM    string // the vllm executable
	LogDir  string
	Token   string
	APIKey  string // vLLM's own API key, or empty
	BaseEnv []string
	Now     func() time.Time
	// NvidiaSMI and ProcDir are where GET /machine reads the GPUs and the CPU and memory; empty is
	// nvidia-smi on PATH (else WSL2's) and /proc.
	NvidiaSMI string
	ProcDir   string
}

// Status is GET /vllm's body.
type Status struct {
	State     string     `json:"state"`
	PID       int        `json:"pid,omitempty"`
	Args      []string   `json:"args,omitempty"` // what vllm serve was given, as /proc/<pid>/cmdline has it
	ExitCode  *int       `json:"exit_code,omitempty"`
	StartedAt *time.Time `json:"started_at,omitempty"`
}

// Request is PUT /vllm's body.
type Request struct {
	Args []string          `json:"args"`
	Env  map[string]string `json:"env"`
}

// Launcher supervises at most one vLLM process.
type Launcher struct {
	options Options
	mu      sync.Mutex
	cmd     *exec.Cmd
	done    chan struct{}
	status  Status
}

func New(options Options) *Launcher {
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Launcher{options: options, status: Status{State: StateNone}}
}

// Handler is the HTTP API.
func (l *Launcher) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /vllm", l.start)
	mux.HandleFunc("GET /vllm", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, l.Status()) })
	mux.HandleFunc("GET /vllm/log", l.log)
	mux.HandleFunc("GET /vllm/version", l.version)
	mux.HandleFunc("GET /machine", l.machine)
	mux.HandleFunc("DELETE /vllm", func(w http.ResponseWriter, _ *http.Request) {
		l.Stop()
		writeJSON(w, http.StatusOK, l.Status())
	})
	return l.authorized(mux)
}

func (l *Launcher) authorized(next http.Handler) http.Handler {
	want := []byte("Bearer " + l.options.Token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "a valid bearer token is required"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (l *Launcher) start(w http.ResponseWriter, r *http.Request) {
	var request Request
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body: " + err.Error()})
		return
	}
	env, err := checkedEnv(request.Env)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := l.Start(request.Args, env); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, l.Status())
}

func checkedEnv(env map[string]string) ([]string, error) {
	var problems []string
	result := make([]string, 0, len(env))
	for name, value := range env {
		allowed := false
		for _, prefix := range allowedEnvPrefixes {
			allowed = allowed || strings.HasPrefix(name, prefix)
		}
		switch {
		case !envNamePattern.MatchString(name):
			problems = append(problems, name+" is not an environment variable name")
		case refusedEnv[name]:
			problems = append(problems, name+" is a secret: the launcher reads it on the host")
		case !allowed:
			problems = append(problems, name+" has no allowed prefix ("+strings.Join(allowedEnvPrefixes, ", ")+")")
		default:
			result = append(result, name+"="+value)
		}
	}
	if len(problems) > 0 {
		return nil, errors.New(strings.Join(problems, "; "))
	}
	return result, nil
}

// Start stops any vLLM the launcher started and starts `vllm serve <args>` in a process group of its
// own, its output in LogDir/vllm.log (truncated per start).
func (l *Launcher) Start(args, env []string) error {
	l.Stop()
	l.mu.Lock()
	defer l.mu.Unlock()
	logFile, err := os.OpenFile(filepath.Join(l.options.LogDir, logFileName), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open the vLLM log: %w", err)
	}
	cmd := exec.Command(l.options.VLLM, append([]string{"serve"}, args...)...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.Env = append(append([]string{}, l.options.BaseEnv...), env...)
	if l.options.APIKey != "" {
		cmd.Env = append(cmd.Env, "VLLM_API_KEY="+l.options.APIKey)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // so a stop reaches vLLM's worker processes too
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("start %s: %w", l.options.VLLM, err)
	}
	started := l.options.Now().UTC()
	done := make(chan struct{})
	l.cmd, l.done = cmd, done
	l.status = Status{State: StateRunning, PID: cmd.Process.Pid, StartedAt: &started}
	go func() {
		err := cmd.Wait()
		_ = logFile.Close()
		l.mu.Lock()
		if l.status.State == StateRunning && l.cmd == cmd {
			code := cmd.ProcessState.ExitCode()
			if err != nil && code < 0 {
				code = -1
			}
			l.status.State, l.status.ExitCode = StateExited, &code
		}
		l.mu.Unlock()
		close(done)
	}()
	return nil
}

// Stop ends the vLLM process group: SIGTERM, then SIGKILL after StopGrace.
func (l *Launcher) Stop() {
	l.mu.Lock()
	cmd, done := l.cmd, l.done
	if cmd == nil || l.status.State != StateRunning {
		l.mu.Unlock()
		return
	}
	l.status.State = StateStopped
	l.mu.Unlock()
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(StopGrace):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
	}
}

// Status reports the process; for a running one, Args come from /proc so they are what vLLM really got.
func (l *Launcher) Status() Status {
	l.mu.Lock()
	status := l.status
	var startedWith []string
	if l.cmd != nil {
		startedWith = l.cmd.Args[2:] // after the executable and `serve`
	}
	l.mu.Unlock()
	status.Args = startedWith
	if status.State == StateRunning {
		if args, err := procArgs(status.PID); err == nil {
			status.Args = args
		}
	}
	return status
}

// procArgs reads /proc/<pid>/cmdline and drops the executable and `serve`.
func procArgs(pid int) ([]string, error) {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
	for i, part := range parts {
		if part == "serve" {
			return parts[i+1:], nil
		}
	}
	return nil, fmt.Errorf("pid %d is not vllm serve", pid)
}

func (l *Launcher) log(w http.ResponseWriter, r *http.Request) {
	tail := defaultLogTail
	if value, err := strconv.Atoi(r.URL.Query().Get("tail")); err == nil && value > 0 {
		tail = min(value, MaxLogTail)
	}
	content, err := os.ReadFile(filepath.Join(l.options.LogDir, logFileName))
	if errors.Is(err, os.ErrNotExist) {
		content, err = nil, nil
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	var lines []string
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(nil, maxLogLineBytes)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, strings.Join(lines, "\n"))
}

// versionTimeout bounds `vllm --version`: it imports vLLM's Python package, a few seconds.
const versionTimeout = 60 * time.Second

// version reports the installed vLLM's version without starting it, so the api can tell whether
// evidence measured on this host still holds. The output's last line is the version.
func (l *Launcher) version(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), versionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, l.options.VLLM, "--version")
	cmd.Env = append([]string{}, l.options.BaseEnv...)
	output, err := cmd.Output()
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if err != nil || last == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("vllm --version: %v", err)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"version": last})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
