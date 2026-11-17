package app

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	accountdomain "github.com/chenyme/grok2api/backend/internal/domain/account"
	modeldomain "github.com/chenyme/grok2api/backend/internal/domain/model"
	"github.com/chenyme/grok2api/backend/internal/infra/config"
	"github.com/chenyme/grok2api/backend/internal/infra/persistence/relational"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
	webprovider "github.com/chenyme/grok2api/backend/internal/infra/provider/web"
)

func TestReadinessStartupReportDoesNotExposeInternalErrors(t *testing.T) {
	state := newStartupState(0)
	state.recordError(errors.New("postgres://private-host/internal"))
	_, _, report, _ := state.snapshot()
	payload, err := json.Marshal(newReadinessStartupReport(report))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "private-host") || !strings.Contains(string(payload), `"errorCount":1`) {
		t.Fatalf("public readiness leaked internal error: %s", payload)
	}
}

func TestDisabledStatsigNeedsNoAccountRepositoryOrWarmup(t *testing.T) {
	state := newStartupState(0)
	state.setStatsigMode(config.StatsigModeURL)
	state.setStatsigWarmup("warming", "in flight", 0)
	state.setStatsigMode(config.StatsigModeDisabled)
	state.setStatsigWarmup("unavailable", "obsolete warmup failure", 0)
	application := &Application{
		startup: state,
		web:     webprovider.NewAdapter(webprovider.Config{StatsigMode: config.StatsigModeDisabled}, nil, nil, nil, nil),
	}
	application.warmStatsig(context.Background())
	_, _, _, component := state.snapshot()
	if component.State != "disabled" {
		t.Fatalf("statsig = %#v", component)
	}
}

