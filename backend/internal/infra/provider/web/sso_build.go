package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	accountdomain "github.com/chenyme/grok2api/backend/internal/domain/account"
	egressdomain "github.com/chenyme/grok2api/backend/internal/domain/egress"
	infraegress "github.com/chenyme/grok2api/backend/internal/infra/egress"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
	"golang.org/x/net/html"
)

const (
	ssoBuildClientID = "b1a00492-073a-47ea-816f-4c329264a828"
	ssoBuildScope    = "openid profile email offline_access grok-cli:access api:access conversations:read conversations:write"
	ssoDeviceURL     = "https://auth.x.ai/oauth2/device/code"
	ssoVerifyURL     = "https://auth.x.ai/oauth2/device/verify"
	ssoApproveURL    = "https://auth.x.ai/oauth2/device/approve"
	ssoTokenURL      = "https://auth.x.ai/oauth2/token"
	maxAuthBody      = 2 << 20
)

type ssoBuildHTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

type ssoBuildFlow struct {
	client    ssoBuildHTTPClient
	userAgent string
	cookies   map[string]string
}

func (a *Adapter) ConvertToBuild(ctx context.Context, credential accountdomain.Credential) (provider.CredentialSeed, error) {
	if credential.Provider != accountdomain.ProviderWeb || credential.AuthType != accountdomain.AuthTypeSSO {
		return provider.CredentialSeed{}, fmt.Errorf("仅 Grok Web SSO 账号支持转换")
	}
	token, err := a.cipher.Decrypt(credential.EncryptedAccessToken)
	if err != nil {
		return provider.CredentialSeed{}, fmt.Errorf("解密 Grok Web SSO: %w", err)
	}
	token = normalizeSSOToken(token)
	if token == "" {
		return provider.CredentialSeed{}, provider.ErrUnauthorized
	}
	lease, err := a.egress.AcquireCredential(ctx, egressdomain.ScopeWeb, credential)
	if err != nil {
		return provider.CredentialSeed{}, err
	}
	defer lease.Release()
	requestCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	flow := &ssoBuildFlow{
		client: lease, userAgent: lease.UserAgent,
		cookies: map[string]string{"sso": token, "sso-rw": token},
	}
	seed, err := flow.convert(requestCtx, credential)
	if err != nil {
		a.egress.Feedback(context.WithoutCancel(ctx), lease.NodeID, conversionStatus(err), err)
		return provider.CredentialSeed{}, err
	}
	a.egress.Feedback(context.WithoutCancel(ctx), lease.NodeID, http.StatusOK, nil)
	return seed, nil
}

