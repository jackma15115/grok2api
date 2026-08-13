package resourcelimit

import (
	"errors"
	"testing"
)

func TestConfigureMemoryLimitUsesCgroupBudget(t *testing.T) {
	var configured int64
	status, err := configureMemoryLimit(
		func(string) string { return "" },
		func(path string) ([]byte, error) {
			if path == "/sys/fs/cgroup/memory.max" {
				return []byte("1073741824\n"), nil
			}
			return nil, errors.New("not found")
		},
		func(value int64) int64 {
			configured = value
			return 0
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Applied || status.Source != "cgroup" || status.ContainerBytes != 1<<30 {
		t.Fatalf("status = %#v", status)
	}
	if want := int64(1<<30) * autoMemoryLimitPercent / 100; status.GoBytes != want || configured != want {
		t.Fatalf("Go limit = %d, configured = %d, want %d", status.GoBytes, configured, want)
	}
}

func TestConfigureMemoryLimitRespectsGOMEMLIMIT(t *testing.T) {
	const current = int64(384 << 20)
	var requested int64
	status, err := configureMemoryLimit(
		func(name string) string {
			if name == "GOMEMLIMIT" {
				return "384MiB"
			}
			return ""
		},
		func(string) ([]byte, error) { return []byte("1073741824"), nil },
		func(value int64) int64 {
			requested = value
			return current
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if status.Applied || status.Source != "GOMEMLIMIT" || status.GoBytes != current || requested != -1 {
		t.Fatalf("status = %#v, requested = %d", status, requested)
	}
}

func TestConfigureMemoryLimitUsesExplicitSoftLimit(t *testing.T) {
	var configured int64
	status, err := configureMemoryLimit(
		func(name string) string {
			if name == memorySoftLimitEnv {
				return "768MiB"
			}
			return ""
		},
		func(string) ([]byte, error) { return []byte("1073741824"), nil },
		func(value int64) int64 {
			configured = value
			return 0
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Applied || status.Source != memorySoftLimitEnv || status.GoBytes != 768<<20 || configured != 768<<20 {
		t.Fatalf("status = %#v, configured = %d", status, configured)
	}
}

func TestConfigureMemoryLimitRejectsInvalidExplicitLimit(t *testing.T) {
	_, err := configureMemoryLimit(
		func(name string) string {
			if name == memorySoftLimitEnv {
				return "most-of-it"
			}
			return ""
		},
		func(string) ([]byte, error) { return nil, errors.New("not found") },
		func(int64) int64 { return 0 },
	)
	if err == nil {
		t.Fatal("invalid explicit memory limit was accepted")
	}
}

func TestDetectCgroupMemoryLimitIgnoresUnlimitedValues(t *testing.T) {
	values := map[string]string{
		"/sys/fs/cgroup/memory.max":                   "max",
		"/sys/fs/cgroup/memory/memory.limit_in_bytes": "9223372036854771712",
		"/sys/fs/cgroup/memory.limit_in_bytes":        "536870912",
	}
	limit := detectCgroupMemoryLimit(func(path string) ([]byte, error) {
		value, ok := values[path]
		if !ok {
			return nil, errors.New("not found")
		}
		return []byte(value), nil
	})
	if limit != 512<<20 {
		t.Fatalf("limit = %d, want %d", limit, 512<<20)
	}
}
