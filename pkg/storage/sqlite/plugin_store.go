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

	ociplugins "github.com/stacklok/toolhive-core/oci/plugins"
	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/storage"
)

// PluginStore implements storage.PluginStore using SQLite. It is a structural
// mirror of SkillStore (pkg/storage/sqlite/skill_store.go), substituting
// plugin types and the installed_plugins/plugin_dependencies tables. It shares
// the entries table with SkillStore via the entry_type discriminator
// (storage.EntryTypePlugin), so a skill and a plugin can share the same name.
type PluginStore struct {
	wrapper *DB
	db      *sql.DB
}

// NewPluginStore creates a new SQLite-backed PluginStore.
func NewPluginStore(db *DB) *PluginStore {
	return &PluginStore{wrapper: db, db: db.DB()}
}

// Close closes the underlying database connection.
func (s *PluginStore) Close() error {
	return s.wrapper.Close()
}

var _ storage.PluginStore = (*PluginStore)(nil)

// pluginColumns is the SELECT column list shared by Get and List queries. It
// expands skillColumns with the plugin-specific keywords, components, and
// signature columns. json() is used for the JSONB columns so SQLite returns
// text we can unmarshal; the columns are stored via jsonb().
const pluginColumns = `ip_.id, e.name, ip_.scope, ip_.project_root, ip_.reference, ip_.tag,
			ip_.digest, ip_.version, ip_.description, ip_.author, ip_.license, json(ip_.keywords),
			json(ip_.client_apps), json(ip_.components), ip_.signature, ip_.status, ip_.installed_at`

