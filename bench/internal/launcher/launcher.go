// Package launcher runs a model server (vLLM, or SGLang) for a managed target on a plain host outside
// Kubernetes. `servtuner launcher` serves a small HTTP API on the host; servtuner's deployer calls it the way it
// calls Kubernetes on a cluster:
//
//	PUT    /server      {"server": "vllm"|"sglang", "args": [...], "env": {...}}  stop any server it started,
//	                    then start `vllm serve <args>` or `<sglang python> -m sglang.launch_server <args>`
//	GET    /server      the process: server, state, pid, exit code, and its args as /proc/<pid>/cmdline has them
//	GET    /server/log  the last lines of its output (?tail=N, at most MaxLogTail)
//	GET    /server/version?server=vllm|sglang  the installed server's version, known before it boots
//	GET    /machine     the host's GPUs, CPU and memory (HostMachine), for its host record
//	DELETE /server      stop it
//
// It can do nothing else: the executables are fixed when the launcher starts, there is no shell, only
// environment variables with an allowed prefix are passed, and every request needs the bearer token.
// Secrets stay on the host: the server's API key is read from a file and given to vLLM as VLLM_API_KEY,
// or to SGLang (which reads no key from its environment) in a --config file only the launcher can read,
// so it is in neither the request nor the process's argv.
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
	// MaxLogTail bounds GET /server/log: enough for the boot-failure classifier's window
	// (about 200 lines).
	MaxLogTail     = 1000
	defaultLogTail = 200
	// StopGrace is how long the server gets after SIGTERM before SIGKILL; it releases GPU memory on exit.
	StopGrace       = 30 * time.Second
	maxRequestBytes = 1048576 // 1 MiB: args and env are small
	// maxLogLineBytes: vLLM logs long tracebacks on one line at times; 1 MiB per line is plenty.
	maxLogLineBytes = 1048576
	logFileName     = "server.log"
	// sglangKeyFileName holds SGLang's API key as a --config file (YAML); SGLang wants a .yaml name.
	sglangKeyFileName = "sglang-api-key.yaml"
)

// The servers the launcher starts.
const (
	ServerVLLM   = "vllm"
	ServerSGLang = "sglang"
)

// sglangModule is SGLang's documented server entry point.
const sglangModule = "sglang.launch_server"

// keyFlags are flags a request may not pass: the key stays on the host, and SGLang's --config is the
// launcher's own (it carries the key).
var keyFlags = []string{"--api-key", "--admin-api-key", "--config"}

// envNamePattern and allowedEnvPrefixes: what a request may set in vLLM's environment. servtuner-owned
// and secret variables are refused, and so is anything that changes how the process loads code.
var (
	envNamePattern     = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	allowedEnvPrefixes = []string{"VLLM_", "SGLANG_", "FLASHINFER_", "CUDA_", "PYTORCH_", "NCCL_", "OMP_", "TOKENIZERS_", "OPENAI_MOCK_"}
	refusedEnv         = map[string]bool{"VLLM_API_KEY": true, "HF_TOKEN": true}
)

// State of the server process the launcher started.
const (
	StateNone    = "none"    // nothing started yet
	StateRunning = "running" // started and not exited
	StateExited  = "exited"  // exited on its own; ExitCode says how
	StateStopped = "stopped" // stopped through DELETE or a new PUT
)

// Options configure a launcher. Token and APIKey are values read from files by the caller.
type Options struct {
	VLLM string // the vllm executable
	// SGLangPython is the Python of SGLang's virtualenv; empty means this host runs no SGLang.
	SGLangPython string
	LogDir       string
	Token        string
	APIKey       string // the server's own API key, or empty
	BaseEnv      []string
	Now          func() time.Time
	// NvidiaSMI and ProcDir are where GET /machine reads the GPUs and the CPU and memory; empty is
	// nvidia-smi on PATH (else WSL2's) and /proc.
	NvidiaSMI string
	ProcDir   string
}

// Status is GET /server's body.
type Status struct {
	Server    string     `json:"server,omitempty"` // vllm or sglang; empty before the first start
	State     string     `json:"state"`
	PID       int        `json:"pid,omitempty"`
	Args      []string   `json:"args,omitempty"` // what the server was given, as /proc/<pid>/cmdline has it
	ExitCode  *int       `json:"exit_code,omitempty"`
	StartedAt *time.Time `json:"started_at,omitempty"`
}

// Request is PUT /server's body. Server is required: the launcher never guesses which one to run.
type Request struct {
	Server string            `json:"server"`
	Args   []string          `json:"args"`
	Env    map[string]string `json:"env"`
}

// Launcher supervises at most one server process.
type Launcher struct {
	options Options
	mu      sync.Mutex
	cmd     *exec.Cmd
	done    chan struct{}
	status  Status
	// prefix is how many args come before the server's own (serve, or -m sglang.launch_server).
	prefix int
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
	mux.HandleFunc("PUT /server", l.start)
	mux.HandleFunc("GET /server", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, l.Status()) })
	mux.HandleFunc("GET /server/log", l.log)
	mux.HandleFunc("GET /server/version", l.version)
	mux.HandleFunc("GET /machine", l.machine)
	mux.HandleFunc("DELETE /server", func(w http.ResponseWriter, _ *http.Request) {
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
	if err := l.checkServer(request.Server); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := checkedArgs(request.Args); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	env, err := checkedEnv(request.Env)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := l.Start(request.Server, request.Args, env); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, l.Status())
}

