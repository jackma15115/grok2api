package resourcelimit

import (
	"fmt"
	"math"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
)

const autoMemoryLimitPercent int64 = 80
const memorySoftLimitEnv = "GROK2API_MEMORY_SOFT_LIMIT"

var cgroupMemoryLimitPaths = []string{
	"/sys/fs/cgroup/memory.max",
	"/sys/fs/cgroup/memory/memory.limit_in_bytes",
	"/sys/fs/cgroup/memory.limit_in_bytes",
}

type MemoryLimit struct {
	Source         string
	ContainerBytes int64
	GoBytes        int64
	Applied        bool
}

// ConfigureMemoryLimit gives the Go runtime a budget below the container hard
// limit. GOMEMLIMIT remains the explicit override when operators set it.
func ConfigureMemoryLimit() (MemoryLimit, error) {
	return configureMemoryLimit(os.Getenv, os.ReadFile, debug.SetMemoryLimit)
}

func configureMemoryLimit(
	getenv func(string) string,
	readFile func(string) ([]byte, error),
	setMemoryLimit func(int64) int64,
) (MemoryLimit, error) {
	if strings.TrimSpace(getenv("GOMEMLIMIT")) != "" {
		return MemoryLimit{Source: "GOMEMLIMIT", GoBytes: setMemoryLimit(-1)}, nil
	}
	if value := strings.TrimSpace(getenv(memorySoftLimitEnv)); value != "" {
		goBytes, err := parseByteLimit(value)
		if err != nil {
			return MemoryLimit{}, fmt.Errorf("invalid %s: %w", memorySoftLimitEnv, err)
		}
		setMemoryLimit(goBytes)
		return MemoryLimit{Source: memorySoftLimitEnv, GoBytes: goBytes, Applied: true}, nil
	}

	containerBytes := detectCgroupMemoryLimit(readFile)
	if containerBytes <= 0 {
		return MemoryLimit{}, nil
	}
	goBytes := containerBytes/100*autoMemoryLimitPercent + containerBytes%100*autoMemoryLimitPercent/100
	setMemoryLimit(goBytes)
	return MemoryLimit{
		Source:         "cgroup",
		ContainerBytes: containerBytes,
		GoBytes:        goBytes,
		Applied:        true,
	}, nil
}

func parseByteLimit(value string) (int64, error) {
	value = strings.TrimSpace(value)
	index := 0
	for index < len(value) && value[index] >= '0' && value[index] <= '9' {
		index++
	}
	if index == 0 {
		return 0, fmt.Errorf("expected a positive byte count such as 768MiB")
	}
	number, err := strconv.ParseInt(value[:index], 10, 64)
	if err != nil || number <= 0 {
		return 0, fmt.Errorf("expected a positive byte count such as 768MiB")
	}
	multipliers := map[string]int64{
		"": 1, "b": 1,
		"kb": 1000, "mb": 1000 * 1000, "gb": 1000 * 1000 * 1000, "tb": 1000 * 1000 * 1000 * 1000,
		"kib": 1 << 10, "mib": 1 << 20, "gib": 1 << 30, "tib": 1 << 40,
	}
	multiplier, ok := multipliers[strings.ToLower(strings.TrimSpace(value[index:]))]
	if !ok {
		return 0, fmt.Errorf("unsupported unit in %q", value)
	}
	if number > math.MaxInt64/multiplier {
		return 0, fmt.Errorf("value %q is too large", value)
	}
	return number * multiplier, nil
}

func detectCgroupMemoryLimit(readFile func(string) ([]byte, error)) int64 {
	var detected int64
	for _, path := range cgroupMemoryLimitPaths {
		value, err := readFile(path)
		if err != nil {
			continue
		}
		limit, err := strconv.ParseInt(strings.TrimSpace(string(value)), 10, 64)
		if err != nil || limit <= 0 || limit >= 1<<60 {
			continue
		}
		if detected == 0 || limit < detected {
			detected = limit
		}
	}
	return detected
}