// Create stores a new installed plugin.
func (s *PluginStore) Create(ctx context.Context, plugin plugins.InstalledPlugin) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer rollback(tx)

	// Upsert into entries table. A single plugin name can have multiple
	// installations (user-scoped + project-scoped), so we reuse the entry if it
	// already exists. Look up by (EntryTypePlugin, name) — never name alone, so
	// a skill of the same name doesn't collide (entries has UNIQUE
	// (entry_type, name)).
	var entryID int64
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM entries WHERE entry_type = ? AND name = ?`,
		string(storage.EntryTypePlugin), plugin.Metadata.Name,
	).Scan(&entryID)
	if errors.Is(err, sql.ErrNoRows) {
		res, insertErr := tx.ExecContext(ctx,
			`INSERT INTO entries (entry_type, name) VALUES (?, ?)`,
			string(storage.EntryTypePlugin), plugin.Metadata.Name,
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

	keywordsJSON, err := encodeJSONB(plugin.Metadata.Keywords)
	if err != nil {
		return fmt.Errorf("encoding keywords: %w", err)
	}
	clientsJSON, err := encodeJSONB(plugin.Clients)
	if err != nil {
		return fmt.Errorf("encoding clients: %w", err)
	}
	componentsJSON, err := encodeComponentInventory(plugin.Components)
	if err != nil {
		return fmt.Errorf("encoding components: %w", err)
	}

	res, err := tx.ExecContext(ctx, `
		INSERT INTO installed_plugins (
			entry_id, scope, project_root, reference, tag, digest,
			version, description, author, license, keywords, client_apps, components,
			signature, status
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, jsonb(?), jsonb(?), jsonb(?), ?, ?)`,
		entryID,
		string(plugin.Scope),
		plugin.ProjectRoot,
		plugin.Reference,
		plugin.Tag,
		plugin.Digest,
		plugin.Metadata.Version,
		plugin.Metadata.Description,
		plugin.Metadata.Author,
		plugin.Metadata.License,
		keywordsJSON,
		clientsJSON,
		componentsJSON,
		nullableSignature(plugin.Signature),
		string(plugin.Status),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return storage.ErrAlreadyExists
		}
		return fmt.Errorf("inserting installed plugin: %w", err)
	}

	installedPluginID, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("getting installed plugin id: %w", err)
	}

	// Insert dependencies.
	if err := insertPluginDependencies(ctx, tx, installedPluginID, plugin.Dependencies); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}

	return nil
}

// Get retrieves an installed plugin by name, scope, and project root.
func (s *PluginStore) Get(
	ctx context.Context, name string, scope plugins.Scope, projectRoot string,
) (plugins.InstalledPlugin, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+pluginColumns+`
		FROM entries e
		JOIN installed_plugins ip_ ON ip_.entry_id = e.id
		WHERE e.entry_type = ?
		  AND e.name = ? AND ip_.scope = ? AND ip_.project_root = ?`,
		string(storage.EntryTypePlugin), name, string(scope), projectRoot,
	)

	pl, installedPluginID, err := scanPluginFields(row)
	if err != nil {
		return plugins.InstalledPlugin{}, err
	}

	deps, err := fetchPluginDependencies(ctx, s.db, installedPluginID)
	if err != nil {
		return plugins.InstalledPlugin{}, err
	}
	pl.Dependencies = deps

	return pl, nil
}

// List returns all installed plugins matching the given filter. It uses the
// SAME two-phase fetch pattern as SkillStore.List: Phase 1 collects rows + IDs
// and closes the rows cursor; Phase 2 fetches dependencies per ID. This is
// required because SQLite MaxOpenConns=1 means fetchDependencies cannot run
// while the List rows cursor is still open — forgetting this deadlocks under
// load.
func (s *PluginStore) List(ctx context.Context, filter storage.ListFilter) ([]plugins.InstalledPlugin, error) {
	query := `SELECT ` + pluginColumns + `
		FROM entries e
		JOIN installed_plugins ip_ ON ip_.entry_id = e.id
		WHERE e.entry_type = ?`

	args := []any{string(storage.EntryTypePlugin)}

	if filter.Scope != "" {
		query += ` AND ip_.scope = ?`
		args = append(args, string(filter.Scope))
	}
	if filter.ProjectRoot != "" {
		query += ` AND ip_.project_root = ?`
		args = append(args, filter.ProjectRoot)
	}
	if filter.ClientApp != "" {
		query += ` AND EXISTS (SELECT 1 FROM json_each(ip_.client_apps) WHERE value = ?)`
		args = append(args, filter.ClientApp)
	}

	query += ` ORDER BY e.name`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying installed plugins: %w", err)
	}
	defer func() { _ = rows.Close() }() // safety net for error paths

	// Phase 1: collect plugins and their IDs. We must close rows before
	// fetching dependencies because SQLite is limited to one connection
	// (MaxOpenConns=1) and fetchPluginDependencies needs the same connection.
	type pluginWithID struct {
		plugin plugins.InstalledPlugin
		id     int64
	}
	var intermediate []pluginWithID
	for rows.Next() {
		pl, installedPluginID, scanErr := scanPluginFields(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		intermediate = append(intermediate, pluginWithID{plugin: pl, id: installedPluginID})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating plugin rows: %w", err)
	}
	// Explicitly close to release the connection before phase 2.
	// The deferred Close is idempotent and handles any remaining paths.
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("closing plugin rows: %w", err)
	}

	// Phase 2: fetch dependencies now that the connection is released.
	result := make([]plugins.InstalledPlugin, 0, len(intermediate))
	for _, item := range intermediate {
		deps, depErr := fetchPluginDependencies(ctx, s.db, item.id)
		if depErr != nil {
			return nil, depErr
		}
		item.plugin.Dependencies = deps
		result = append(result, item.plugin)
	}

	return result, nil
}

// Update modifies an existing installed plugin.
func (s *PluginStore) Update(ctx context.Context, plugin plugins.InstalledPlugin) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer rollback(tx)

	// Look up entry_id and installed_plugin_id.
	var entryID, installedPluginID int64
	err = tx.QueryRowContext(ctx, `
		SELECT e.id, ip_.id
		FROM entries e
		JOIN installed_plugins ip_ ON ip_.entry_id = e.id
		WHERE e.entry_type = ?
		  AND e.name = ?
		  AND ip_.scope = ?
		  AND ip_.project_root = ?`,
		string(storage.EntryTypePlugin), plugin.Metadata.Name, string(plugin.Scope), plugin.ProjectRoot,
	).Scan(&entryID, &installedPluginID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return storage.ErrNotFound
		}
		return fmt.Errorf("looking up plugin: %w", err)
	}

	// Update the entries timestamp.
	if _, err := tx.ExecContext(ctx,
		`UPDATE entries SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?`,
		entryID,
	); err != nil {
		return fmt.Errorf("updating entry timestamp: %w", err)
	}

	keywordsJSON, err := encodeJSONB(plugin.Metadata.Keywords)
	if err != nil {
		return fmt.Errorf("encoding keywords: %w", err)
	}
	clientsJSON, err := encodeJSONB(plugin.Clients)
	if err != nil {
		return fmt.Errorf("encoding clients: %w", err)
	}
	componentsJSON, err := encodeComponentInventory(plugin.Components)
	if err != nil {
		return fmt.Errorf("encoding components: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE installed_plugins SET
			reference = ?, tag = ?, digest = ?, version = ?, description = ?,
			author = ?, license = ?, keywords = jsonb(?), client_apps = jsonb(?),
			components = jsonb(?), signature = ?, status = ?
		WHERE id = ?`,
		plugin.Reference,
		plugin.Tag,
		plugin.Digest,
		plugin.Metadata.Version,
		plugin.Metadata.Description,
		plugin.Metadata.Author,
		plugin.Metadata.License,
		keywordsJSON,
		clientsJSON,
		componentsJSON,
		nullableSignature(plugin.Signature),
		string(plugin.Status),
		installedPluginID,
	); err != nil {
		return fmt.Errorf("updating installed plugin: %w", err)
	}

	// Replace dependencies: delete existing, then re-insert.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM plugin_dependencies WHERE installed_plugin_id = ?`,
		installedPluginID,
	); err != nil {
		return fmt.Errorf("deleting old dependencies: %w", err)
	}

	if err := insertPluginDependencies(ctx, tx, installedPluginID, plugin.Dependencies); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}

	return nil
}