// checkServer refuses a server the request does not name, does not exist, or this host cannot run.
func (l *Launcher) checkServer(server string) error {
	switch server {
	case ServerVLLM:
		return nil
	case ServerSGLang:
		if l.options.SGLangPython == "" {
			return errors.New("this launcher runs no SGLang: start it with --sglang <the Python of SGLang's virtualenv>")
		}
		return nil
	case "":
		return errors.New("server is required: vllm or sglang")
	default:
		return fmt.Errorf("server %q: vllm or sglang", server)
	}
}

// checkedArgs refuses the flags that carry a key: it is read on the host, never sent.
func checkedArgs(args []string) error {
	for _, argument := range args {
		for _, flag := range keyFlags {
			if argument == flag || strings.HasPrefix(argument, flag+"=") {
				return fmt.Errorf("%s is the launcher's to set: the key is read on the host", flag)
			}
		}
	}
	return nil
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

// Start stops any server the launcher started and starts `vllm serve <args>` or
// `<sglang python> -m sglang.launch_server <args>` in a process group of its own, its output in
// LogDir/server.log (truncated per start).
func (l *Launcher) Start(server string, args, env []string) error {
	if err := l.checkServer(server); err != nil {
		return err
	}
	l.Stop()
	l.mu.Lock()
	defer l.mu.Unlock()
	logFile, err := os.OpenFile(filepath.Join(l.options.LogDir, logFileName), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open the server log: %w", err)
	}
	executable, prefix := l.options.VLLM, []string{"serve"}
	if server == ServerSGLang {
		executable, prefix = l.options.SGLangPython, []string{"-m", sglangModule}
	}
	commandArgs := append(append([]string{}, prefix...), args...)
	extraEnv := []string{}
	if l.options.APIKey != "" {
		if server == ServerSGLang {
			keyFile, err := l.writeSGLangKeyFile()
			if err != nil {
				_ = logFile.Close()
				return err
			}
			commandArgs = append(commandArgs, "--config", keyFile)
		} else {
			extraEnv = append(extraEnv, "VLLM_API_KEY="+l.options.APIKey)
		}
	}
	cmd := exec.Command(executable, commandArgs...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.Env = append(append(append([]string{}, l.options.BaseEnv...), env...), extraEnv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // so a stop reaches vLLM's worker processes too
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("start %s: %w", executable, err)
	}
	started := l.options.Now().UTC()
	done := make(chan struct{})
	l.cmd, l.done, l.prefix = cmd, done, len(prefix)
	l.status = Status{Server: server, State: StateRunning, PID: cmd.Process.Pid, StartedAt: &started}
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

// writeSGLangKeyFile writes SGLang's API key as a --config YAML only the launcher's user can read.
// The value is a JSON string, which is valid YAML, so no character of the key can break the file.
func (l *Launcher) writeSGLangKeyFile() (string, error) {
	path := filepath.Join(l.options.LogDir, sglangKeyFileName)
	value, err := json.Marshal(l.options.APIKey)
	if err != nil {
		return "", fmt.Errorf("encode the API key: %w", err)
	}
	if err := os.WriteFile(path, append([]byte("api_key: "), append(value, '\n')...), 0o600); err != nil {
		return "", fmt.Errorf("write SGLang's key file: %w", err)
	}
	return path, nil
}

// Stop ends the server's process group: SIGTERM, then SIGKILL after StopGrace.
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

// Status reports the process; for a running one, Args come from /proc so they are what the server
// really got. SGLang's --config (the launcher's key file) is not one of them.
func (l *Launcher) Status() Status {
	l.mu.Lock()
	status := l.status
	var startedWith []string
	if l.cmd != nil {
		startedWith = l.cmd.Args[1+l.prefix:] // after the executable and the entry point
	}
	l.mu.Unlock()
	status.Args = startedWith
	if status.State == StateRunning {
		if args, err := procArgs(status.PID, status.Server); err == nil {
			status.Args = args
		}
	}
	status.Args = withoutKeyConfig(status.Args)
	return status
}

// withoutKeyConfig drops the --config <key file> the launcher adds for SGLang.
func withoutKeyConfig(args []string) []string {
	for i, argument := range args {
		if argument == "--config" && i+1 < len(args) && filepath.Base(args[i+1]) == sglangKeyFileName {
			return append(append([]string{}, args[:i]...), args[i+2:]...)
		}
	}
	return args
}

// procArgs reads /proc/<pid>/cmdline and drops everything up to the entry point: `serve` for vLLM,
// sglang.launch_server for SGLang.
func procArgs(pid int, server string) ([]string, error) {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return nil, err
	}
	entry := "serve"
	if server == ServerSGLang {
		entry = sglangModule
	}
	parts := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
	for i, part := range parts {
		if part == entry {
			return parts[i+1:], nil
		}
	}
	return nil, fmt.Errorf("pid %d is not %s", pid, server)
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

// versionTimeout bounds the version command: it imports the server's Python package, a few seconds.
const versionTimeout = 60 * time.Second

// sglangVersionScript prints SGLang's version without starting it.
const sglangVersionScript = "import sglang; print(sglang.__version__)"

// version reports an installed server's version (?server=, vllm by default) without starting it, so
// the api can tell whether evidence measured on this host still holds. The output's last line is the
// version.
func (l *Launcher) version(w http.ResponseWriter, r *http.Request) {
	server := r.URL.Query().Get("server")
	if server == "" {
		server = ServerVLLM
	}
	if err := l.checkServer(server); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), versionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, l.options.VLLM, "--version")
	if server == ServerSGLang {
		cmd = exec.CommandContext(ctx, l.options.SGLangPython, "-c", sglangVersionScript)
	}
	cmd.Env = append([]string{}, l.options.BaseEnv...)
	output, err := cmd.Output()
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if err != nil || last == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("%s version: %v", server, err)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"version": last})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
