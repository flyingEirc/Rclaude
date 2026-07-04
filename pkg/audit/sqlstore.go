package audit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	// Register the database/sql drivers for the supported audit backends.
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

const (
	// DefaultTable is the audit table name used when none is configured.
	DefaultTable = "file_audit_log"

	openTimeout = 5 * time.Second
)

var (
	// ErrUnsupportedDriver indicates an unknown SQLOptions.Driver value.
	ErrUnsupportedDriver = errors.New("audit: driver must be one of sqlite/mysql/postgres")
	// ErrInvalidTable indicates a table name with characters outside [A-Za-z0-9_].
	ErrInvalidTable = errors.New("audit: table name may only contain letters, digits and underscores")

	tableNamePattern = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
)

// SQLOptions configures OpenSQL.
type SQLOptions struct {
	// Driver selects the backend: sqlite/sqlite3, mysql, or
	// postgres/postgresql/pgsql.
	Driver string
	// DSN is the driver-specific data source name, e.g. a file path for
	// sqlite, "user:pass@tcp(host:3306)/db" for mysql, or a
	// "postgres://..." URL for postgres.
	DSN string
	// Table is the audit table name; DefaultTable when empty. The table is
	// created on open if it does not exist.
	Table string
}

type sqlDialect struct {
	driverName string
	createDDL  string
	insertStmt string
}

var sqlDialects = map[string]sqlDialect{
	"sqlite": {
		driverName: "sqlite",
		createDDL: `CREATE TABLE IF NOT EXISTS %s (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			created_at_ms BIGINT NOT NULL,
			request_id TEXT NOT NULL,
			operation TEXT NOT NULL,
			path TEXT NOT NULL,
			target TEXT NOT NULL,
			bytes BIGINT NOT NULL,
			success SMALLINT NOT NULL,
			error TEXT NOT NULL
		)`,
		insertStmt: "INSERT INTO %s (created_at_ms, request_id, operation, path, target, bytes, success, error) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
	},
	"mysql": {
		driverName: "mysql",
		createDDL: `CREATE TABLE IF NOT EXISTS %s (
			id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
			created_at_ms BIGINT NOT NULL,
			request_id VARCHAR(128) NOT NULL,
			operation VARCHAR(32) NOT NULL,
			path TEXT NOT NULL,
			target TEXT NOT NULL,
			bytes BIGINT NOT NULL,
			success SMALLINT NOT NULL,
			error TEXT NOT NULL
		)`,
		insertStmt: "INSERT INTO %s (created_at_ms, request_id, operation, path, target, bytes, success, error) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
	},
	"postgres": {
		driverName: "pgx",
		createDDL: `CREATE TABLE IF NOT EXISTS %s (
			id BIGSERIAL PRIMARY KEY,
			created_at_ms BIGINT NOT NULL,
			request_id TEXT NOT NULL,
			operation TEXT NOT NULL,
			path TEXT NOT NULL,
			target TEXT NOT NULL,
			bytes BIGINT NOT NULL,
			success SMALLINT NOT NULL,
			error TEXT NOT NULL
		)`,
		insertStmt: "INSERT INTO %s (created_at_ms, request_id, operation, path, target, bytes, success, error) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)",
	},
}

// SQLStore persists audit records through database/sql.
type SQLStore struct {
	db         *sql.DB
	insertStmt string
}

// OpenSQL opens the configured database, verifies connectivity, and ensures
// the audit table exists.
func OpenSQL(ctx context.Context, opts SQLOptions) (*SQLStore, error) {
	dialect, err := dialectFor(opts.Driver)
	if err != nil {
		return nil, err
	}
	table, err := tableOrDefault(opts.Table)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open(dialect.driverName, opts.DSN)
	if err != nil {
		return nil, fmt.Errorf("audit: open %s database: %w", opts.Driver, err)
	}
	if err := initSchema(ctx, db, dialect, table); err != nil {
		return nil, closeOnError(db, err)
	}

	// Table name is validated against tableNamePattern above, so the
	// statement cannot be influenced beyond choosing an identifier.
	//nolint:gosec // see comment above
	insertStmt := fmt.Sprintf(dialect.insertStmt, table)
	return &SQLStore{db: db, insertStmt: insertStmt}, nil
}

// Save implements Store.
func (s *SQLStore) Save(ctx context.Context, rec *Record) error {
	if rec == nil {
		return nil
	}
	success := 0
	if rec.Success {
		success = 1
	}
	_, err := s.db.ExecContext(ctx, s.insertStmt,
		rec.Time.UnixMilli(), rec.RequestID, rec.Operation,
		rec.Path, rec.Target, rec.Bytes, success, rec.Error)
	if err != nil {
		return fmt.Errorf("audit: insert record: %w", err)
	}
	return nil
}

// Close implements Store.
func (s *SQLStore) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("audit: close database: %w", err)
	}
	return nil
}

func dialectFor(driver string) (sqlDialect, error) {
	switch strings.ToLower(strings.TrimSpace(driver)) {
	case "sqlite", "sqlite3":
		return sqlDialects["sqlite"], nil
	case "mysql":
		return sqlDialects["mysql"], nil
	case "postgres", "postgresql", "pgsql":
		return sqlDialects["postgres"], nil
	default:
		return sqlDialect{}, fmt.Errorf("%w: %q", ErrUnsupportedDriver, driver)
	}
}

func tableOrDefault(table string) (string, error) {
	if table == "" {
		return DefaultTable, nil
	}
	if !tableNamePattern.MatchString(table) {
		return "", fmt.Errorf("%w: %q", ErrInvalidTable, table)
	}
	return table, nil
}

func initSchema(ctx context.Context, db *sql.DB, dialect sqlDialect, table string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, openTimeout)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("audit: ping database: %w", err)
	}
	// Table name is validated against tableNamePattern before this point.
	//nolint:gosec // see comment above
	ddl := fmt.Sprintf(dialect.createDDL, table)
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("audit: create table %s: %w", table, err)
	}
	return nil
}

func closeOnError(db *sql.DB, err error) error {
	if closeErr := db.Close(); closeErr != nil {
		return errors.Join(err, closeErr)
	}
	return err
}
