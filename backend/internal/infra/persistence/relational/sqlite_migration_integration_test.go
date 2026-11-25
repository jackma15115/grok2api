package relational

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/domain/admin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPostgresSQLiteImportPreservesDataAndSkipsCompletedRestart(t *testing.T) {
	ctx := context.Background()
	dsn := newSQLiteMigrationTestDatabase(t)
	sourcePath := createSQLiteMigrationFixture(t, ctx, true)

	if err := MigrateSQLiteToPostgres(ctx, sourcePath, dsn, 10, 5); err != nil {
		t.Fatal(err)
	}
	database, err := OpenPostgres(ctx, dsn, 10, 5)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	var credential accountCredentialModel
	if err := database.db.WithContext(ctx).First(&credential, "account_id = ?", uint64(123)).Error; err != nil {
		t.Fatal(err)
	}
	if credential.EncryptedPrimary != "encrypted-primary-fixture" || credential.EncryptedRefresh != "encrypted-refresh-fixture" {
		t.Fatalf("encrypted credential changed during import: %#v", credential)
	}
	var accountRow accountModel
	if err := database.db.WithContext(ctx).First(&accountRow, uint64(123)).Error; err != nil {
		t.Fatal(err)
	}
	var quotaRows int64
	if err := database.db.WithContext(ctx).Model(&quotaWindowModel{}).Where("account_id = ?", uint64(123)).Count(&quotaRows).Error; err != nil {
		t.Fatal(err)
	}
	if quotaRows != sqliteMigrationBatchSize+17 {
		t.Fatalf("composite-key rows after import = %d, want %d", quotaRows, sqliteMigrationBatchSize+17)
	}
	if accountRow.Name != "migration-fixture" || accountRow.Provider != string(account.ProviderBuild) {
		t.Fatalf("account after import = %#v", accountRow)
	}
	var adminRows []adminModel
	if err := database.db.WithContext(ctx).Order("id").Find(&adminRows).Error; err != nil {
		t.Fatal(err)
	}
	if len(adminRows) != 1 || adminRows[0].ID != 81 {
		t.Fatalf("admins after import = %#v", adminRows)
	}
	createdAdmin, err := NewAdminRepository(database).Create(ctx, admin.Admin{Username: "after-import", PasswordHash: "hash"})
	if err != nil {
		t.Fatal(err)
	}
	if createdAdmin.ID <= 81 {
		t.Fatalf("PostgreSQL sequence ID = %d, want > 81", createdAdmin.ID)
	}

	if err := MigrateSQLiteToPostgres(ctx, sourcePath, dsn, 10, 5); err != nil {
		t.Fatalf("repeat migration after restart: %v", err)
	}
	if err := NewAccountRepository(database).UpdateObservedModel(ctx, 123, "after-migration", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := database.db.WithContext(ctx).Model(&adminModel{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("admin count after repeat migration = %d, want 2", count)
	}
}

func TestPostgresSQLiteImportRejectsPopulatedTarget(t *testing.T) {
	ctx := context.Background()
	dsn := newSQLiteMigrationTestDatabase(t)
	sourcePath := createSQLiteMigrationFixture(t, ctx, false)
	target, err := OpenPostgres(ctx, dsn, 10, 5)
	if err != nil {
		t.Fatal(err)
	}
	if err := target.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := NewAdminRepository(target).Create(ctx, admin.Admin{Username: "already-there", PasswordHash: "hash"}); err != nil {
		t.Fatal(err)
	}
	err = target.ImportSQLite(ctx, sourcePath)
	if err == nil || !strings.Contains(err.Error(), "PostgreSQL target is not empty") {
		t.Fatalf("ImportSQLite error = %v", err)
	}
	var count int64
	if err := target.db.Model(&adminModel{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("target admin count after refusal = %d, want 1", count)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresSQLiteImportRollsBackOnInvalidSourceRow(t *testing.T) {
	ctx := context.Background()
	dsn := newSQLiteMigrationTestDatabase(t)
	sourcePath := createSQLiteMigrationFixture(t, ctx, false)
	source, err := OpenSQLite(ctx, sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if sqlDB, err := source.db.DB(); err != nil {
		t.Fatal(err)
	} else {
		sqlDB.SetMaxOpenConns(1)
		sqlDB.SetMaxIdleConns(1)
	}
	if err := source.db.Exec("PRAGMA ignore_check_constraints = ON").Error; err != nil {
		t.Fatal(err)
	}
	if err := source.db.Model(&accountModel{}).Where("id = ?", uint64(123)).Update("provider", "not-a-provider").Error; err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	err = MigrateSQLiteToPostgres(ctx, sourcePath, dsn, 10, 5)
	if err == nil || !strings.Contains(err.Error(), "copy SQLite table provider_accounts") {
		t.Fatalf("ImportSQLite error = %v", err)
	}
	database, err := OpenPostgres(ctx, dsn, 10, 5)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var count int64
	if err := database.db.Model(&adminModel{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("admin rows after failed migration = %d, want transaction rollback", count)
	}
	if err := database.db.Model(&sqlitePostgresMigrationModel{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("migration markers after failed import = %d", count)
	}
}

func createSQLiteMigrationFixture(t *testing.T, ctx context.Context, withHighIDs bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.db")
	database, err := OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if withHighIDs {
		if err := database.db.Create(&adminModel{ID: 81, Username: "fixture-admin", PasswordHash: "hash", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.db.Create(&accountModel{
		ID: 123, IdentityKey: strings.Repeat("a", 64), Provider: string(account.ProviderBuild), Name: "migration-fixture",
		SourceKey: "migration-fixture", Enabled: true, AuthStatus: string(account.AuthStatusActive), Priority: 1, MaxConcurrent: 8,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.db.Create(&accountCredentialModel{
		AccountID: 123, AuthType: string(account.AuthTypeOAuth), EncryptedPrimary: "encrypted-primary-fixture",
		EncryptedRefresh: "encrypted-refresh-fixture", UpdatedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if withHighIDs {
		quotaRows := make([]quotaWindowModel, sqliteMigrationBatchSize+17)
		for index := range quotaRows {
			quotaRows[index] = quotaWindowModel{
				AccountID: 123, Mode: fmt.Sprintf("migration-%03d", index), Remaining: 10, Total: 10,
				BreakdownJSON: "[]", Source: "default", UpdatedAt: time.Now().UTC(),
			}
		}
		if err := database.db.Create(&quotaRows).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func newSQLiteMigrationTestDatabase(t *testing.T) string {
	t.Helper()
	adminDSN := os.Getenv(testPostgresAdminDSNEnv)
	if adminDSN == "" {
		t.Skip("TEST_POSTGRES_ADMIN_DSN is required for isolated SQLite migration integration tests")
	}
	parsed, err := url.Parse(adminDSN)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		t.Fatalf("%s must be a PostgreSQL URL", testPostgresAdminDSNEnv)
	}
	parsed.RawQuery, err = url.QueryUnescape(parsed.RawQuery)
	if err != nil {
		t.Fatal(err)
	}
	adminDSN = parsed.String()
	name := fmt.Sprintf("grok2api_sqlite_migration_%d", time.Now().UTC().UnixNano())
	adminDB, err := gorm.Open(postgres.Open(adminDSN), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	adminSQL, err := adminDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	identifier := quotePostgresTestIdentifier(name)
	if err := adminDB.Exec("CREATE DATABASE " + identifier).Error; err != nil {
		adminSQL.Close()
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	dsn := parsed.String()
	t.Cleanup(func() {
		defer adminSQL.Close()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := adminDB.WithContext(cleanupCtx).Exec("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = ? AND pid <> pg_backend_pid()", name).Error; err != nil {
			t.Errorf("terminate temporary migration database connections: %v", err)
			return
		}
		if err := adminDB.WithContext(cleanupCtx).Exec("DROP DATABASE IF EXISTS " + identifier).Error; err != nil {
			t.Errorf("drop temporary migration database: %v", err)
		}
	})
	return dsn
}

func TestSQLiteMigrationRequiresPostgresDestination(t *testing.T) {
	if err := (&Database{dialect: "sqlite"}).ImportSQLite(context.Background(), "unused"); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ImportSQLite error = %v, want target dialect error", err)
	}
}
