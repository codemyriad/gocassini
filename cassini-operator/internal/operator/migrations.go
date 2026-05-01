package operator

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const schemaMigrationsTable = "schema_migrations"

//go:embed migrations/*.sql
var migrationFiles embed.FS

type migration struct {
	Version int
	Name    string
	UpSQL   string
	DownSQL string
}

func loadMigrations() ([]migration, error) {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	versions := map[int]*migration{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".sql") {
			return nil, fmt.Errorf("invalid migration filename %q", name)
		}
		base := strings.TrimSuffix(name, ".sql")
		dot := strings.LastIndex(base, ".")
		underscore := strings.Index(base, "_")
		if dot <= 0 || underscore <= 0 || underscore >= dot {
			return nil, fmt.Errorf("invalid migration filename %q", name)
		}
		direction := base[dot+1:]
		if direction != "up" && direction != "down" {
			return nil, fmt.Errorf("invalid migration filename %q", name)
		}
		version, err := strconv.Atoi(base[:underscore])
		if err != nil {
			return nil, fmt.Errorf("parse migration version %q: %w", name, err)
		}
		migrationName := base[underscore+1 : dot]
		m := versions[version]
		if m == nil {
			m = &migration{Version: version, Name: migrationName}
			versions[version] = m
		}
		if m.Name != migrationName {
			return nil, fmt.Errorf("migration %04d uses multiple names: %q and %q", version, m.Name, migrationName)
		}
		body, err := migrationFiles.ReadFile(filepath.Join("migrations", name))
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", name, err)
		}
		switch direction {
		case "up":
			if m.UpSQL != "" {
				return nil, fmt.Errorf("duplicate up migration for version %04d", version)
			}
			m.UpSQL = string(body)
		case "down":
			if m.DownSQL != "" {
				return nil, fmt.Errorf("duplicate down migration for version %04d", version)
			}
			m.DownSQL = string(body)
		}
	}
	if len(versions) == 0 {
		return nil, errors.New("no migrations found")
	}
	ordered := make([]migration, 0, len(versions))
	for _, m := range versions {
		if strings.TrimSpace(m.UpSQL) == "" || strings.TrimSpace(m.DownSQL) == "" {
			return nil, fmt.Errorf("migration %04d is missing up or down SQL", m.Version)
		}
		ordered = append(ordered, *m)
	}
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].Version < ordered[j].Version
	})
	for i, m := range ordered {
		want := ordered[0].Version + i
		if m.Version != want {
			return nil, fmt.Errorf("migration versions must be contiguous: got %04d want %04d", m.Version, want)
		}
	}
	return ordered, nil
}

func (s *Store) ensureSchema() error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	return s.migrateUp(migrations)
}

func (s *Store) migrateDownTo(targetVersion int) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	if targetVersion < 0 {
		return fmt.Errorf("target version must be >= 0")
	}
	return s.migrateDown(migrations, targetVersion)
}

