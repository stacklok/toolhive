// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"

	"github.com/stacklok/toolhive/pkg/memory"
)

// VectorStore implements memory.VectorStore using SQLite blob storage and
// Go-native cosine similarity. Suitable for datasets up to ~100K entries.
// Use an external VectorStore (Qdrant, pgvector) for larger datasets.
type VectorStore struct {
	db *sql.DB
}

// NewVectorStore creates a new SQLite-backed VectorStore.
func NewVectorStore(wrapper *DB) *VectorStore {
	return &VectorStore{db: wrapper.DB()}
}

var _ memory.VectorStore = (*VectorStore)(nil)

// Upsert stores or replaces the embedding for entry id.
func (v *VectorStore) Upsert(ctx context.Context, id string, embedding []float32) error {
	data, err := json.Marshal(embedding)
	if err != nil {
		return fmt.Errorf("marshalling embedding: %w", err)
	}
	_, err = v.db.ExecContext(ctx,
		`INSERT INTO memory_embeddings (entry_id, embedding) VALUES (?,?)
		 ON CONFLICT(entry_id) DO UPDATE SET embedding=excluded.embedding`,
		id, string(data))
	return err
}

// Search loads all embeddings matching the filter, computes cosine similarity
// against query, and returns the topK results in descending score order.
func (v *VectorStore) Search(
	ctx context.Context, query []float32, topK int, filter memory.VectorFilter,
) ([]memory.ScoredID, error) {
	q := `SELECT e.entry_id, e.embedding
		  FROM memory_embeddings e
		  JOIN memory_entries m ON m.id = e.entry_id
		  WHERE 1=1`
	var args []any
	if filter.Type != nil {
		q += " AND m.type=?"
		args = append(args, string(*filter.Type))
	}
	if filter.Status != nil {
		q += " AND m.status=?"
		args = append(args, string(*filter.Status))
	}
	if filter.Source != nil {
		q += " AND m.source=?"
		args = append(args, string(*filter.Source))
	}

	rows, err := v.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	qNorm := l2Norm(query)
	if qNorm == 0 {
		return nil, fmt.Errorf("query vector has zero magnitude")
	}

	var scored []memory.ScoredID
	for rows.Next() {
		var id, embJSON string
		if err := rows.Scan(&id, &embJSON); err != nil {
			return nil, err
		}
		var emb []float32
		if err := json.Unmarshal([]byte(embJSON), &emb); err != nil {
			continue
		}
		sim := cosineSimilarity(query, emb, qNorm)
		scored = append(scored, memory.ScoredID{ID: id, Similarity: sim})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].Similarity > scored[j].Similarity
	})
	if topK > 0 && len(scored) > topK {
		scored = scored[:topK]
	}
	return scored, nil
}

// Delete removes the embedding for entry id.
func (v *VectorStore) Delete(ctx context.Context, id string) error {
	_, err := v.db.ExecContext(ctx, `DELETE FROM memory_embeddings WHERE entry_id=?`, id)
	return err
}

func cosineSimilarity(a, b []float32, aNorm float32) float32 {
	if len(a) != len(b) || aNorm == 0 {
		return 0
	}
	bNorm := l2Norm(b)
	if bNorm == 0 {
		return 0
	}
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return float32(dot / (float64(aNorm) * float64(bNorm)))
}

func l2Norm(v []float32) float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	return float32(math.Sqrt(sum))
}
