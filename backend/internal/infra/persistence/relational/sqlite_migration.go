package relational

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	egressdomain "github.com/chenyme/grok2api/backend/internal/domain/egress"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

const (
	sqlitePostgresMigrationVersion = "sqlite_to_postgres_v1"
	sqliteMigrationBatchSize       = 200
)

// MigrateSQLiteToPostgres initializes an empty PostgreSQL database and copies
// the current SQLite database into it. A completion marker makes this safe to
// call before every application start while the migration flag remains set.
func MigrateSQLiteToPostgres(ctx context.Context, sqlitePath, postgresDSN string, maxOpenConns, maxIdleConns int) error {
	database, err := OpenPostgres(ctx, postgresDSN, maxOpenConns, maxIdleConns)
	if err != nil {
		return err
	}
	defer database.Close()
	if err := database.InitializeSchema(ctx); err != nil {
		return fmt.Errorf("initialize PostgreSQL schema for SQLite migration: %w", err)
	}
	return database.ImportSQLite(ctx, sqlitePath)
}

// ImportSQLite copies an application SQLite database into an initialized,
// empty PostgreSQL database in one PostgreSQL transaction.
func (d *Database) ImportSQLite(ctx context.Context, sqlitePath string) error {
	if d == nil || d.dialect != "postgres" {
		return errors.New("SQLite → PostgreSQL 迁移目标必须是 PostgreSQL")
	}
	absolutePath, err := filepath.Abs(filepath.Clean(strings.TrimSpace(sqlitePath)))
	if err != nil {
		return fmt.Errorf("resolve SQLite source path: %w", err)
	}

	postgresTx := d.db.WithContext(ctx).Begin()
	if postgresTx.Error != nil {
		return fmt.Errorf("begin PostgreSQL migration transaction: %w", postgresTx.Error)
	}
	defer postgresTx.Rollback()
	if err := postgresTx.Exec("SELECT pg_advisory_xact_lock(?)", postgresSchemaMigrationLockID).Error; err != nil {
		return fmt.Errorf("acquire PostgreSQL migration lock: %w", err)
	}
	if err := lockSQLiteMigrationTables(postgresTx); err != nil {
		return fmt.Errorf("lock PostgreSQL tables for SQLite migration: %w", err)
	}

	var completed sqlitePostgresMigrationModel
	err = postgresTx.First(&completed, "version = ?", sqlitePostgresMigrationVersion).Error
	if err == nil {
		if completed.SourcePath != absolutePath {
			return fmt.Errorf("PostgreSQL database already contains a SQLite migration from a different source path; disable %s before restarting", "GROK2API_MIGRATE_SQLITE_TO_POSTGRES")
		}
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("inspect SQLite migration status: %w", err)
	}

	if err := ensurePostgresMigrationTargetEmpty(postgresTx); err != nil {
		return err
	}
	info, err := os.Stat(absolutePath)
	if err != nil {
		return fmt.Errorf("read SQLite source database %q: %w", absolutePath, err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("SQLite source %q must be a non-empty regular database file", absolutePath)
	}

	source, err := OpenSQLite(ctx, absolutePath)
	if err != nil {
		return fmt.Errorf("open SQLite source: %w", err)
	}
	defer source.Close()
	if !source.db.Migrator().HasTable(&adminModel{}) && !source.db.Migrator().HasTable(&accountModel{}) {
		return fmt.Errorf("SQLite source %q does not contain a recognized Grok2API database", absolutePath)
	}
	if err := source.InitializeSchema(ctx); err != nil {
		return fmt.Errorf("upgrade SQLite source schema before import: %w", err)
	}

	sourceTx := source.db.WithContext(ctx).Begin(&sql.TxOptions{ReadOnly: true})
	if sourceTx.Error != nil {
		return fmt.Errorf("begin SQLite source snapshot: %w", sourceTx.Error)
	}
	defer sourceTx.Rollback()

	expectedCounts := make(map[string]int64, len(schemaModels)-1)
	for _, model := range schemaModels {
		table := modelTableName(model)
		if table == (sqlitePostgresMigrationModel{}).TableName() {
			continue
		}
		var count int64
		if err := sourceTx.Table(table).Count(&count).Error; err != nil {
			return fmt.Errorf("count SQLite table %s: %w", table, err)
		}
		expectedCounts[table] = count
	}

	// Schema initialization inserts this one default singleton on both dialects.
	// It is the only row a fresh target may contain before source data is copied.
	if err := postgresTx.Delete(&egressOperationsConfigModel{}, "id = ?", 1).Error; err != nil {
		return fmt.Errorf("replace PostgreSQL default egress config with SQLite data: %w", err)
	}

	for _, model := range schemaModels {
		table := modelTableName(model)
		if table == (sqlitePostgresMigrationModel{}).TableName() {
			continue
		}
		if expectedCounts[table] == 0 {
			continue
		}
		if err := copySQLiteTable(ctx, sourceTx, postgresTx, model, table); err != nil {
			return fmt.Errorf("copy SQLite table %s: %w", table, err)
		}
	}

	if err := postgresTx.Exec("SET CONSTRAINTS ALL IMMEDIATE").Error; err != nil {
		return fmt.Errorf("validate imported PostgreSQL foreign keys: %w", err)
	}
	for table, expected := range expectedCounts {
		var actual int64
		if err := postgresTx.Table(table).Count(&actual).Error; err != nil {
			return fmt.Errorf("count imported PostgreSQL table %s: %w", table, err)
		}
		if actual != expected {
			return fmt.Errorf("SQLite migration row count mismatch for %s: source=%d PostgreSQL=%d", table, expected, actual)
		}
	}
	if err := resetPostgresSequences(postgresTx, sourceTx, schemaModels); err != nil {
		return fmt.Errorf("synchronize PostgreSQL sequences: %w", err)
	}
	if err := postgresTx.Create(&sqlitePostgresMigrationModel{
		Version: sqlitePostgresMigrationVersion, SourcePath: absolutePath, CompletedAt: time.Now().UTC(),
	}).Error; err != nil {
		return fmt.Errorf("record SQLite migration completion: %w", err)
	}
	if err := postgresTx.Commit().Error; err != nil {
		return fmt.Errorf("commit SQLite → PostgreSQL migration: %w", err)
	}
	return nil
}

func lockSQLiteMigrationTables(tx *gorm.DB) error {
	tables := make([]string, 0, len(schemaModels))
	for _, model := range schemaModels {
		tables = append(tables, quotePostgresIdentifier(modelTableName(model)))
	}
	statement := "LOCK TABLE " + strings.Join(tables, ", ") + " IN ACCESS EXCLUSIVE MODE"
	return tx.Exec(statement).Error
}

func ensurePostgresMigrationTargetEmpty(tx *gorm.DB) error {
	for _, model := range schemaModels {
		table := modelTableName(model)
		if table == (sqlitePostgresMigrationModel{}).TableName() {
			continue
		}
		var count int64
		if err := tx.Table(table).Count(&count).Error; err != nil {
			return fmt.Errorf("inspect PostgreSQL table %s: %w", table, err)
		}
		if count == 0 {
			continue
		}
		if table == (egressOperationsConfigModel{}).TableName() && count == 1 {
			var current egressOperationsConfigModel
			if err := tx.First(&current, "id = ?", 1).Error; err != nil {
				return fmt.Errorf("inspect default PostgreSQL egress config: %w", err)
			}
			expected := fromEgressOperationsConfigDomain(egressdomain.DefaultOperationsConfig())
			expected.ProxyProfileMigrationCompleted = true
			expected.UpdatedAt = current.UpdatedAt
			if current == expected {
				continue
			}
		}
		return fmt.Errorf("PostgreSQL target is not empty: table %s contains %d row(s); use a fresh database rather than merging data", table, count)
	}
	return nil
}

func copySQLiteTable(ctx context.Context, source, destination *gorm.DB, model any, table string) error {
	modelType := reflect.TypeOf(model)
	if modelType.Kind() != reflect.Ptr {
		return fmt.Errorf("model %T is not a pointer", model)
	}
	parsed, err := schema.Parse(model, &sync.Map{}, source.NamingStrategy)
	if err != nil {
		return err
	}
	if len(parsed.PrimaryFields) == 0 {
		return fmt.Errorf("table %s has no primary key", table)
	}
	order := make([]clause.OrderByColumn, 0, len(parsed.PrimaryFields))
	for _, field := range parsed.PrimaryFields {
		order = append(order, clause.OrderByColumn{Column: clause.Column{Table: table, Name: field.DBName}})
	}
	var lastValues []any
	for {
		rows := reflect.New(reflect.SliceOf(modelType.Elem()))
		query := source.Model(model).Order(clause.OrderBy{Columns: order}).Limit(sqliteMigrationBatchSize)
		if len(lastValues) > 0 {
			alternatives := make([]clause.Expression, 0, len(parsed.PrimaryFields))
			for greaterIndex, field := range parsed.PrimaryFields {
				terms := make([]clause.Expression, 0, greaterIndex+1)
				for equalIndex := 0; equalIndex < greaterIndex; equalIndex++ {
					terms = append(terms, clause.Eq{
						Column: clause.Column{Table: table, Name: parsed.PrimaryFields[equalIndex].DBName},
						Value:  lastValues[equalIndex],
					})
				}
				terms = append(terms, clause.Gt{Column: clause.Column{Table: table, Name: field.DBName}, Value: lastValues[greaterIndex]})
				alternatives = append(alternatives, clause.And(terms...))
			}
			query = query.Where(clause.Or(alternatives...))
		}
		if err := query.Find(rows.Interface()).Error; err != nil {
			return fmt.Errorf("read SQLite batch: %w", err)
		}
		batchValue := rows.Elem()
		if batchValue.Len() == 0 {
			return nil
		}
		if err := destination.Model(model).Omit(clause.Associations).Select("*").CreateInBatches(batchValue.Interface(), sqliteMigrationBatchSize).Error; err != nil {
			return err
		}
		last := batchValue.Index(batchValue.Len() - 1)
		lastValues = make([]any, 0, len(parsed.PrimaryFields))
		for _, field := range parsed.PrimaryFields {
			value, _ := field.ValueOf(ctx, last)
			lastValues = append(lastValues, value)
		}
		if batchValue.Len() < sqliteMigrationBatchSize {
			return nil
		}
	}
}

func resetPostgresSequences(destination, source *gorm.DB, models []any) error {
	cache := &sync.Map{}
	for _, model := range models {
		if modelTableName(model) == (sqlitePostgresMigrationModel{}).TableName() {
			continue
		}
		parsed, err := schema.Parse(model, cache, destination.NamingStrategy)
		if err != nil {
			return err
		}
		for _, field := range parsed.Fields {
			if !field.AutoIncrement {
				continue
			}
			table, column := parsed.Table, field.DBName
			quotedTable, quotedColumn := quotePostgresIdentifier(table), quotePostgresIdentifier(column)
			var sequence sql.NullString
			if err := destination.Raw("SELECT pg_get_serial_sequence(?, ?)", table, column).Scan(&sequence).Error; err != nil {
				return err
			}
			if !sequence.Valid || sequence.String == "" {
				continue
			}
			var maximum int64
			if err := destination.Raw("SELECT COALESCE(MAX(" + quotedColumn + "), 0) FROM " + quotedTable).Scan(&maximum).Error; err != nil {
				return err
			}
			var sqliteSequence sql.NullInt64
			if source.Migrator().HasTable("sqlite_sequence") {
				if err := source.Raw("SELECT seq FROM sqlite_sequence WHERE name = ?", table).Scan(&sqliteSequence).Error; err != nil {
					return err
				}
			}
			if sqliteSequence.Valid && sqliteSequence.Int64 > maximum {
				maximum = sqliteSequence.Int64
			}
			if maximum == 0 {
				maximum = 1
				if err := destination.Exec("SELECT setval(?::regclass, ?, false)", sequence.String, maximum).Error; err != nil {
					return err
				}
				continue
			}
			if err := destination.Exec("SELECT setval(?::regclass, ?, true)", sequence.String, maximum).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func modelTableName(model any) string {
	return model.(interface{ TableName() string }).TableName()
}

func quotePostgresIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
