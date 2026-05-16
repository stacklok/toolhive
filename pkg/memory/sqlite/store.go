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

	"github.com/stacklok/toolhive/pkg/memory"
)

// Store implements memory.Store using SQLite.
type Store struct {
	db *sql.DB
}

// NewStore creates a new SQLite-backed Store.
func NewStore(wrapper *DB) *Store {
	return &Store{db: wrapper.DB()}
}

var _ memory.Store = (*Store)(nil)

// Create inserts a new memory entry.
func (s *Store) Create(ctx context.Context, e memory.Entry) error {
	tags, err := json.Marshal(e.Tags)
	if err != nil {
		return fmt.Errorf("marshalling tags: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO memory_entries
			(id, type, content, tags, author, agent_id, session_id, source, skill_ref,
			 status, trust_score, staleness_score, access_count, last_accessed_at,
			 ttl_days, expires_at, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.ID, string(e.Type), e.Content, string(tags),
		string(e.Author), e.AgentID, e.SessionID, string(e.Source), e.SkillRef,
		string(e.Status), e.TrustScore, e.StalenessScore, e.AccessCount,
		nullableTime(e.LastAccessedAt),
		e.TTLDays, nullableTimePtr(e.ExpiresAt),
		e.CreatedAt.UTC().Format(time.RFC3339Nano),
		e.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	return err
}

// Get retrieves a single entry by ID, including its revision history.
func (s *Store) Get(ctx context.Context, id string) (memory.Entry, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, type, content, tags, author, agent_id, session_id, source, skill_ref,
		       status, trust_score, staleness_score, access_count, last_accessed_at,
		       flagged_at, flag_reason, ttl_days, expires_at, archived_at,
		       consolidated_into, crystallized_into, created_at, updated_at
		FROM memory_entries WHERE id = ?`, id)

	e, err := scanEntry(row)
	if errors.Is(err, sql.ErrNoRows) {
		return memory.Entry{}, fmt.Errorf("entry %q: %w", id, memory.ErrNotFound)
	}
	if err != nil {
		return memory.Entry{}, err
	}

	e.History, err = s.loadHistory(ctx, id)
	return e, err
}

// Update replaces content and appends the old content to revisions.
func (s *Store) Update(ctx context.Context, id, content string, author memory.AuthorType, note string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)

	var oldContent string
	if err := tx.QueryRowContext(ctx, `SELECT content FROM memory_entries WHERE id = ?`, id).Scan(&oldContent); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("entry %q: %w", id, memory.ErrNotFound)
		}
		return err
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO memory_revisions (entry_id, content, author, correction_note, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		id, oldContent, string(author), note, time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE memory_entries SET content = ?, updated_at = ? WHERE id = ?`,
		content, time.Now().UTC().Format(time.RFC3339Nano), id,
	); err != nil {
		return err
	}

	return tx.Commit()
}

// Flag marks an entry as potentially stale.
func (s *Store) Flag(ctx context.Context, id, reason string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx,
		`UPDATE memory_entries SET status='flagged', flagged_at=?, flag_reason=?, updated_at=? WHERE id=?`,
		now, reason, now, id)
	return err
}

// Unflag clears the flag on an entry.
func (s *Store) Unflag(ctx context.Context, id string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx,
		`UPDATE memory_entries SET status='active', flagged_at=NULL, flag_reason='', updated_at=? WHERE id=?`,
		now, id)
	return err
}

// Delete permanently removes an entry.
func (s *Store) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM memory_entries WHERE id=?`, id)
	return err
}

// List returns entries matching the filter.
func (s *Store) List(ctx context.Context, f memory.ListFilter) ([]memory.Entry, error) {
	query := `SELECT id, type, content, tags, author, agent_id, session_id, source, skill_ref,
		       status, trust_score, staleness_score, access_count, last_accessed_at,
		       flagged_at, flag_reason, ttl_days, expires_at, archived_at,
		       consolidated_into, crystallized_into, created_at, updated_at
		FROM memory_entries WHERE 1=1`
	var args []any

	if f.Type != nil {
		query += " AND type=?"
		args = append(args, string(*f.Type))
	}
	if f.Author != nil {
		query += " AND author=?"
		args = append(args, string(*f.Author))
	}
	if f.Source != nil {
		query += " AND source=?"
		args = append(args, string(*f.Source))
	}
	if f.Status != nil {
		query += " AND status=?"
		args = append(args, string(*f.Status))
	}
	if f.CreatedAfter != nil {
		query += " AND created_at >= ?"
		args = append(args, f.CreatedAfter.UTC().Format(time.RFC3339Nano))
	}
	if f.CreatedBefore != nil {
		query += " AND created_at <= ?"
		args = append(args, f.CreatedBefore.UTC().Format(time.RFC3339Nano))
	}

	query += " ORDER BY created_at DESC"
	if f.Limit > 0 {
		query += " LIMIT ? OFFSET ?"
		args = append(args, f.Limit, f.Offset)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var entries []memory.Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// Archive transitions an entry to archived status.
func (s *Store) Archive(ctx context.Context, id string, reason memory.ArchiveReason, ref string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	field := consolidatedField(reason)
	_, err := s.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE memory_entries SET status='archived', archived_at=?, %s=?, updated_at=? WHERE id=?`, field),
		now, ref, now, id)
	return err
}

