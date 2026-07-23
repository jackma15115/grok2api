package web

import (
	"context"
	"encoding/base64"
	"errors"
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

func TestLocalStatsigUsesBuiltInMaterialWithoutRemoteURL(t *testing.T) {
	signer := newStatsigSigner()
	signer.fetchMaterial = func(context.Context, string) (localStatsigMaterial, time.Time, error) {
		t.Fatal("material service should not be called when URL is empty")
		return localStatsigMaterial{}, time.Time{}, nil
	}
	value, source, err := signer.SignLocal(context.Background(), "", "POST", "/rest/modes")
	if err != nil || source != "built-in" || !validStatsigID(value) {
		t.Fatalf("source=%q valid=%v err=%v", source, validStatsigID(value), err)
	}
}

func TestLocalStatsigUsesRemoteMaterial(t *testing.T) {
	now := time.Date(2026, 7, 23, 2, 0, 0, 0, time.UTC)
	signer := newStatsigSigner()
	signer.now = func() time.Time { return now }
	material, err := newLocalStatsigMaterialPair(
		"exHFyDNMkNhYgrQns67Q4eZZlzsta4qBAp8iQcn/a2mmXOBZ1m/BxScUEaJmhu8t",
		"25b52710051eb851eb851ec0051eb851eb851ec100",
	)
	if err != nil {
		t.Fatal(err)
	}
	signer.fetchMaterial = func(context.Context, string) (localStatsigMaterial, time.Time, error) {
		return material, now.Add(20 * time.Minute), nil
	}
	value, source, err := signer.SignLocal(context.Background(), "http://seed-hex-catch:8789/material", "POST", "/rest/modes")
	if err != nil || source != "remote" || !validStatsigID(value) {
		t.Fatalf("source=%q valid=%v err=%v", source, validStatsigID(value), err)
	}
	entry := signer.localMaterials["http://seed-hex-catch:8789/material"]
	if !entry.expiresAt.Equal(now.Add(remoteStatsigMaterialTTL)) {
		t.Fatalf("remote cache expiry = %v", entry.expiresAt)
	}
}

func TestLocalStatsigFallsBackWhenRemoteMaterialFails(t *testing.T) {
	signer := newStatsigSigner()
	fetches := 0
	signer.fetchMaterial = func(context.Context, string) (localStatsigMaterial, time.Time, error) {
		fetches++
		return localStatsigMaterial{}, time.Time{}, errors.New("collector unavailable")
	}

	value, source, err := signer.SignLocal(context.Background(), "http://seed-hex-catch:8789/material", "POST", "/rest/modes")
	if err != nil || source != "fallback" || !validStatsigID(value) {
		t.Fatalf("source=%q valid=%v err=%v", source, validStatsigID(value), err)
	}
	_, source, err = signer.SignLocal(context.Background(), "http://seed-hex-catch:8789/material", "POST", "/rest/modes")
	if err != nil || source != "cache" || fetches != 1 {
		t.Fatalf("cached fallback source=%q fetches=%d err=%v", source, fetches, err)
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
