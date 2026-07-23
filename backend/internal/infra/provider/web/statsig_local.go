package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	statsigModeLocal         = "local"
	statsigEpoch             = 1682924400
	statsigSalt              = "obfiowerehiring"
	statsigMark              = 0x03
	remoteStatsigMaterialTTL = 10 * time.Minute
	localStatsigFallbackTTL  = time.Minute
	localStatsigSeedBase64   = "sNxHIO54yYnP4770Z9D4ziLsj1lk1iNZq3sn/FXhB53gt7pcrjvVTZUsqWyls+ON"
	localStatsigHEX          = "6c2b600ee147ae147ae1805c28f5c28f5c2805c28f5c28f5c280ee147ae147ae1800"
)

var localStatsigSeed = mustDecodeLocalStatsigSeed(localStatsigSeedBase64)

type localStatsigMaterial struct {
	seed []byte
	hex  string
}

type localStatsigMaterialEntry struct {
	material  localStatsigMaterial
	expiresAt time.Time
}

type localStatsigMaterialResult struct {
	material localStatsigMaterial
	source   string
}

func (s *statsigSigner) SignLocal(ctx context.Context, materialURL, method, pathname string) (string, string, error) {
	material, source := embeddedLocalStatsigMaterial(), "built-in"
	if strings.TrimSpace(materialURL) != "" {
		material, source = s.localMaterial(ctx, materialURL)
	}
	var key [1]byte
	if _, err := rand.Read(key[:]); err != nil {
		return "", "", err
	}
	value, err := buildLocalStatsig(material.seed, material.hex, pathname, method, s.now().Unix(), key[0])
	if err != nil {
		return "", "", err
	}
	return value, source, nil
}

func (s *statsigSigner) localMaterial(ctx context.Context, materialURL string) (localStatsigMaterial, string) {
	cacheKey := strings.TrimSpace(materialURL)
	now := s.now().UTC()
	if material, ok := s.cachedLocalMaterial(cacheKey, now); ok {
		return material, "cache"
	}
	value, _, _ := s.refreshes.Do("local-material\x00"+cacheKey, func() (any, error) {
		now := s.now().UTC()
		if material, ok := s.cachedLocalMaterial(cacheKey, now); ok {
			return localStatsigMaterialResult{material: material, source: "cache"}, nil
		}
		material, expiresAt, err := s.fetchMaterial(ctx, cacheKey)
		if err != nil {
			material = embeddedLocalStatsigMaterial()
			s.storeLocalMaterial(cacheKey, material, now.Add(localStatsigFallbackTTL))
			return localStatsigMaterialResult{material: material, source: "fallback"}, nil
		}
		cacheUntil := now.Add(remoteStatsigMaterialTTL)
		if expiresAt.IsZero() || expiresAt.After(cacheUntil) {
			expiresAt = cacheUntil
		}
		s.storeLocalMaterial(cacheKey, material, expiresAt)
		return localStatsigMaterialResult{material: material, source: "remote"}, nil
	})
	result := value.(localStatsigMaterialResult)
	return result.material, result.source
}

func (s *statsigSigner) cachedLocalMaterial(key string, now time.Time) (localStatsigMaterial, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.localMaterials[key]
	if !ok || len(entry.material.seed) != 48 || entry.material.hex == "" || !now.Before(entry.expiresAt) {
		return localStatsigMaterial{}, false
	}
	return entry.material, true
}

func (s *statsigSigner) storeLocalMaterial(key string, material localStatsigMaterial, expiresAt time.Time) {
	s.mu.Lock()
	s.localMaterials[key] = localStatsigMaterialEntry{material: material, expiresAt: expiresAt}
	s.mu.Unlock()
}

func (s *statsigSigner) InvalidateLocal(materialURL string) {
	key := strings.TrimSpace(materialURL)
	if key == "" {
		return
	}
	s.mu.Lock()
	delete(s.localMaterials, key)
	s.mu.Unlock()
}

func embeddedLocalStatsigMaterial() localStatsigMaterial {
	return localStatsigMaterial{seed: localStatsigSeed, hex: localStatsigHEX}
}

