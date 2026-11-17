package web

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chenyme/grok2api/backend/internal/domain/account"
	infraegress "github.com/chenyme/grok2api/backend/internal/infra/egress"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
)

// TestLiveWebQuotaAndFastChat exercises the configured Web adapter against
// Grok. Successful requests verify quota/chat behavior; they do not establish
// that Grok validates an optional legacy signature.
func TestLiveWebQuotaAndFastChat(t *testing.T) {
	sso := strings.TrimSpace(os.Getenv("GROK_LIVE_SSO"))
	if os.Getenv("GROK_LIVE_SSO_STDIN") == "1" {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && err != io.EOF {
			t.Fatal("could not read SSO from stdin")
		}
		sso = strings.TrimSpace(line)
	}
	if sso == "" {
		t.Skip("no live SSO supplied")
	}
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("GROK_LIVE_STATSIG_MODE")))
	if mode == "" {
		mode = statsigModeDisabled
	}
	if mode != statsigModeDisabled && mode != statsigModeLocal && mode != "signer" {
		t.Skip("GROK_LIVE_STATSIG_MODE must be disabled, local, or signer")
	}

	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("GROK_LIVE_BASE_URL")), "/")
	if baseURL == "" {
		baseURL = "https://grok.com"
	}
	cfg := Config{BaseURL: baseURL, StatsigMode: mode, QuotaTimeoutSeconds: 60, ChatTimeoutSeconds: 120}
	if mode == statsigModeLocal {
		cfg.StatsigMaterialURL = strings.TrimSpace(os.Getenv("GROK_LIVE_MATERIAL_URL"))
	}
	if mode == "signer" {
		cfg.StatsigMode = "url"
		cfg.StatsigSignerURL = strings.TrimSpace(os.Getenv("GROK_LIVE_SIGNER_URL"))
		if cfg.StatsigSignerURL == "" {
			t.Fatal("GROK_LIVE_SIGNER_URL is required in signer mode")
		}
	}

	cipher, err := security.NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	encryptedSSO, err := cipher.Encrypt(sso)
	if err != nil {
		t.Fatal(err)
	}
	manager := infraegress.NewManager(egressRepositoryStub{}, cipher)
	if solverURL := strings.TrimSpace(os.Getenv("GROK_LIVE_FLARESOLVERR_URL")); solverURL != "" {
		manager.UpdateClearanceConfig(infraegress.ClearanceConfig{
			Mode: "flaresolverr", FlareSolverrURL: solverURL, TargetURL: baseURL,
			Timeout: 90 * time.Second, RefreshInterval: 10 * time.Minute,
		})
	}

	credential := account.Credential{
		ID: 1, Provider: account.ProviderWeb, AuthType: account.AuthTypeSSO,
		EncryptedAccessToken: encryptedSSO, Enabled: true,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	adapter := NewAdapter(cfg, manager, cipher, nil, nil)
	window, err := adapter.SyncQuotaMode(ctx, credential, "auto")
	if err != nil {
		t.Fatalf("quota request failed with signing mode %s: %v", mode, err)
	}
	if window.Total <= 0 || window.Remaining < 0 || window.Remaining > window.Total {
		t.Fatalf("invalid quota window with signing mode %s", mode)
	}
	t.Logf("quota request succeeded with signing mode %s: remaining=%d total=%d", mode, window.Remaining, window.Total)

	chatBody, err := json.Marshal(map[string]any{
		"model":    "grok-chat-fast",
		"messages": []any{map[string]any{"role": "user", "content": "HI"}},
		"stream":   false,
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := adapter.ForwardResponse(ctx, provider.ResponseResourceRequest{
		Credential: credential, Method: http.MethodPost, Path: "/responses", Body: chatBody,
		Model: "grok-chat-fast", Operation: "chat",
	})
	if err != nil {
		t.Fatalf("Fast chat request failed with signing mode %s: %v", mode, err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatalf("Fast chat response failed with signing mode %s: %v", mode, err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Fast chat returned HTTP %d with signing mode %s", response.StatusCode, mode)
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(responseBody, &result); err != nil || len(result.Choices) == 0 || strings.TrimSpace(result.Choices[0].Message.Content) == "" {
		t.Fatalf("Fast chat returned no assistant text with signing mode %s", mode)
	}
	preview := []rune(strings.TrimSpace(result.Choices[0].Message.Content))
	if len(preview) > 200 {
		preview = preview[:200]
	}
	t.Logf("Fast chat completed with signing mode %s: response=%q", mode, string(preview))
}
