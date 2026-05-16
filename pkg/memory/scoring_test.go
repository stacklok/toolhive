// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package memory_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/memory"
)

func TestComputeTrustScore(t *testing.T) {
	t.Parallel()
	now := time.Now()

	tests := []struct {
		name    string
		entry   memory.Entry
		wantMin float32
		wantMax float32
	}{
		{
			name: "fresh human entry has high trust",
			entry: memory.Entry{
				Author:    memory.AuthorHuman,
				CreatedAt: now,
			},
			wantMin: 0.95,
			wantMax: 1.0,
		},
		{
			name: "fresh agent entry has lower trust than human",
			entry: memory.Entry{
				Author:    memory.AuthorAgent,
				CreatedAt: now,
			},
			wantMin: 0.65,
			wantMax: 0.75,
		},
		{
			name: "flagged entry has halved trust",
			entry: func() memory.Entry {
				ft := now
				return memory.Entry{
					Author:    memory.AuthorHuman,
					CreatedAt: now,
					FlaggedAt: &ft,
				}
			}(),
			wantMin: 0.45,
			wantMax: 0.55,
		},
		{
			name: "two corrections reduce trust",
			entry: memory.Entry{
				Author:    memory.AuthorHuman,
				CreatedAt: now,
				History:   []memory.Revision{{}, {}},
			},
			wantMin: 0.85,
			wantMax: 0.95,
		},
		{
			name: "old entry has decayed trust",
			entry: memory.Entry{
				Author:    memory.AuthorHuman,
				CreatedAt: now.AddDate(0, 0, -180), // half-life
			},
			wantMin: 0.45,
			wantMax: 0.55,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			score := memory.ComputeTrustScore(tc.entry)
			require.GreaterOrEqual(t, score, tc.wantMin, "trust score too low")
			require.LessOrEqual(t, score, tc.wantMax, "trust score too high")
		})
	}
}

func TestComputeStalenessScore(t *testing.T) {
	t.Parallel()
	now := time.Now()

	tests := []struct {
		name    string
		entry   memory.Entry
		wantMin float32
		wantMax float32
	}{
		{
			name: "recently accessed entry is fresh",
			entry: memory.Entry{
				CreatedAt:      now,
				LastAccessedAt: now,
			},
			wantMin: 0.0,
			wantMax: 0.05,
		},
		{
			name: "entry not accessed for 90 days is stale",
			entry: memory.Entry{
				CreatedAt:      now.AddDate(0, 0, -90),
				LastAccessedAt: now.AddDate(0, 0, -90),
			},
			wantMin: 0.95,
			wantMax: 1.0,
		},
		{
			name: "flagged entry adds staleness bonus",
			entry: func() memory.Entry {
				ft := now
				return memory.Entry{
					CreatedAt:      now,
					LastAccessedAt: now,
					FlaggedAt:      &ft,
				}
			}(),
			wantMin: 0.28,
			wantMax: 0.32,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			score := memory.ComputeStalenessScore(tc.entry)
			require.GreaterOrEqual(t, score, tc.wantMin)
			require.LessOrEqual(t, score, tc.wantMax)
		})
	}
}
