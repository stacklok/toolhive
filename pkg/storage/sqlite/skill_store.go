// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	sqlite3 "modernc.org/sqlite"
	sqlite3lib "modernc.org/sqlite/lib"

	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/storage"
)

// SkillStore implements storage.SkillStore using SQLite.
type SkillStore struct {
	wrapper *DB
	db      *sql.DB
}

// NewSkillStore creates a new SQLite-backed SkillStore.
func NewSkillStore(db *DB) *SkillStore {
	return &SkillStore{wrapper: db, db: db.DB()}
}

// Close closes the underlying database connection.
func (s *SkillStore) Close() error {
	return s.wrapper.Close()
}

var _ storage.SkillStore = (*SkillStore)(nil)

// skillColumns is the SELECT column list shared by Get and List queries.
const skillColumns = `is_.id, e.name, is_.scope, is_.project_root, is_.reference, is_.tag,
			is_.digest, is_.version, is_.description, is_.author, json(is_.tags),
			json(is_.client_apps), is_.status, is_.installed_at`

// Create stores a new installed skill.
func (s *SkillStore) Create(ctx context.Context, skill skills.InstalledSkill) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer rollback(tx)

	// Upsert into entries table. A single skill name can have multiple
	// installations (user-scoped + project-scoped), so we reuse the entry
	// if it already exists.
	var entryID int64
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM entries WHERE entry_type = 'skill' AND name = ?`,
		skill.Metadata.Name,
	).Scan(&entryID)
	if errors.Is(err, sql.ErrNoRows) {
		res, insertErr := tx.ExecContext(ctx,
			`INSERT INTO entries (entry_type, name) VALUES ('skill', ?)`,
			skill.Metadata.Name,
		)
		if insertErr != nil {
			return fmt.Errorf("inserting entry: %w", insertErr)
		}
		entryID, err = res.LastInsertId()
		if err != nil {
			return fmt.Errorf("getting entry id: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("looking up entry: %w", err)
	}

	tagsJSON, err := encodeJSONB(skill.Metadata.Tags)
	if err != nil {
		return fmt.Errorf("encoding tags: %w", err)
	}
	clientsJSON, err := encodeJSONB(skill.Clients)
	if err != nil {
		return fmt.Errorf("encoding clients: %w", err)
	}

	res, err := tx.ExecContext(ctx, `
		INSERT INTO installed_skills (
			entry_id, scope, project_root, reference, tag, digest,
			version, description, author, tags, client_apps, status
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, jsonb(?), jsonb(?), ?)`,
		entryID,
		string(skill.Scope),
		skill.ProjectRoot,
		skill.Reference,
		skill.Tag,
		skill.Digest,
		skill.Metadata.Version,
		skill.Metadata.Description,
		skill.Metadata.Author,
		tagsJSON,
		clientsJSON,
		string(skill.Status),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return storage.ErrAlreadyExists
		}
		return fmt.Errorf("inserting installed skill: %w", err)
	}

	installedSkillID, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("getting installed skill id: %w", err)
	}

	// Insert dependencies.
	if err := insertDependencies(ctx, tx, installedSkillID, skill.Dependencies); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}

	return nil
}

// Get retrieves an installed skill by name, scope, and project root.
func (s *SkillStore) Get(
	ctx context.Context, name string, scope skills.Scope, projectRoot string,
) (skills.InstalledSkill, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+skillColumns+`
		FROM entries e
		JOIN installed_skills is_ ON is_.entry_id = e.id
		WHERE e.entry_type = 'skill'
		  AND e.name = ? AND is_.scope = ? AND is_.project_root = ?`,
		name, string(scope), projectRoot,
	)

	sk, installedSkillID, err := scanSkillFields(row)
	if err != nil {
		return skills.InstalledSkill{}, err
	}

	deps, err := fetchDependencies(ctx, s.db, installedSkillID)
	if err != nil {
		return skills.InstalledSkill{}, err
	}
	sk.Dependencies = deps

	return sk, nil
}

// List returns all installed skills matching the given filter.
func (s *SkillStore) List(ctx context.Context, filter storage.ListFilter) ([]skills.InstalledSkill, error) {
	query := `SELECT ` + skillColumns + `
		FROM entries e
		JOIN installed_skills is_ ON is_.entry_id = e.id
		WHERE e.entry_type = 'skill'`

	var args []any

	if filter.Scope != "" {
		query += ` AND is_.scope = ?`
		args = append(args, string(filter.Scope))
	}
	if filter.ProjectRoot != "" {
		query += ` AND is_.project_root = ?`
		args = append(args, filter.ProjectRoot)
	}
	if filter.ClientApp != "" {
		query += ` AND EXISTS (SELECT 1 FROM json_each(is_.client_apps) WHERE value = ?)`
		args = append(args, filter.ClientApp)
	}

	query += ` ORDER BY e.name`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying installed skills: %w", err)
	}
	defer func() { _ = rows.Close() }() // safety net for error paths

	// Phase 1: collect skills and their IDs. We must close rows before
	// fetching dependencies because SQLite is limited to one connection
	// (MaxOpenConns=1) and fetchDependencies needs the same connection.
	type skillWithID struct {
		skill skills.InstalledSkill
		id    int64
	}
	var intermediate []skillWithID
	for rows.Next() {
		sk, installedSkillID, scanErr := scanSkillFields(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		intermediate = append(intermediate, skillWithID{skill: sk, id: installedSkillID})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating skill rows: %w", err)
	}
	// Explicitly close to release the connection before phase 2.
	// The deferred Close is idempotent and handles any remaining paths.
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("closing skill rows: %w", err)
	}

	// Phase 2: fetch dependencies now that the connection is released.
	result := make([]skills.InstalledSkill, 0, len(intermediate))
	for _, item := range intermediate {
		deps, depErr := fetchDependencies(ctx, s.db, item.id)
		if depErr != nil {
			return nil, depErr
		}
		item.skill.Dependencies = deps
		result = append(result, item.skill)
	}

	return result, nil
}

// Update modifies an existing installed skill.
func (s *SkillStore) Update(ctx context.Context, skill skills.InstalledSkill) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer rollback(tx)

	// Look up entry_id and installed_skill_id.
	var entryID, installedSkillID int64
	err = tx.QueryRowContext(ctx, `
		SELECT e.id, is_.id
		FROM entries e
		JOIN installed_skills is_ ON is_.entry_id = e.id
		WHERE e.entry_type = 'skill'
		  AND e.name = ?
		  AND is_.scope = ?
		  AND is_.project_root = ?`,
		skill.Metadata.Name, string(skill.Scope), skill.ProjectRoot,
	).Scan(&entryID, &installedSkillID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return storage.ErrNotFound
		}
		return fmt.Errorf("looking up skill: %w", err)
	}

	// Update the entries timestamp.
	if _, err := tx.ExecContext(ctx,
		`UPDATE entries SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?`,
		entryID,
	); err != nil {
		return fmt.Errorf("updating entry timestamp: %w", err)
	}

	tagsJSON, err := encodeJSONB(skill.Metadata.Tags)
	if err != nil {
		return fmt.Errorf("encoding tags: %w", err)
	}
	clientsJSON, err := encodeJSONB(skill.Clients)
	if err != nil {
		return fmt.Errorf("encoding clients: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE installed_skills SET
			reference = ?, tag = ?, digest = ?, version = ?, description = ?,
			author = ?, tags = jsonb(?), client_apps = jsonb(?), status = ?
		WHERE id = ?`,
		skill.Reference,
		skill.Tag,
		skill.Digest,
		skill.Metadata.Version,
		skill.Metadata.Description,
		skill.Metadata.Author,
		tagsJSON,
		clientsJSON,
		string(skill.Status),
		installedSkillID,
	); err != nil {
		return fmt.Errorf("updating installed skill: %w", err)
	}

	// Replace dependencies: delete existing, then re-insert.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM skill_dependencies WHERE installed_skill_id = ?`,
		installedSkillID,
	); err != nil {
		return fmt.Errorf("deleting old dependencies: %w", err)
	}

	if err := insertDependencies(ctx, tx, installedSkillID, skill.Dependencies); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}

	return nil
}

