package web

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBuildLocalStatsigMatchesBrowserCapture(t *testing.T) {
	const capturedID = "tQVp8pVbzXw8elYLQdJlTXuXWTrs0WOW7B7OkkngVLIoVQIP6RuOYPggmRzZEAZWOB6EpLMJZcIKFl5R7WvSNFypdNq/tg"
	const capturedSeed = "sNxHIO54yYnP4770Z9D4ziLsj1lk1iNZq3sn/FXhB53gt7pcrjvVTZUsqWyls+ON"
	const capturedHEX = "6c2b600ee147ae147ae1805c28f5c28f5c2805c28f5c28f5c280ee147ae147ae1800"
	captured, err := base64.RawStdEncoding.DecodeString(capturedID)
	if err != nil {
		t.Fatal(err)
	}
	material, err := newLocalStatsigMaterialPair(capturedSeed, capturedHEX)
	if err != nil {
		t.Fatal(err)
	}
	value, err := buildLocalStatsig(material, "/rest/modes", "POST", statsigEpoch+101790123, captured[0])
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

func TestRequestLocalStatsigMaterialPreservesPayloadShape(t *testing.T) {
	signer := newStatsigSigner()
	signer.validateEndpoint = func(context.Context, string) error { return nil }
	signer.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		body := `{"seed":"exHFyDNMkNhYgrQns67Q4eZZlzsta4qBAp8iQcn/a2mmXOBZ1m/BxScUEaJmhu8t","hex":"25b52710051eb851eb851ec0051eb851eb851ec100","prefix":"AgE","digestLength":16,"hasMarker":true}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})}
	material, _, err := signer.requestLocalMaterial(context.Background(), "http://seed-hex-catch:8789/material")
	if err != nil {
		t.Fatal(err)
	}
	if string(material.prefix) != "\x02\x01" || material.digestLength != 16 || !material.hasMarker {
		t.Fatalf("remote material shape = prefix:%x digest:%d marker:%v", material.prefix, material.digestLength, material.hasMarker)
	}
	value, err := buildLocalStatsig(material, "/rest/modes", "POST", statsigEpoch+1, 0x21)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil || len(decoded) != 72 || string(decoded[:2]) != "\x02\x01" {
		t.Fatalf("prefixed Statsig = length:%d prefix:%x err:%v", len(decoded), decoded[:min(2, len(decoded))], err)
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

func TestLocalStatsigMaterialCacheReplacesPairsAtomically(t *testing.T) {
	signer := newStatsigSigner()
	key := "http://seed-hex-catch:8789/material"
	expiresAt := time.Now().Add(time.Hour)
	first := localStatsigMaterial{seed: make([]byte, 48), hex: "aa", digestLength: 16, hasMarker: true}
	second := localStatsigMaterial{seed: make([]byte, 48), hex: "bb", prefix: []byte{2, 1}, digestLength: 16, hasMarker: true}
	for index := range second.seed {
		second.seed[index] = 0xff
	}
	signer.storeLocalMaterial(key, first, expiresAt)

	var wait sync.WaitGroup
	wait.Add(5)
	go func() {
		defer wait.Done()
		for index := 0; index < 10_000; index++ {
			if index%2 == 0 {
				signer.storeLocalMaterial(key, second, expiresAt)
			} else {
				signer.storeLocalMaterial(key, first, expiresAt)
			}
		}
	}()
	for range 4 {
		go func() {
			defer wait.Done()
			for range 10_000 {
				material, ok := signer.cachedLocalMaterial(key, time.Now())
				if !ok {
					t.Error("material disappeared during replacement")
					return
				}
				allZero, allFF := true, true
				for _, value := range material.seed {
					allZero = allZero && value == 0
					allFF = allFF && value == 0xff
				}
				firstPair := material.hex == "aa" && allZero && len(material.prefix) == 0
				secondPair := material.hex == "bb" && allFF && string(material.prefix) == "\x02\x01"
				if !firstPair && !secondPair {
					t.Errorf("observed mixed material pair: hex=%q seed=%x prefix=%x", material.hex, material.seed, material.prefix)
					return
				}
			}
		}()
	}
	wait.Wait()
}

// TestGenerateLocalStatsigProducesSeventyByteValue 验证本地生成入口输出协议要求的固定长度。
func TestGenerateLocalStatsigProducesSeventyByteValue(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	value, err := generateLocalStatsig("/rest/rate-limits", "post", now)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil || len(decoded) != 70 {
		t.Fatalf("decoded local Statsig length = %d, err = %v", len(decoded), err)
	}
	if decoded[69]^decoded[0] != statsigMark {
		t.Fatalf("decoded local Statsig mark = %x", decoded[69]^decoded[0])
	}
}

// TestBuildLocalStatsigChangesWithKey 使用确定性 key 验证随机掩码会改变完整签名。
func TestBuildLocalStatsigChangesWithKey(t *testing.T) {
	const nowUnix = statsigEpoch + 101790123
	material := embeddedLocalStatsigMaterial()
	first, err := buildLocalStatsig(material, "/rest/rate-limits", "POST", nowUnix, 0x12)
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildLocalStatsig(material, "/rest/rate-limits", "POST", nowUnix, 0x34)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("different local Statsig keys produced the same value")
	}
}
