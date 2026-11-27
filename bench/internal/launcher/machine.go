package launcher

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

const (
	// machineTimeout bounds nvidia-smi, which can take a few seconds on a busy GPU.
	machineTimeout = 30 * time.Second
	bytesPerMiB    = 1048576 // nvidia-smi reports memory.total in MiB
	bytesPerKiB    = 1024    // /proc/meminfo's "kB" is KiB
	// wslNvidiaSMI is where WSL2 puts nvidia-smi; it is often not on a service's PATH.
	wslNvidiaSMI = "/usr/lib/wsl/lib/nvidia-smi"
)

// Machine reads what the host is: its GPUs, CPU and memory (GET /machine). A part it
// cannot read is named in Problems and left out, never guessed.
func (l *Launcher) Machine(ctx context.Context) spec.HostMachine {
	machine := spec.HostMachine{ReadAt: spec.NewUTCTimestamp(l.options.Now()), Gpus: []spec.HostGpu{}, Problems: []string{}}
	if name, err := os.Hostname(); err == nil && name != "" {
		machine.Hostname = &name
	}
	gpus, err := l.readGPUs(ctx)
	if err != nil {
		machine.Problems = append(machine.Problems, err.Error())
	}
	machine.Gpus = append(machine.Gpus, gpus...)
	proc := l.options.ProcDir
	if proc == "" {
		proc = "/proc"
	}
	if model, cores, err := readCPU(filepath.Join(proc, "cpuinfo")); err != nil {
		machine.Problems = append(machine.Problems, err.Error())
	} else {
		machine.CpuCores = &cores
		if model != "" {
			machine.CpuModel = &model
		}
	}
	if memory, err := readMemory(filepath.Join(proc, "meminfo")); err != nil {
		machine.Problems = append(machine.Problems, err.Error())
	} else {
		machine.MemoryBytes = &memory
	}
	return machine
}

func (l *Launcher) machine(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), machineTimeout)
	defer cancel()
	writeJSON(w, http.StatusOK, l.Machine(ctx))
}

// readGPUs asks nvidia-smi for each GPU's name, memory and driver.
func (l *Launcher) readGPUs(ctx context.Context) ([]spec.HostGpu, error) {
	binary := l.options.NvidiaSMI
	if binary == "" {
		binary = "nvidia-smi"
		if _, err := exec.LookPath(binary); err != nil {
			if _, statErr := os.Stat(wslNvidiaSMI); statErr == nil {
				binary = wslNvidiaSMI
			}
		}
	}
	cmd := exec.CommandContext(ctx, binary, "--query-gpu=name,memory.total,driver_version", "--format=csv,noheader,nounits")
	cmd.Env = append([]string{}, l.options.BaseEnv...)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("nvidia-smi: %w", err)
	}
	var gpus []spec.HostGpu
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Split(line, ",")
		if len(fields) != 3 {
			continue
		}
		mebibytes, err := strconv.Atoi(strings.TrimSpace(fields[1]))
		name := strings.TrimSpace(fields[0])
		if err != nil || mebibytes <= 0 || name == "" {
			return nil, fmt.Errorf("nvidia-smi: unreadable line %q", line)
		}
		gpu := spec.HostGpu{Model: name, MemoryBytes: mebibytes * bytesPerMiB}
		if driver := strings.TrimSpace(fields[2]); driver != "" {
			gpu.DriverVersion = &driver
		}
		gpus = append(gpus, gpu)
	}
	return gpus, nil
}

// readCPU counts the logical CPUs in /proc/cpuinfo and takes the first model name.
func readCPU(path string) (string, int, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("cpu: %w", err)
	}
	defer file.Close()
	model, cores := "", 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		key, value, found := strings.Cut(scanner.Text(), ":")
		if !found {
			continue
		}
		switch strings.TrimSpace(key) {
		case "processor":
			cores++
		case "model name":
			if model == "" {
				model = strings.TrimSpace(value)
			}
		}
	}
	if cores == 0 {
		return "", 0, fmt.Errorf("cpu: no processor in %s", path)
	}
	return model, cores, nil
}

// readMemory reads MemTotal from /proc/meminfo.
func readMemory(path string) (int, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("memory: %w", err)
	}
	for _, line := range strings.Split(string(content), "\n") {
		value, found := strings.CutPrefix(line, "MemTotal:")
		if !found {
			continue
		}
		kibibytes, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(value), " kB"))
		if err != nil || kibibytes <= 0 {
			return 0, fmt.Errorf("memory: unreadable MemTotal %q", line)
		}
		return kibibytes * bytesPerKiB, nil
	}
	return 0, fmt.Errorf("memory: no MemTotal in %s", path)
}
