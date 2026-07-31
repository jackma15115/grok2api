package account

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	accountdomain "github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
)

const (
	webAccountProbeTimeout       = 45 * time.Second
	webAccountProbeResponseLimit = 256 << 10
	webAccountProbeReplyRunes    = 200
)

// WebAccountProbeStatus 表示一次固定对话对凭据有效性的判定。
type WebAccountProbeStatus string

const (
	// WebAccountProbeValid 只用于上游成功接受并完成对话的情况。
	WebAccountProbeValid WebAccountProbeStatus = "valid"
	// WebAccountProbeInvalid 只用于 401 或明确的账号封禁响应。
	WebAccountProbeInvalid WebAccountProbeStatus = "invalid"
	// WebAccountProbeInconclusive 覆盖限流、临时服务故障和通用风控拒绝。
	WebAccountProbeInconclusive WebAccountProbeStatus = "inconclusive"
)

// WebAccountProbeResult 描述固定 hi 对话的诊断结果，不包含凭据或上游错误正文。
type WebAccountProbeResult struct {
	Status         WebAccountProbeStatus
	UpstreamStatus int
	Reply          string
}

// ProbeWebAccount 固定使用指定 Grok Web 账号发送最小 hi 对话，不经过账号池，也不修改账号状态。
func (s *Service) ProbeWebAccount(ctx context.Context, id uint64) (WebAccountProbeResult, error) {
	credential, err := s.accounts.Get(ctx, id)
	if err != nil {
		return WebAccountProbeResult{}, mapRepositoryError(err)
	}
	if credential.Provider != accountdomain.ProviderWeb {
		return WebAccountProbeResult{}, fmt.Errorf("%w: 仅 Grok Web 账号支持对话测试", ErrUnsupported)
	}
	if s.providers == nil {
		return WebAccountProbeResult{}, errors.New("Provider 注册表未初始化")
	}
	adapter, ok := s.providers.Responses(accountdomain.ProviderWeb)
	if !ok {
		return WebAccountProbeResult{}, fmt.Errorf("%w: Grok Web 对话 Provider 未注册", ErrUnsupported)
	}

	probeCtx, cancel := context.WithTimeout(ctx, webAccountProbeTimeout)
	defer cancel()
	response, err := adapter.ForwardResponse(probeCtx, provider.ResponseResourceRequest{
		Credential: credential,
		Method:     http.MethodPost,
		Path:       "/responses",
		Body:       []byte(`{"model":"grok-chat-fast","messages":[{"role":"user","content":"hi"}],"max_tokens":16,"stream":false}`),
		Model:      "grok-chat-fast",
		Operation:  "chat",
	})
	if err != nil {
		return WebAccountProbeResult{}, fmt.Errorf("发送 Grok Web 测试对话: %w", err)
	}
	if response == nil || response.Body == nil {
		return WebAccountProbeResult{}, errors.New("Grok Web 测试对话缺少响应")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, webAccountProbeResponseLimit+1))
	if err != nil {
		return WebAccountProbeResult{}, fmt.Errorf("读取 Grok Web 测试响应: %w", err)
	}
	if len(body) > webAccountProbeResponseLimit {
		return WebAccountProbeResult{}, errors.New("Grok Web 测试响应超过安全上限")
	}

	result := WebAccountProbeResult{Status: WebAccountProbeInconclusive, UpstreamStatus: response.StatusCode}
	switch {
	case response.StatusCode == http.StatusUnauthorized:
		result.Status = WebAccountProbeInvalid
	case response.StatusCode == http.StatusForbidden && provider.IsDefinitiveAccountBlockBody(body):
		result.Status = WebAccountProbeInvalid
	case response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices:
		result.Status = WebAccountProbeValid
		result.Reply = webAccountProbeReply(body)
	}
	return result, nil
}

// webAccountProbeReply 只提取并限制助手文本，避免把完整 Provider 响应暴露到管理端。
func webAccountProbeReply(body []byte) string {
	var payload struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(body, &payload) != nil || len(payload.Choices) == 0 {
		return ""
	}
	reply := strings.TrimSpace(payload.Choices[0].Message.Content)
	if utf8.RuneCountInString(reply) <= webAccountProbeReplyRunes {
		return reply
	}
	return string([]rune(reply)[:webAccountProbeReplyRunes])
}
