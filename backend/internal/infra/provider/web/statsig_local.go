package web

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

const (
	statsigModeLocal = "local"
	statsigEpoch     = 1682924400
	statsigSalt      = "obfiowerehiring"
	statsigMark      = 0x03

	// Captured from a browser and cross-checked against Grok's 70-byte output.
	localStatsigSeedBase64 = "t2ODAFY4ozXd0K2Y8MdI2XfxTDiJoakZPuoaKfcQn8VuasZMcKliyhA1pJ+o1oMf"
	localStatsigHEX        = "3bab9506b851eb851eb840e8f5c28f5c28f80e8f5c28f5c28f806b851eb851eb8400"
)

var localStatsigSeed = mustDecodeLocalStatsigSeed(localStatsigSeedBase64)

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
