package launcher

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

const fakeCPUInfo = `processor	: 0
model name	: AMD Ryzen 7 5800X 8-Core Processor
processor	: 1
model name	: AMD Ryzen 7 5800X 8-Core Processor
`

func machineLauncher(t *testing.T, nvidiaSMI string) *Launcher {
	t.Helper()
	dir := t.TempDir()
	write := func(name, content string, mode os.FileMode) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("cpuinfo", fakeCPUInfo, 0o600)
	write("meminfo", "MemTotal:       32768000 kB\nMemFree:  1 kB\n", 0o600)
	smi := filepath.Join(dir, "missing-nvidia-smi")
	if nvidiaSMI != "" {
		smi = write("nvidia-smi", "#!/bin/sh\n"+nvidiaSMI, 0o700)
	}
	return New(Options{Token: token, ProcDir: dir, NvidiaSMI: smi})
}

func TestMachineReportsGPUsCPUAndMemory(t *testing.T) {
	l := machineLauncher(t, "echo 'NVIDIA GeForce RTX 3070, 8192, 581.57'\n")
	machine := l.Machine(context.Background())
	if len(machine.Problems) != 0 {
		t.Fatalf("problems: %v", machine.Problems)
	}
	want := spec.HostGpu{Model: "NVIDIA GeForce RTX 3070", MemoryBytes: 8192 * bytesPerMiB}
	if len(machine.Gpus) != 1 || machine.Gpus[0].Model != want.Model || machine.Gpus[0].MemoryBytes != want.MemoryBytes ||
		*machine.Gpus[0].DriverVersion != "581.57" {
		t.Errorf("gpus = %+v", machine.Gpus)
	}
	if *machine.CpuCores != 2 || *machine.CpuModel != "AMD Ryzen 7 5800X 8-Core Processor" || *machine.MemoryBytes != 32768000*bytesPerKiB {
		t.Errorf("cpu %v %v, memory %v", *machine.CpuCores, *machine.CpuModel, *machine.MemoryBytes)
	}
	encoded, _ := json.Marshal(machine)
	if err := spec.Validate(spec.SchemaHostMachine, encoded); err != nil {
		t.Errorf("breaks its schema: %v", err)
	}
}

func TestMachineNamesWhatItCannotRead(t *testing.T) {
	machine := machineLauncher(t, "").Machine(context.Background())
	if len(machine.Gpus) != 0 || len(machine.Problems) != 1 || !strings.HasPrefix(machine.Problems[0], "nvidia-smi:") {
		t.Errorf("gpus %v, problems %v", machine.Gpus, machine.Problems)
	}
	if machine.CpuCores == nil || machine.MemoryBytes == nil {
		t.Error("the CPU and memory were dropped with the GPUs")
	}
	encoded, _ := json.Marshal(machine)
	if err := spec.Validate(spec.SchemaHostMachine, encoded); err != nil {
		t.Errorf("breaks its schema: %v", err)
	}
}

func TestMachineNeedsTheToken(t *testing.T) {
	_, server := newTestLauncher(t)
	response, body := call(t, server, http.MethodGet, "/machine", nil)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"read_at"`) {
		t.Fatalf("GET /machine: %d %s", response.StatusCode, body)
	}
	anonymous, _ := http.Get(server.URL + "/machine")
	if anonymous.StatusCode != http.StatusUnauthorized {
		t.Errorf("without the token: %d", anonymous.StatusCode)
	}
	_ = anonymous.Body.Close()
}
