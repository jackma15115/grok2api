package account

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	accountdomain "github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/infra/persistence/relational"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
)

type webAccountProbeAdapter struct {
	status  int
	body    string
	err     error
	request provider.ResponseResourceRequest
}

// Provider 将测试适配器固定为 Grok Web。
func (a *webAccountProbeAdapter) Provider() accountdomain.Provider { return accountdomain.ProviderWeb }

// ForwardResponse 捕获探测请求并返回测试指定的响应。
func (a *webAccountProbeAdapter) ForwardResponse(_ context.Context, request provider.ResponseResourceRequest) (*provider.Response, error) {
	a.request = request
	if a.err != nil {
		return nil, a.err
	}
	return &provider.Response{StatusCode: a.status, Body: io.NopCloser(strings.NewReader(a.body))}, nil
}

// TestProbeWebAccountClassifiesChatResponse 验证真实对话状态不会把临时上游错误误判为凭据失效。
func TestProbeWebAccountClassifiesChatResponse(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantStatus WebAccountProbeStatus
		wantReply  string
	}{
		{name: "valid", status: http.StatusOK, body: `{"choices":[{"message":{"content":"Hello!"}}]}`, wantStatus: WebAccountProbeValid, wantReply: "Hello!"},
		{name: "unauthorized", status: http.StatusUnauthorized, body: `{"error":"unauthorized"}`, wantStatus: WebAccountProbeInvalid},
		{name: "blocked", status: http.StatusForbidden, body: `{"error":{"code":"blocked-user"}}`, wantStatus: WebAccountProbeInvalid},
		{name: "generic forbidden", status: http.StatusForbidden, body: `{"error":"cloudflare"}`, wantStatus: WebAccountProbeInconclusive},
		{name: "rate limited", status: http.StatusTooManyRequests, body: `{"error":"limited"}`, wantStatus: WebAccountProbeInconclusive},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, credential, adapter := newWebAccountProbeService(t)
			adapter.status, adapter.body = test.status, test.body
			result, err := service.ProbeWebAccount(context.Background(), credential.ID)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != test.wantStatus || result.UpstreamStatus != test.status || result.Reply != test.wantReply {
				t.Fatalf("result = %#v", result)
			}
			if adapter.request.Model != "grok-chat-fast" || adapter.request.Operation != "chat" || !strings.Contains(string(adapter.request.Body), `"content":"hi"`) {
				t.Fatalf("request = %#v", adapter.request)
			}
		})
	}
}

// TestProbeWebAccountReturnsTransportFailure 验证网络错误保留为内部错误，而不是伪造凭据状态。
func TestProbeWebAccountReturnsTransportFailure(t *testing.T) {
	service, credential, adapter := newWebAccountProbeService(t)
	adapter.err = errors.New("dial failed")
	if _, err := service.ProbeWebAccount(context.Background(), credential.ID); err == nil || !strings.Contains(err.Error(), "dial failed") {
		t.Fatalf("err = %v", err)
	}
}

// newWebAccountProbeService 为每个用例创建隔离的账号仓储和 Provider。
func newWebAccountProbeService(t *testing.T) (*Service, accountdomain.Credential, *webAccountProbeAdapter) {
	t.Helper()
	ctx := context.Background()
	database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "web-account-probe.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	accounts := relational.NewAccountRepository(database)
	credential, _, err := accounts.UpsertByIdentity(ctx, accountdomain.Credential{
		Provider: accountdomain.ProviderWeb, AuthType: accountdomain.AuthTypeSSO,
		Name: "web-probe", SourceKey: "web-probe", EncryptedAccessToken: "encrypted",
		Enabled: true, AuthStatus: accountdomain.AuthStatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter := &webAccountProbeAdapter{}
	return NewService(accounts, nil, nil, nil, provider.NewRegistry(adapter), nil, nil), credential, adapter
}