// Delete removes an installed plugin by name, scope, and project root.
func (s *PluginStore) Delete(ctx context.Context, name string, scope plugins.Scope, projectRoot string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer rollback(tx)

	// Delete the specific installed_plugins row. CASCADE will clean up
	// plugin_dependencies.
	res, err := tx.ExecContext(ctx, `
		DELETE FROM installed_plugins WHERE id IN (
			SELECT ip_.id FROM installed_plugins ip_
			JOIN entries e ON ip_.entry_id = e.id
			WHERE e.entry_type = ?
			  AND e.name = ?
			  AND ip_.scope = ?
			  AND ip_.project_root = ?
		)`,
		string(storage.EntryTypePlugin), name, string(scope), projectRoot,
	)
	if err != nil {
		return fmt.Errorf("deleting installed plugin: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected: %w", err)
	}
	if affected == 0 {
		return storage.ErrNotFound
	}

	// Clean up the parent entry if no installed_plugins remain for it.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM entries WHERE entry_type = ? AND name = ?
		  AND NOT EXISTS (SELECT 1 FROM installed_plugins WHERE entry_id = entries.id)`,
		string(storage.EntryTypePlugin), name,
	); err != nil {
		return fmt.Errorf("cleaning up orphaned entry: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}

	return nil
}

// scanPluginFields scans a plugin row into an InstalledPlugin and its DB id.
// The column list must match pluginColumns (17 fields: id, name, scope,
// project_root, reference, tag, digest, version, description, author, license,
// keywords, client_apps, components, signature, status, installed_at).
func scanPluginFields(sc scanner) (plugins.InstalledPlugin, int64, error) {
	var (
		installedPluginID int64
		name              string
		scope             string
		projectRoot       string
		reference         string
		tag               string
		digest            string
		version           string
		description       string
		author            string
		license           string
		keywordsBlob      []byte
		clientsBlob       []byte
		componentsBlob    []byte
		signature         sql.NullString
		status            string
		installedAtStr    string
	)

	err := sc.Scan(
		&installedPluginID, &name, &scope, &projectRoot, &reference, &tag,
		&digest, &version, &description, &author, &license, &keywordsBlob,
		&clientsBlob, &componentsBlob, &signature, &status, &installedAtStr,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return plugins.InstalledPlugin{}, 0, storage.ErrNotFound
		}
		return plugins.InstalledPlugin{}, 0, fmt.Errorf("scanning plugin row: %w", err)
	}

	installedAt, err := time.Parse(time.RFC3339Nano, installedAtStr)
	if err != nil {
		return plugins.InstalledPlugin{}, 0, fmt.Errorf("parsing installed_at: %w", err)
	}
	keywords, err := decodeJSONB(keywordsBlob)
	if err != nil {
		return plugins.InstalledPlugin{}, 0, fmt.Errorf("decoding keywords: %w", err)
	}
	clients, err := decodeJSONB(clientsBlob)
	if err != nil {
		return plugins.InstalledPlugin{}, 0, fmt.Errorf("decoding clients: %w", err)
	}
	components, err := decodeComponentInventory(componentsBlob)
	if err != nil {
		return plugins.InstalledPlugin{}, 0, fmt.Errorf("decoding components: %w", err)
	}
	pl := plugins.InstalledPlugin{
		Metadata: plugins.PluginMetadata{
			Name:        name,
			Version:     version,
			Description: description,
			Author:      author,
			License:     license,
			Keywords:    keywords,
		},
		Scope:       plugins.Scope(scope),
		ProjectRoot: projectRoot,
		Reference:   reference,
		Tag:         tag,
		Digest:      digest,
		Status:      plugins.InstallStatus(status),
		InstalledAt: installedAt,
		Clients:     clients,
		Components:  components,
	}
	// signature is nullable — set Signature only when Valid.
	if signature.Valid {
		pl.Signature = signature.String
	}

	return pl, installedPluginID, nil
}

// fetchPluginDependencies retrieves all dependencies for a given installed plugin ID.
func fetchPluginDependencies(ctx context.Context, db *sql.DB, installedPluginID int64) ([]plugins.Dependency, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT dep_name, dep_reference, dep_digest
		 FROM plugin_dependencies
		 WHERE installed_plugin_id = ?`,
		installedPluginID,
	)
	if err != nil {
		return nil, fmt.Errorf("querying plugin dependencies: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var deps []plugins.Dependency
	for rows.Next() {
		var d plugins.Dependency
		if err := rows.Scan(&d.Name, &d.Reference, &d.Digest); err != nil {
			return nil, fmt.Errorf("scanning plugin dependency: %w", err)
		}
		deps = append(deps, d)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating plugin dependency rows: %w", err)
	}

	return deps, nil
}

// insertPluginDependencies inserts plugin dependencies within a transaction.
func insertPluginDependencies(ctx context.Context, tx *sql.Tx, installedPluginID int64, deps []plugins.Dependency) error {
	for _, dep := range deps {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO plugin_dependencies (installed_plugin_id, dep_name, dep_reference, dep_digest)
			 VALUES (?, ?, ?, ?)`,
			installedPluginID, dep.Name, dep.Reference, dep.Digest,
		); err != nil {
			return fmt.Errorf("inserting plugin dependency %q: %w", dep.Reference, err)
		}
	}
	return nil
}

// encodeComponentInventory marshals a ComponentInventory (map[string]int) for
// the SQLite jsonb() function. A nil map is stored as SQL NULL ("null" text)
// so it round-trips as nil, matching the encodeJSONB convention.
func encodeComponentInventory(c ociplugins.ComponentInventory) (string, error) {
	if c == nil {
		return "null", nil
	}
	data, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("marshaling components: %w", err)
	}
	return string(data), nil
}

// decodeComponentInventory unmarshals a JSONB blob into a ComponentInventory.
// An empty/NULL blob decodes to nil.
func decodeComponentInventory(data []byte) (ociplugins.ComponentInventory, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var result ociplugins.ComponentInventory
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("unmarshaling components: %w", err)
	}
	return result, nil
}

// nullableSignature returns a sql.NullString-compatible value for the
// signature column: NULL when the signature is empty (so it round-trips as
// empty), the string otherwise. Using sql.NullString via the driver requires
// returning nil for the empty case; the column accepts a TEXT or NULL argument.
func nullableSignature(signature string) any {
	if signature == "" {
		return nil
	}
	return signature
}
