package web

import (
	"encoding/base64"
	"testing"
	"time"
)

func TestBuildLocalStatsigMatchesBrowserCapture(t *testing.T) {
	const capturedID = "tQVp8pVbzXw8elYLQdJlTXuXWTrs0WOW7B7OkkngVLIoVQIP6RuOYPggmRzZEAZWOB6EpLMJZcIKFl5R7WvSNFypdNq/tg"
	captured, err := base64.RawStdEncoding.DecodeString(capturedID)
	if err != nil {
		t.Fatal(err)
	}
	value, err := buildLocalStatsig(localStatsigSeed, localStatsigHEX, "/rest/modes", "POST", statsigEpoch+101790123, captured[0])
	if err != nil {
		t.Fatal(err)
	}
	if value != capturedID {
		t.Fatal("local Statsig does not match browser capture")
	}
}

func TestGenerateLocalStatsigProducesFreshSeventyByteValues(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	first, err := generateLocalStatsig("/rest/rate-limits", "post", now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := generateLocalStatsig("/rest/rate-limits", "POST", now)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.RawStdEncoding.DecodeString(first)
	if err != nil || len(decoded) != 70 {
		t.Fatalf("decoded local Statsig length = %d, err = %v", len(decoded), err)
	}
	if first == second {
		t.Fatal("two local Statsig values unexpectedly matched")
	}
}
