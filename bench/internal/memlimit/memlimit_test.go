package memlimit

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDetect(t *testing.T) {
	for _, testCase := range []struct {
		name, file, content string
		want                int64
		wantLimited         bool
	}{
		{"v2 limit", "memory.max", "1073741824\n", 1 << 30, true},
		{"v2 unlimited", "memory.max", "max\n", 0, false},
		{"v1 limit", "memory/memory.limit_in_bytes", "536870912\n", 1 << 29, true},
		{"v1 unlimited", "memory/memory.limit_in_bytes", "9223372036854771712\n", 0, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, testCase.file, testCase.content)
			limit, limited, err := Detect(root)
			if err != nil || limit != testCase.want || limited != testCase.wantLimited {
				t.Errorf("Detect = %d, %v, %v; want %d, %v", limit, limited, err, testCase.want, testCase.wantLimited)
			}
		})
	}
}

func TestDetectWithoutCgroup(t *testing.T) {
	if limit, limited, err := Detect(t.TempDir()); err != nil || limited || limit != 0 {
		t.Errorf("Detect = %d, %v, %v; want no limit", limit, limited, err)
	}
}

func TestDetectRejectsGarbage(t *testing.T) {
	root := t.TempDir()
	write(t, root, "memory.max", "lots\n")
	if _, _, err := Detect(root); err == nil {
		t.Error("an unparseable memory.max must be an error, not a silent 'unlimited'")
	}
}

func TestApplySoftLimit(t *testing.T) {
	root := t.TempDir()
	write(t, root, "memory.max", "1000000000\n")
	var set int64
	record := func(limit int64) int64 { set = limit; return 0 }
	noEnv := func(string) string { return "" }
	if soft, err := ApplySoftLimit(root, noEnv, record); err != nil || soft != 750000000 || set != soft {
		t.Errorf("soft = %d, set %d, err %v; want 75%% of the limit", soft, set, err)
	}
	set = 0
	explicit := func(name string) string { return map[string]string{"GOMEMLIMIT": "2GiB"}[name] }
	if soft, err := ApplySoftLimit(root, explicit, record); err != nil || soft != 0 || set != 0 {
		t.Errorf("an explicit GOMEMLIMIT was overridden: soft %d, set %d, err %v", soft, set, err)
	}
	if soft, err := ApplySoftLimit(t.TempDir(), noEnv, record); err != nil || soft != 0 {
		t.Errorf("no cgroup limit: soft %d, err %v; want none", soft, err)
	}
}