// IncrementAccess increments the access counter and updates last_accessed_at.
func (s *Store) IncrementAccess(ctx context.Context, id string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx,
		`UPDATE memory_entries SET access_count=access_count+1, last_accessed_at=?, updated_at=? WHERE id=?`,
		now, now, id)
	return err
}

// UpdateScores persists recomputed trust and staleness scores.
func (s *Store) UpdateScores(ctx context.Context, id string, trust, staleness float32) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE memory_entries SET trust_score=?, staleness_score=? WHERE id=?`,
		trust, staleness, id)
	return err
}

// ListExpired returns active entries whose TTL has elapsed.
func (s *Store) ListExpired(ctx context.Context) ([]memory.Entry, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, type, content, tags, author, agent_id, session_id, source, skill_ref,
		        status, trust_score, staleness_score, access_count, last_accessed_at,
		        flagged_at, flag_reason, ttl_days, expires_at, archived_at,
		        consolidated_into, crystallized_into, created_at, updated_at
		 FROM memory_entries
		 WHERE expires_at IS NOT NULL AND expires_at <= ? AND status NOT IN ('expired','archived')`, now)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var entries []memory.Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// ListActive returns all active and flagged entries for score recomputation.
func (s *Store) ListActive(ctx context.Context) ([]memory.Entry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, type, content, tags, author, agent_id, session_id, source, skill_ref,
		        status, trust_score, staleness_score, access_count, last_accessed_at,
		        flagged_at, flag_reason, ttl_days, expires_at, archived_at,
		        consolidated_into, crystallized_into, created_at, updated_at
		 FROM memory_entries WHERE status IN ('active','flagged')`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var entries []memory.Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// ---- helpers ----

type scanner interface {
	Scan(dest ...any) error
}

func scanEntry(sc scanner) (memory.Entry, error) {
	var e memory.Entry
	var (
		mtype, author, source, status string
		tagsJSON                      string
		lastAccessed, flaggedAt       sql.NullString
		expiresAt, archivedAt         sql.NullString
		createdAt, updatedAt          string
	)
	err := sc.Scan(
		&e.ID, &mtype, &e.Content, &tagsJSON, &author,
		&e.AgentID, &e.SessionID, &source, &e.SkillRef,
		&status, &e.TrustScore, &e.StalenessScore, &e.AccessCount, &lastAccessed,
		&flaggedAt, &e.FlagReason, &e.TTLDays, &expiresAt, &archivedAt,
		&e.ConsolidatedInto, &e.CrystallizedInto, &createdAt, &updatedAt,
	)
	if err != nil {
		return memory.Entry{}, err
	}
	e.Type = memory.Type(mtype)
	e.Author = memory.AuthorType(author)
	e.Source = memory.SourceType(source)
	e.Status = memory.EntryStatus(status)
	_ = json.Unmarshal([]byte(tagsJSON), &e.Tags)
	e.CreatedAt, _ = parseTime(createdAt)
	e.UpdatedAt, _ = parseTime(updatedAt)
	if lastAccessed.Valid {
		t, _ := parseTime(lastAccessed.String)
		e.LastAccessedAt = t
	}
	if flaggedAt.Valid {
		t, _ := parseTime(flaggedAt.String)
		e.FlaggedAt = &t
	}
	if expiresAt.Valid {
		t, _ := parseTime(expiresAt.String)
		e.ExpiresAt = &t
	}
	if archivedAt.Valid {
		t, _ := parseTime(archivedAt.String)
		e.ArchivedAt = &t
	}
	return e, nil
}

func (s *Store) loadHistory(ctx context.Context, entryID string) ([]memory.Revision, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT content, author, correction_note, created_at
		 FROM memory_revisions WHERE entry_id=? ORDER BY created_at ASC`, entryID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var revs []memory.Revision
	for rows.Next() {
		var r memory.Revision
		var author, createdAt string
		if err := rows.Scan(&r.Content, &author, &r.CorrectionNote, &createdAt); err != nil {
			return nil, err
		}
		r.Author = memory.AuthorType(author)
		r.Timestamp, _ = parseTime(createdAt)
		revs = append(revs, r)
	}
	return revs, rows.Err()
}

func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func nullableTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, s)
}

func consolidatedField(reason memory.ArchiveReason) string {
	if reason == memory.ArchiveReasonCrystallized {
		return "crystallized_into"
	}
	return "consolidated_into"
}

func rollback(tx *sql.Tx) {
	_ = tx.Rollback()
}