func (f *ssoBuildFlow) convert(ctx context.Context, credential accountdomain.Credential) (provider.CredentialSeed, error) {
	form := url.Values{"client_id": {ssoBuildClientID}, "scope": {ssoBuildScope}}
	status, _, body, err := f.do(ctx, http.MethodPost, ssoDeviceURL, form)
	if err != nil {
		return provider.CredentialSeed{}, err
	}
	if status < 200 || status >= 300 {
		return provider.CredentialSeed{}, fmt.Errorf("xAI Device Flow 启动失败: %w", conversionHTTPError{status: status})
	}
	var device struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
		Interval   int    `json:"interval"`
		ExpiresIn  int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &device); err != nil {
		return provider.CredentialSeed{}, fmt.Errorf("解析 xAI Device Flow: %w", err)
	}
	if device.DeviceCode == "" || device.UserCode == "" {
		return provider.CredentialSeed{}, fmt.Errorf("xAI Device Flow 返回字段不完整")
	}
	if device.Interval <= 0 {
		device.Interval = 5
	}
	if device.ExpiresIn <= 0 {
		device.ExpiresIn = 1800
	}

	status, finalURL, _, err := f.doWithFollow(ctx, http.MethodPost, ssoVerifyURL, url.Values{"user_code": {device.UserCode}}, false)
	if err != nil {
		return provider.CredentialSeed{}, err
	}
	if status == http.StatusUnauthorized {
		return provider.CredentialSeed{}, provider.ErrUnauthorized
	}
	if status < 200 || status >= 400 {
		return provider.CredentialSeed{}, fmt.Errorf("SSO 自动验证 Device Flow 失败: %w", conversionHTTPError{status: status})
	}
	if redirectState := ssoDeviceRedirectState(finalURL); redirectState != "consent" {
		if redirectState == "sign-in" {
			return provider.CredentialSeed{}, provider.ErrUnauthorized
		}
		return provider.CredentialSeed{}, fmt.Errorf("SSO 自动验证 Device Flow 失败")
	}

	// xAI 的 consent 页面会签发一次性的 consent_token。不能根据 verify 的
	// 重定向位置直接拼 approve 表单，否则 Cloudflare 会以请求无法验证拒绝。
	status, consentURL, consentBody, err := f.do(ctx, http.MethodGet, finalURL, nil)
	if err != nil {
		return provider.CredentialSeed{}, err
	}
	if ssoDeviceRedirectState(consentURL) == "sign-in" || status == http.StatusUnauthorized {
		return provider.CredentialSeed{}, provider.ErrUnauthorized
	}
	if status < 200 || status >= 300 {
		return provider.CredentialSeed{}, fmt.Errorf("加载 xAI Device Flow 授权页失败: %w", conversionHTTPError{status: status})
	}
	approveURL, approveForm, err := parseSSOConsentForm(consentURL, consentBody, device.UserCode)
	if err != nil {
		return provider.CredentialSeed{}, err
	}
	consentOrigin, err := xaiURLOrigin(consentURL)
	if err != nil {
		return provider.CredentialSeed{}, err
	}
	status, finalURL, _, err = f.doWithFollowHeaders(ctx, http.MethodPost, approveURL, approveForm, false, http.Header{
		"Accept":                    {"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"},
		"Origin":                    {consentOrigin},
		"Referer":                   {consentURL},
		"Sec-Fetch-Dest":            {"document"},
		"Sec-Fetch-Mode":            {"navigate"},
		"Sec-Fetch-Site":            {"same-site"},
		"Upgrade-Insecure-Requests": {"1"},
	})
	if err != nil {
		return provider.CredentialSeed{}, err
	}
	if status < 200 || status >= 400 {
		return provider.CredentialSeed{}, fmt.Errorf("SSO 自动批准 Device Flow 失败: %w", conversionHTTPError{status: status})
	}
	if redirectState := ssoDeviceRedirectState(finalURL); redirectState != "done" {
		if redirectState == "sign-in" {
			return provider.CredentialSeed{}, provider.ErrUnauthorized
		}
		return provider.CredentialSeed{}, fmt.Errorf("SSO 自动批准 Device Flow 失败")
	}

	token, err := f.pollToken(ctx, device.DeviceCode, time.Duration(device.Interval)*time.Second, time.Duration(device.ExpiresIn)*time.Second)
	if err != nil {
		return provider.CredentialSeed{}, err
	}
	claims := decodeBuildClaims(firstValue(token.IDToken, token.AccessToken))
	userID := claimString(claims, "sub")
	email := claimString(claims, "email")
	teamID := claimString(claims, "team_id")
	name := strings.TrimSpace(credential.Name)
	if name == "" {
		name = "Grok Web account"
	}
	return provider.CredentialSeed{
		Provider: accountdomain.ProviderBuild, AuthType: accountdomain.AuthTypeOAuth,
		Name: firstValue(email, name+" Build", userID, "Grok Build account"), Email: email, UserID: userID, TeamID: teamID,
		SourceKey: "sso-build:" + security.HashToken(token.AccessToken), OIDCClientID: ssoBuildClientID,
		AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, ExpiresAt: token.ExpiresAt,
	}, nil
}

type ssoBuildToken struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	ExpiresAt    time.Time
}

func (f *ssoBuildFlow) pollToken(ctx context.Context, deviceCode string, interval, expiresIn time.Duration) (ssoBuildToken, error) {
	if interval < time.Second {
		interval = time.Second
	}
	deadline := time.Now().Add(min(expiresIn, 75*time.Second))
	for time.Now().Before(deadline) {
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ssoBuildToken{}, ctx.Err()
		case <-timer.C:
		}
		status, _, body, err := f.do(ctx, http.MethodPost, ssoTokenURL, url.Values{
			"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}, "client_id": {ssoBuildClientID}, "device_code": {deviceCode},
		})
		if err != nil {
			return ssoBuildToken{}, err
		}
		var payload struct {
			AccessToken      string `json:"access_token"`
			RefreshToken     string `json:"refresh_token"`
			IDToken          string `json:"id_token"`
			ExpiresIn        int    `json:"expires_in"`
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return ssoBuildToken{}, fmt.Errorf("解析 xAI OAuth Token: %w", err)
		}
		if status >= 200 && status < 300 && payload.AccessToken != "" {
			if payload.ExpiresIn <= 0 {
				payload.ExpiresIn = 3600
			}
			return ssoBuildToken{AccessToken: payload.AccessToken, RefreshToken: payload.RefreshToken, IDToken: payload.IDToken, ExpiresAt: time.Now().UTC().Add(time.Duration(payload.ExpiresIn) * time.Second)}, nil
		}
		switch payload.Error {
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5 * time.Second
			continue
		case "access_denied", "expired_token":
			return ssoBuildToken{}, provider.ErrAuthorizationDenied
		default:
			if status >= 400 {
				return ssoBuildToken{}, fmt.Errorf("xAI OAuth Token 失败 (%s): %w", firstValue(payload.ErrorDescription, payload.Error), conversionHTTPError{status: status})
			}
			return ssoBuildToken{}, fmt.Errorf("xAI OAuth Token 失败: %s", firstValue(payload.ErrorDescription, payload.Error, strconv.Itoa(status)))
		}
	}
	return ssoBuildToken{}, fmt.Errorf("xAI Device Flow 轮询超时")
}