func TestReadinessKeepsWebReadyWhenStatsigIsDisabled(t *testing.T) {
	ctx := context.Background()
	database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "unsigned-web.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	accounts := relational.NewAccountRepository(database)
	models := relational.NewModelRepository(database)
	credential, _, err := accounts.UpsertByIdentity(ctx, accountdomain.Credential{
		Provider: accountdomain.ProviderWeb, AuthType: accountdomain.AuthTypeSSO,
		Name: "unsigned-web", SourceKey: "unsigned-web", EncryptedAccessToken: "test-sso",
		Enabled: true, AuthStatus: accountdomain.AuthStatusActive, MaxConcurrent: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := models.UpsertRoutes(ctx, []modeldomain.Route{{
		PublicID: "grok-chat-fast", Provider: accountdomain.ProviderWeb, UpstreamModel: "grok-chat-fast",
		Capability: modeldomain.CapabilityChat, Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := models.ReplaceAccountCapabilities(ctx, credential.ID, []string{"grok-chat-fast"}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	state := newStartupState(0)
	state.setPhase("running")
	state.setStatsigMode(config.StatsigModeDisabled)
	snapshot := readinessSnapshot(ctx, state, func(context.Context) error { return nil }, models, accounts, provider.NewRegistry(), nil)
	if !snapshot.Ready || snapshot.State != "ready" || snapshot.Components["grok_web"].State != "ready" || snapshot.Components["statsig"].State != "disabled" {
		t.Fatalf("readiness = %#v", snapshot)
	}
}

func TestReadinessKeepsBuildReadyWhenWebIsUnavailable(t *testing.T) {
	ctx := context.Background()
	database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "readiness.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	accounts := relational.NewAccountRepository(database)
	models := relational.NewModelRepository(database)
	now := time.Now().UTC()
	build, _, err := accounts.UpsertByIdentity(ctx, accountdomain.Credential{
		Provider: accountdomain.ProviderBuild, Name: "build-ready", SourceKey: "build-ready",
		EncryptedAccessToken: "access", EncryptedRefreshToken: "refresh", ExpiresAt: now.Add(time.Hour),
		Enabled: true, AuthStatus: accountdomain.AuthStatusActive, MaxConcurrent: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := models.UpsertRoutes(ctx, []modeldomain.Route{
		{PublicID: "build-model", Provider: accountdomain.ProviderBuild, UpstreamModel: "build-model", Capability: modeldomain.CapabilityResponses, Enabled: true},
		{PublicID: "web-model", Provider: accountdomain.ProviderWeb, UpstreamModel: "web-model", Capability: modeldomain.CapabilityChat, Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := models.ReplaceAccountCapabilities(ctx, build.ID, []string{"build-model"}, now); err != nil {
		t.Fatal(err)
	}
	state := newStartupState(0)
	state.setPhase("running")
	state.setStatsig("unavailable", "test", 0)
	snapshot := readinessSnapshot(ctx, state, func(context.Context) error { return nil }, models, accounts, provider.NewRegistry(), nil)
	if !snapshot.Ready || snapshot.State != "degraded" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if snapshot.Components["grok_build"].State != "ready" || snapshot.Components["grok_web"].State != "unavailable" {
		t.Fatalf("components = %#v", snapshot.Components)
	}
}

func TestReadinessRejectsAccountWithoutAccessToken(t *testing.T) {
	ctx := context.Background()
	database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "readiness-missing-access.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	accounts := relational.NewAccountRepository(database)
	models := relational.NewModelRepository(database)
	now := time.Now().UTC()
	build, _, err := accounts.UpsertByIdentity(ctx, accountdomain.Credential{
		Provider: accountdomain.ProviderBuild, AuthType: accountdomain.AuthTypeOAuth,
		Name: "refresh-only", SourceKey: "refresh-only", EncryptedRefreshToken: "refresh",
		ExpiresAt: now.Add(time.Hour), Enabled: true, AuthStatus: accountdomain.AuthStatusActive, MaxConcurrent: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := models.UpsertRoutes(ctx, []modeldomain.Route{{
		PublicID: "build-model", Provider: accountdomain.ProviderBuild, UpstreamModel: "build-model", Capability: modeldomain.CapabilityResponses, Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := models.ReplaceAccountCapabilities(ctx, build.ID, []string{"build-model"}, now); err != nil {
		t.Fatal(err)
	}
	state := newStartupState(0)
	state.setPhase("running")
	snapshot := readinessSnapshot(ctx, state, func(context.Context) error { return nil }, models, accounts, provider.NewRegistry(), nil)
	if snapshot.Ready || snapshot.Components["grok_build"].State != "unavailable" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestReadinessRestoresPersistedCooldownWithoutUpstreamProbe(t *testing.T) {
	ctx := context.Background()
	database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "cooldown-readiness.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	accounts := relational.NewAccountRepository(database)
	models := relational.NewModelRepository(database)
	now := time.Now().UTC()
	cooldownUntil := now.Add(10 * time.Minute)
	build, _, err := accounts.UpsertByIdentity(ctx, accountdomain.Credential{
		Provider: accountdomain.ProviderBuild, Name: "cooling", SourceKey: "cooling",
		EncryptedAccessToken: "access", EncryptedRefreshToken: "refresh", ExpiresAt: now.Add(time.Hour),
		Enabled: true, AuthStatus: accountdomain.AuthStatusActive, MaxConcurrent: 1, CooldownUntil: &cooldownUntil,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := models.UpsertRoutes(ctx, []modeldomain.Route{{PublicID: "build-model", Provider: accountdomain.ProviderBuild, UpstreamModel: "build-model", Capability: modeldomain.CapabilityResponses, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if err := models.ReplaceAccountCapabilities(ctx, build.ID, []string{"build-model"}, now); err != nil {
		t.Fatal(err)
	}
	state := newStartupState(0)
	state.setPhase("running")
	snapshot := readinessSnapshot(ctx, state, func(context.Context) error { return nil }, models, accounts, provider.NewRegistry(), nil)
	if snapshot.Ready || snapshot.State != "not_ready" || snapshot.Components["grok_build"].State != "unavailable" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}
