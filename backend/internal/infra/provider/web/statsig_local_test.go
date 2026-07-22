package web

import (
	"encoding/base64"
	"testing"
	"time"
)

func TestBuildLocalStatsigMatchesBrowserCapture(t *testing.T) {
	const seedBase64 = "1zyptn4udMOQU5tdgJBcp9Zu71BLajtU53nI+Mi+4VPN+d9BXkYvGxeEnBRa3ow0"
	seed, err := base64.RawStdEncoding.DecodeString(seedBase64)
	if err != nil {
		t.Fatal(err)
	}
	value, err := buildLocalStatsig(seed, "ad36d100100", "/rest/app-chat/conversations/new", "POST", statsigEpoch+99789180, 143)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{143, 88, 179, 38, 57, 241, 161, 251, 76, 31, 220, 20, 210, 15, 31, 211, 40, 89, 225, 96, 223, 196, 229, 180, 219, 104, 246, 71, 119, 71, 49, 110, 220, 66, 118, 80, 206, 209, 201, 160, 148, 152, 11, 19, 155, 213, 81, 3, 187, 243, 38, 125, 138, 27, 62, 96, 63, 212, 65, 52, 228, 53, 177, 114, 125, 99, 165, 182, 110, 140}
	decoded, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != string(want) {
		t.Fatalf("local Statsig does not match browser capture: %v", decoded)
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