func (f *ssoBuildFlow) do(ctx context.Context, method, endpoint string, form url.Values) (int, string, []byte, error) {
	return f.doWithFollow(ctx, method, endpoint, form, true)
}

// doWithFollow 在 follow=false 时遇到 3xx 直接返回状态码与解析后的 Location 作为 finalURL，
// 用于重定向目标域会被 Cloudflare 拦截（accounts.x.ai）的请求。
func (f *ssoBuildFlow) doWithFollow(ctx context.Context, method, endpoint string, form url.Values, follow bool) (int, string, []byte, error) {
	return f.doWithFollowHeaders(ctx, method, endpoint, form, follow, nil)
}

func (f *ssoBuildFlow) doWithFollowHeaders(ctx context.Context, method, endpoint string, form url.Values, follow bool, headers http.Header) (int, string, []byte, error) {
	if !safeXAIURL(endpoint) {
		return 0, "", nil, fmt.Errorf("xAI OAuth URL 不安全")
	}
	currentURL := endpoint
	currentMethod := method
	currentForm := form
	for redirects := 0; redirects <= 8; redirects++ {
		var body io.Reader
		if currentForm != nil {
			body = strings.NewReader(currentForm.Encode())
		}
		request, err := http.NewRequestWithContext(ctx, currentMethod, currentURL, body)
		if err != nil {
			return 0, "", nil, err
		}
		request.Header.Set("Accept", "application/json, text/html;q=0.9, */*;q=0.8")
		request.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
		for name, values := range headers {
			request.Header.Del(name)
			for _, value := range values {
				request.Header.Add(name, value)
			}
		}
		request.Header.Set("User-Agent", f.userAgent)
		request.Header.Set("Cookie", f.cookieHeader())
		if currentForm != nil {
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		response, err := f.client.Do(request)
		if err != nil {
			return 0, "", nil, err
		}
		f.captureCookies(response)
		data, readErr := io.ReadAll(io.LimitReader(response.Body, maxAuthBody+1))
		_ = response.Body.Close()
		if readErr != nil {
			return response.StatusCode, currentURL, nil, readErr
		}
		if len(data) > maxAuthBody {
			return response.StatusCode, currentURL, nil, fmt.Errorf("xAI OAuth 响应超过 2 MiB")
		}
		if response.StatusCode < 300 || response.StatusCode > 399 {
			return response.StatusCode, currentURL, data, nil
		}
		location := strings.TrimSpace(response.Header.Get("Location"))
		if location == "" {
			return response.StatusCode, currentURL, data, fmt.Errorf("xAI OAuth 重定向缺少 Location")
		}
		base, _ := url.Parse(currentURL)
		next, err := url.Parse(location)
		if err != nil {
			return response.StatusCode, currentURL, data, err
		}
		currentURL = base.ResolveReference(next).String()
		if !safeXAIURL(currentURL) {
			return response.StatusCode, currentURL, data, fmt.Errorf("xAI OAuth 重定向到非受信域名")
		}
		if !follow {
			return response.StatusCode, currentURL, nil, nil
		}
		if response.StatusCode == http.StatusSeeOther || ((response.StatusCode == http.StatusMovedPermanently || response.StatusCode == http.StatusFound) && currentMethod != http.MethodGet && currentMethod != http.MethodHead) {
			currentMethod = http.MethodGet
			currentForm = nil
		}
	}
	return 0, currentURL, nil, fmt.Errorf("xAI OAuth 重定向次数过多")
}

func parseSSOConsentForm(consentURL string, body []byte, userCode string) (string, url.Values, error) {
	document, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return "", nil, fmt.Errorf("解析 xAI Device Flow 授权页: %w", err)
	}
	base, err := url.Parse(consentURL)
	if err != nil || !safeXAIURL(consentURL) {
		return "", nil, fmt.Errorf("xAI Device Flow 授权页 URL 不安全")
	}
	wanted, _ := url.Parse(ssoApproveURL)
	var visit func(*html.Node) (string, url.Values, bool)
	visit = func(node *html.Node) (string, url.Values, bool) {
		if node.Type == html.ElementNode && node.Data == "form" {
			method := strings.ToUpper(strings.TrimSpace(htmlAttribute(node, "method")))
			if method == "" {
				method = http.MethodGet
			}
			action := strings.TrimSpace(htmlAttribute(node, "action"))
			parsedAction, parseErr := url.Parse(action)
			if parseErr == nil {
				resolved := base.ResolveReference(parsedAction)
				if method == http.MethodPost && safeXAIURL(resolved.String()) && sameOAuthEndpoint(resolved, wanted) {
					values := url.Values{}
					collectSSOConsentInputs(node, values)
					if strings.TrimSpace(values.Get("consent_token")) != "" {
						values.Set("user_code", userCode)
						values.Set("action", "allow")
						if values.Get("principal_type") == "" {
							values.Set("principal_type", "User")
						}
						return resolved.String(), values, true
					}
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if action, values, ok := visit(child); ok {
				return action, values, true
			}
		}
		return "", nil, false
	}
	action, values, found := visit(document)
	if !found {
		return "", nil, fmt.Errorf("xAI Device Flow 授权页缺少有效 consent_token")
	}
	return action, values, nil
}

func collectSSOConsentInputs(node *html.Node, values url.Values) {
	if node.Type == html.ElementNode && node.Data == "input" {
		name := strings.TrimSpace(htmlAttribute(node, "name"))
		switch name {
		case "consent_token", "principal_id", "principal_type", "user_code", "action":
			values.Set(name, htmlAttribute(node, "value"))
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		collectSSOConsentInputs(child, values)
	}
}

func htmlAttribute(node *html.Node, name string) string {
	for _, attribute := range node.Attr {
		if strings.EqualFold(attribute.Key, name) {
			return attribute.Val
		}
	}
	return ""
}

func sameOAuthEndpoint(actual, wanted *url.URL) bool {
	return actual != nil && wanted != nil && strings.EqualFold(actual.Scheme, wanted.Scheme) &&
		strings.EqualFold(actual.Host, wanted.Host) && actual.EscapedPath() == wanted.EscapedPath() && actual.RawQuery == ""
}

func xaiURLOrigin(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || !safeXAIURL(raw) {
		return "", fmt.Errorf("xAI OAuth 来源 URL 不安全")
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

func (f *ssoBuildFlow) captureCookies(response *http.Response) {
	for _, cookie := range response.Cookies() {
		name := strings.TrimSpace(cookie.Name)
		value := strings.TrimSpace(cookie.Value)
		if name == "" || len(name) > 128 || len(value) > 16384 || strings.ContainsAny(name+value, "\r\n\x00") {
			continue
		}
		if cookie.MaxAge < 0 {
			delete(f.cookies, name)
			continue
		}
		f.cookies[name] = value
	}
}

func ssoDeviceRedirectState(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || !safeXAIURL(raw) {
		return ""
	}
	path := strings.TrimRight(parsed.EscapedPath(), "/")
	switch path {
	case "/oauth2/device/consent":
		return "consent"
	case "/oauth2/device/done":
		return "done"
	case "/sign-in", "/sign-up":
		return "sign-in"
	default:
		return ""
	}
}

func (f *ssoBuildFlow) cookieHeader() string {
	keys := make([]string, 0, len(f.cookies))
	for key := range f.cookies {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+f.cookies[key])
	}
	return strings.Join(parts, "; ")
}

func safeXAIURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Hostname() == "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "x.ai" || strings.HasSuffix(host, ".x.ai")
}

func normalizeSSOToken(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(strings.ToLower(value), "sso=") {
		value = strings.TrimSpace(value[len("sso="):])
	}
	if token, _, found := strings.Cut(value, ";"); found {
		value = strings.TrimSpace(token)
	}
	return strings.NewReplacer("\r", "", "\n", "", "\x00", "").Replace(value)
}

func decodeBuildClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims map[string]any
	if json.Unmarshal(data, &claims) != nil {
		return nil
	}
	return claims
}

func claimString(claims map[string]any, key string) string {
	value, _ := claims[key].(string)
	return strings.TrimSpace(value)
}

func firstValue(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

type conversionHTTPError struct{ status int }

func (e conversionHTTPError) Error() string { return fmt.Sprintf("xAI OAuth HTTP %d", e.status) }

func conversionStatus(err error) int {
	var statusErr conversionHTTPError
	if errors.As(err, &statusErr) {
		return statusErr.status
	}
	if errors.Is(err, provider.ErrUnauthorized) {
		return http.StatusUnauthorized
	}
	return 0
}

var _ provider.BuildCredentialConverter = (*Adapter)(nil)
var _ ssoBuildHTTPClient = (*infraegress.Lease)(nil)