func (s *Store) migrateUp(migrations []migration) error {
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("begin migrations: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := baselineLegacySchemaIfNeeded(tx, migrations[0]); err != nil {
		return err
	}
	if err := ensureSchemaMigrationsTable(tx); err != nil {
		return err
	}
	applied, err := appliedMigrationVersions(tx)
	if err != nil {
		return err
	}
	if err := validateAppliedMigrationHistory(migrations, applied); err != nil {
		return err
	}
	appliedSet := make(map[int]struct{}, len(applied))
	for _, version := range applied {
		appliedSet[version] = struct{}{}
	}
	for _, m := range migrations {
		if _, ok := appliedSet[m.Version]; ok {
			continue
		}
		if _, err := tx.Exec(m.UpSQL); err != nil {
			return fmt.Errorf("apply migration %04d up: %w", m.Version, err)
		}
		if _, err := tx.Exec(`
INSERT INTO schema_migrations (version, name, applied_at)
VALUES (?, ?, ?)`, m.Version, m.Name, nowUTCString()); err != nil {
			return fmt.Errorf("record migration %04d: %w", m.Version, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}

func (s *Store) migrateDown(migrations []migration, targetVersion int) error {
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("begin down migrations: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := ensureSchemaMigrationsTable(tx); err != nil {
		return err
	}
	applied, err := appliedMigrationVersions(tx)
	if err != nil {
		return err
	}
	if err := validateAppliedMigrationHistory(migrations, applied); err != nil {
		return err
	}
	currentVersion := 0
	if len(applied) > 0 {
		currentVersion = applied[len(applied)-1]
	}
	if targetVersion > currentVersion {
		return fmt.Errorf("target version %d is above current version %d", targetVersion, currentVersion)
	}
	appliedSet := make(map[int]struct{}, len(applied))
	for _, version := range applied {
		appliedSet[version] = struct{}{}
	}
	for i := len(migrations) - 1; i >= 0; i-- {
		m := migrations[i]
		if m.Version <= targetVersion {
			continue
		}
		if _, ok := appliedSet[m.Version]; !ok {
			continue
		}
		if _, err := tx.Exec(m.DownSQL); err != nil {
			return fmt.Errorf("apply migration %04d down: %w", m.Version, err)
		}
		if _, err := tx.Exec(`DELETE FROM schema_migrations WHERE version = ?`, m.Version); err != nil {
			return fmt.Errorf("delete migration %04d record: %w", m.Version, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit down migrations: %w", err)
	}
	return nil
}

func baselineLegacySchemaIfNeeded(tx *sql.Tx, baseline migration) error {
	hasMigrations, err := tableExists(tx, schemaMigrationsTable)
	if err != nil {
		return err
	}
	if hasMigrations {
		return nil
	}
	hasJobs, err := tableExists(tx, "jobs")
	if err != nil {
		return err
	}
	if !hasJobs {
		return nil
	}
	if err := ensureSchemaMigrationsTable(tx); err != nil {
		return err
	}
	if _, err := tx.Exec(`
INSERT INTO schema_migrations (version, name, applied_at)
VALUES (?, ?, ?)`, baseline.Version, baseline.Name, nowUTCString()); err != nil {
		return fmt.Errorf("baseline legacy schema: %w", err)
	}
	return nil
}

func ensureSchemaMigrationsTable(tx *sql.Tx) error {
	_, err := tx.Exec(`
CREATE TABLE IF NOT EXISTS schema_migrations (
  version INTEGER PRIMARY KEY NOT NULL,
  name TEXT NOT NULL,
  applied_at TEXT NOT NULL
);
`)
	if err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}
	return nil
}

func appliedMigrationVersions(tx *sql.Tx) ([]int, error) {
	rows, err := tx.Query(`SELECT version FROM schema_migrations ORDER BY version ASC`)
	if err != nil {
		return nil, fmt.Errorf("query schema_migrations: %w", err)
	}
	defer rows.Close()

	var versions []int
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("scan schema_migrations: %w", err)
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate schema_migrations: %w", err)
	}
	return versions, nil
}

func validateAppliedMigrationHistory(migrations []migration, applied []int) error {
	known := make(map[int]struct{}, len(migrations))
	for _, m := range migrations {
		known[m.Version] = struct{}{}
	}
	for _, version := range applied {
		if _, ok := known[version]; !ok {
			return fmt.Errorf("unknown applied migration version %04d", version)
		}
	}
	gapSeen := false
	appliedSet := make(map[int]struct{}, len(applied))
	for _, version := range applied {
		appliedSet[version] = struct{}{}
	}
	for _, m := range migrations {
		_, ok := appliedSet[m.Version]
		if !ok {
			gapSeen = true
			continue
		}
		if gapSeen {
			return fmt.Errorf("migration history has a gap before applied version %04d", m.Version)
		}
	}
	return nil
}

func tableExists(queryer interface{ QueryRow(string, ...any) *sql.Row }, name string) (bool, error) {
	var found string
	err := queryer.QueryRow(`
SELECT name
FROM sqlite_master
WHERE type = 'table' AND name = ?
LIMIT 1`, name).Scan(&found)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return false, fmt.Errorf("query sqlite_master for table %s: %w", name, err)
}
