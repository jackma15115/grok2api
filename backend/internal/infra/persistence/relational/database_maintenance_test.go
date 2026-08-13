package relational

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestMaintainSQLiteCompactsAndCheckpointsWAL(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "maintenance.db")
	database, err := OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	if err := database.db.Exec(`CREATE TABLE payloads (id INTEGER PRIMARY KEY, payload BLOB)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.db.Exec(`
		WITH RECURSIVE sequence(value) AS (
			SELECT 1
			UNION ALL
			SELECT value + 1 FROM sequence WHERE value < 512
		)
		INSERT INTO payloads(payload) SELECT randomblob(4096) FROM sequence
	`).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.MaintainSQLite(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := database.db.Exec(`DELETE FROM payloads`).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.MaintainSQLite(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() >= before.Size() {
		t.Fatalf("SQLite database was not compacted: before=%d after=%d", before.Size(), after.Size())
	}

	wal, err := os.Stat(path + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	if wal.Size() != 0 {
		t.Fatalf("SQLite WAL was not truncated: size=%d", wal.Size())
	}
}

func TestMaintainSQLiteIsNoOpForOtherDialects(t *testing.T) {
	database := &Database{dialect: "postgres"}
	if err := database.MaintainSQLite(context.Background()); err != nil {
		t.Fatal(err)
	}
}