// Delete removes an installed skill by name, scope, and project root.
func (s *SkillStore) Delete(ctx context.Context, name string, scope skills.Scope, projectRoot string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer rollback(tx)

	// Delete the specific installed_skills row. CASCADE will clean up
	// skill_dependencies.
	res, err := tx.ExecContext(ctx, `
		DELETE FROM installed_skills WHERE id IN (
			SELECT is_.id FROM installed_skills is_
			JOIN entries e ON is_.entry_id = e.id
			WHERE e.entry_type = 'skill'
			  AND e.name = ?
			  AND is_.scope = ?
			  AND is_.project_root = ?
		)`,
		name, string(scope), projectRoot,
	)
	if err != nil {
		return fmt.Errorf("deleting installed skill: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected: %w", err)
	}
	if affected == 0 {
		return storage.ErrNotFound
	}

	// Clean up the parent entry if no installed_skills remain for it.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM entries WHERE entry_type = 'skill' AND name = ?
		  AND NOT EXISTS (SELECT 1 FROM installed_skills WHERE entry_id = entries.id)`,
		name,
	); err != nil {
		return fmt.Errorf("cleaning up orphaned entry: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}

	return nil
}

// scanner is an interface satisfied by both *sql.Row and *sql.Rows.
type scanner interface{ Scan(dest ...any) error }

// scanSkillFields scans a skill row into an InstalledSkill and its DB id.
func scanSkillFields(sc scanner) (skills.InstalledSkill, int64, error) {
	var (
		installedSkillID int64
		name             string
		scope            string
		projectRoot      string
		reference        string
		tag              string
		digest           string
		version          string
		description      string
		author           string
		tagsBlob         []byte
		clientsBlob      []byte
		status           string
		installedAtStr   string
	)

	err := sc.Scan(
		&installedSkillID, &name, &scope, &projectRoot, &reference, &tag,
		&digest, &version, &description, &author, &tagsBlob,
		&clientsBlob, &status, &installedAtStr,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return skills.InstalledSkill{}, 0, storage.ErrNotFound
		}
		return skills.InstalledSkill{}, 0, fmt.Errorf("scanning skill row: %w", err)
	}

	installedAt, err := time.Parse(time.RFC3339Nano, installedAtStr)
	if err != nil {
		return skills.InstalledSkill{}, 0, fmt.Errorf("parsing installed_at: %w", err)
	}
	tags, err := decodeJSONB(tagsBlob)
	if err != nil {
		return skills.InstalledSkill{}, 0, fmt.Errorf("decoding tags: %w", err)
	}
	clients, err := decodeJSONB(clientsBlob)
	if err != nil {
		return skills.InstalledSkill{}, 0, fmt.Errorf("decoding clients: %w", err)
	}
	sk := skills.InstalledSkill{
		Metadata: skills.SkillMetadata{
			Name:        name,
			Version:     version,
			Description: description,
			Author:      author,
			Tags:        tags,
		},
		Scope:       skills.Scope(scope),
		ProjectRoot: projectRoot,
		Reference:   reference,
		Tag:         tag,
		Digest:      digest,
		Status:      skills.InstallStatus(status),
		InstalledAt: installedAt,
		Clients:     clients,
	}

	return sk, installedSkillID, nil
}

// fetchDependencies retrieves all dependencies for a given installed skill ID.
func fetchDependencies(ctx context.Context, db *sql.DB, installedSkillID int64) ([]skills.Dependency, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT dep_name, dep_reference, dep_digest
		 FROM skill_dependencies
		 WHERE installed_skill_id = ?`,
		installedSkillID,
	)
	if err != nil {
		return nil, fmt.Errorf("querying dependencies: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var deps []skills.Dependency
	for rows.Next() {
		var d skills.Dependency
		if err := rows.Scan(&d.Name, &d.Reference, &d.Digest); err != nil {
			return nil, fmt.Errorf("scanning dependency: %w", err)
		}
		deps = append(deps, d)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating dependency rows: %w", err)
	}

	return deps, nil
}

// insertDependencies inserts skill dependencies within a transaction.
func insertDependencies(ctx context.Context, tx *sql.Tx, installedSkillID int64, deps []skills.Dependency) error {
	for _, dep := range deps {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO skill_dependencies (installed_skill_id, dep_name, dep_reference, dep_digest)
			 VALUES (?, ?, ?, ?)`,
			installedSkillID, dep.Name, dep.Reference, dep.Digest,
		); err != nil {
			return fmt.Errorf("inserting dependency %q: %w", dep.Reference, err)
		}
	}
	return nil
}

// encodeJSONB marshals a string slice for the SQLite jsonb() function.
func encodeJSONB(values []string) (string, error) {
	if values == nil {
		return "null", nil
	}
	data, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("marshaling JSON: %w", err)
	}
	return string(data), nil
}

// decodeJSONB unmarshals a JSONB blob from SQLite into a string slice.
func decodeJSONB(data []byte) ([]string, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var result []string
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("unmarshaling JSON: %w", err)
	}
	return result, nil
}

// isUniqueViolation checks for a SQLite UNIQUE constraint violation.
func isUniqueViolation(err error) bool {
	var sqliteErr *sqlite3.Error
	if errors.As(err, &sqliteErr) {
		return sqliteErr.Code() == sqlite3lib.SQLITE_CONSTRAINT_UNIQUE
	}
	return false
}

// rollback rolls back tx, ignoring errors (tx may already be committed).
func rollback(tx *sql.Tx) { _ = tx.Rollback() }