func newLocalStatsigMaterialPair(seedValue, hexValue string) (localStatsigMaterial, error) {
	seedValue = strings.TrimSpace(seedValue)
	hexValue = strings.ToLower(strings.TrimSpace(hexValue))
	seed, err := base64.StdEncoding.DecodeString(seedValue)
	if err != nil {
		seed, err = base64.RawStdEncoding.DecodeString(seedValue)
	}
	if err != nil || len(seed) != 48 {
		return localStatsigMaterial{}, fmt.Errorf("remote Statsig seed must decode to 48 bytes")
	}
	if len(hexValue) < 8 || len(hexValue) > 256 {
		return localStatsigMaterial{}, errors.New("remote Statsig hex is invalid")
	}
	for _, value := range hexValue {
		if (value < '0' || value > '9') && (value < 'a' || value > 'f') {
			return localStatsigMaterial{}, errors.New("remote Statsig hex is invalid")
		}
	}
	return localStatsigMaterial{seed: seed, hex: hexValue}, nil
}

func (s *statsigSigner) requestLocalMaterial(ctx context.Context, endpoint string) (localStatsigMaterial, time.Time, error) {
	if err := s.validateEndpoint(ctx, endpoint); err != nil {
		return localStatsigMaterial{}, time.Time{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return localStatsigMaterial{}, time.Time{}, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return localStatsigMaterial{}, time.Time{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, statsigMaterialLimit+1))
	if err != nil {
		return localStatsigMaterial{}, time.Time{}, err
	}
	if len(body) > statsigMaterialLimit {
		return localStatsigMaterial{}, time.Time{}, errors.New("remote Statsig material response is too large")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return localStatsigMaterial{}, time.Time{}, fmt.Errorf("remote Statsig material returned %d", response.StatusCode)
	}
	var payload struct {
		Seed      string `json:"seed"`
		HEX       string `json:"hex"`
		ExpiresAt string `json:"expiresAt"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return localStatsigMaterial{}, time.Time{}, errors.New("remote Statsig material returned invalid JSON")
	}
	material, err := newLocalStatsigMaterialPair(payload.Seed, payload.HEX)
	if err != nil {
		return localStatsigMaterial{}, time.Time{}, err
	}
	var expiresAt time.Time
	if strings.TrimSpace(payload.ExpiresAt) != "" {
		expiresAt, err = time.Parse(time.RFC3339, payload.ExpiresAt)
		if err != nil || !expiresAt.After(s.now().UTC()) {
			return localStatsigMaterial{}, time.Time{}, errors.New("remote Statsig material is expired")
		}
	}
	return material, expiresAt, nil
}

func generateLocalStatsig(pathname, method string, now time.Time) (string, error) {
	var key [1]byte
	if _, err := rand.Read(key[:]); err != nil {
		return "", err
	}
	return buildLocalStatsig(localStatsigSeed, localStatsigHEX, pathname, method, now.Unix(), key[0])
}

func buildLocalStatsig(seed []byte, hexValue, pathname, method string, nowUnix int64, key byte) (string, error) {
	if len(seed) != 48 {
		return "", errors.New("local Statsig seed must contain 48 bytes")
	}
	if pathname == "" {
		pathname = "/"
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	number := uint32(nowUnix - statsigEpoch)

	var input strings.Builder
	input.Grow(len(method) + len(pathname) + len(hexValue) + 40)
	input.WriteString(method)
	input.WriteByte('!')
	input.WriteString(pathname)
	input.WriteByte('!')
	input.WriteString(strconv.FormatUint(uint64(number), 10))
	input.WriteString(statsigSalt)
	input.WriteString(hexValue)
	digest := sha256.Sum256([]byte(input.String()))

	output := make([]byte, 70)
	output[0] = key
	for i := 0; i < 48; i++ {
		output[1+i] = seed[i] ^ key
	}
	output[49] = byte(number) ^ key
	output[50] = byte(number>>8) ^ key
	output[51] = byte(number>>16) ^ key
	output[52] = byte(number>>24) ^ key
	for i := 0; i < 16; i++ {
		output[53+i] = digest[i] ^ key
	}
	output[69] = statsigMark ^ key
	return base64.RawStdEncoding.EncodeToString(output), nil
}

func mustDecodeLocalStatsigSeed(value string) []byte {
	seed, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil || len(seed) != 48 {
		panic("invalid embedded local Statsig seed")
	}
	return seed
}
