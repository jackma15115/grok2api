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
	localStatsigSeedBase64   = "AmawzIEMJXM6Sz8NetgNNGjfDmMzYpGmBm6M+MLKGfMtngNDlJnB7m+exx2Epiwc"
	localStatsigHEX          = "4844a90fd70a3d70a3d701c28f5c28f5c2901c28f5c28f5c290fd70a3d70a3d700"
	localStatsigPrefixBase64 = ""
	localStatsigDigestLength = 16
	localStatsigHasMarker    = true
)

var localStatsigSeed = mustDecodeLocalStatsigSeed(localStatsigSeedBase64)

type localStatsigMaterial struct {
	seed         []byte
	hex          string
	prefix       []byte
	digestLength int
	hasMarker    bool
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
	value, err := buildLocalStatsig(material, pathname, method, s.now().Unix(), key[0])
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
	if !ok || !validLocalStatsigMaterial(entry.material) || !now.Before(entry.expiresAt) {
		return localStatsigMaterial{}, false
	}
	return cloneLocalStatsigMaterial(entry.material), true
}

func (s *statsigSigner) storeLocalMaterial(key string, material localStatsigMaterial, expiresAt time.Time) {
	s.mu.Lock()
	s.localMaterials[key] = localStatsigMaterialEntry{material: cloneLocalStatsigMaterial(material), expiresAt: expiresAt}
	s.mu.Unlock()
}

func cloneLocalStatsigMaterial(material localStatsigMaterial) localStatsigMaterial {
	return localStatsigMaterial{
		seed:         append([]byte(nil), material.seed...),
		hex:          material.hex,
		prefix:       append([]byte(nil), material.prefix...),
		digestLength: material.digestLength,
		hasMarker:    material.hasMarker,
	}
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
	prefix, err := decodeOptionalLocalStatsigBase64(localStatsigPrefixBase64)
	if err != nil {
		panic("invalid embedded local Statsig prefix")
	}
	return localStatsigMaterial{
		seed:         localStatsigSeed,
		hex:          localStatsigHEX,
		prefix:       prefix,
		digestLength: localStatsigDigestLength,
		hasMarker:    localStatsigHasMarker,
	}
}

func newLocalStatsigMaterialPair(seedValue, hexValue string) (localStatsigMaterial, error) {
	return newLocalStatsigMaterial(seedValue, hexValue, "", 0, nil)
}

func newLocalStatsigMaterial(seedValue, hexValue, prefixValue string, digestLength int, hasMarkerValue *bool) (localStatsigMaterial, error) {
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
	prefix, err := decodeOptionalLocalStatsigBase64(prefixValue)
	if err != nil || len(prefix) > 8 {
		return localStatsigMaterial{}, errors.New("remote Statsig prefix is invalid")
	}
	if digestLength == 0 {
		digestLength = 16
	}
	if digestLength < 16 || digestLength > 32 {
		return localStatsigMaterial{}, errors.New("remote Statsig digest length is invalid")
	}
	hasMarker := true
	if hasMarkerValue != nil {
		hasMarker = *hasMarkerValue
	}
	return localStatsigMaterial{
		seed:         seed,
		hex:          hexValue,
		prefix:       prefix,
		digestLength: digestLength,
		hasMarker:    hasMarker,
	}, nil
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
		Seed         string `json:"seed"`
		HEX          string `json:"hex"`
		Prefix       string `json:"prefix"`
		DigestLength int    `json:"digestLength"`
		HasMarker    *bool  `json:"hasMarker"`
		ExpiresAt    string `json:"expiresAt"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return localStatsigMaterial{}, time.Time{}, errors.New("remote Statsig material returned invalid JSON")
	}
	material, err := newLocalStatsigMaterial(payload.Seed, payload.HEX, payload.Prefix, payload.DigestLength, payload.HasMarker)
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
	return buildLocalStatsig(embeddedLocalStatsigMaterial(), pathname, method, now.Unix(), key[0])
}

func buildLocalStatsig(material localStatsigMaterial, pathname, method string, nowUnix int64, key byte) (string, error) {
	if !validLocalStatsigMaterial(material) {
		return "", errors.New("local Statsig material is invalid")
	}
	if pathname == "" {
		pathname = "/"
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	number := uint32(nowUnix - statsigEpoch)

	var input strings.Builder
	input.Grow(len(method) + len(pathname) + len(material.hex) + 40)
	input.WriteString(method)
	input.WriteByte('!')
	input.WriteString(pathname)
	input.WriteByte('!')
	input.WriteString(strconv.FormatUint(uint64(number), 10))
	input.WriteString(statsigSalt)
	input.WriteString(material.hex)
	digest := sha256.Sum256([]byte(input.String()))

	markerLength := 0
	if material.hasMarker {
		markerLength = 1
	}
	output := make([]byte, len(material.prefix)+53+material.digestLength+markerLength)
	copy(output, material.prefix)
	offset := len(material.prefix)
	output[offset] = key
	for i := 0; i < 48; i++ {
		output[offset+1+i] = material.seed[i] ^ key
	}
	output[offset+49] = byte(number) ^ key
	output[offset+50] = byte(number>>8) ^ key
	output[offset+51] = byte(number>>16) ^ key
	output[offset+52] = byte(number>>24) ^ key
	for i := 0; i < material.digestLength; i++ {
		output[offset+53+i] = digest[i] ^ key
	}
	if material.hasMarker {
		output[len(output)-1] = statsigMark ^ key
	}
	return base64.RawStdEncoding.EncodeToString(output), nil
}

func validLocalStatsigMaterial(material localStatsigMaterial) bool {
	return len(material.seed) == 48 && material.hex != "" && len(material.prefix) <= 8 && material.digestLength >= 16 && material.digestLength <= 32
}

func decodeOptionalLocalStatsigBase64(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(value)
	}
	if err != nil {
		return nil, err
	}
	return decoded, nil
}

func mustDecodeLocalStatsigSeed(value string) []byte {
	seed, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil || len(seed) != 48 {
		panic("invalid embedded local Statsig seed")
	}
	return seed
}
