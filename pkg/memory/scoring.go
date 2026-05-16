// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package memory

import (
	"math"
	"time"
)

const (
	authorWeightHuman              = 1.0
	authorWeightAgent              = 0.7
	halfLifeDays                   = 180.0
	maxCorrectionPenalty           = 0.30
	correctionPenaltyPerCorrection = 0.05
	flagTrustMultiplier            = 0.5
	maxStalenessAccessDays         = 90.0
	flagStalenessBonus             = 0.3
	correctionStalenessPerItem     = 0.1
	maxCorrectionStaleness         = 0.3
)

// ComputeTrustScore returns a value in [0,1] representing how trustworthy
// this memory entry is. Higher = more trustworthy.
//
// Formula: author_weight × age_decay × (1 - correction_penalty) × flag_multiplier
func ComputeTrustScore(entry Entry) float32 {
	weight := authorWeightAgent
	if entry.Author == AuthorHuman {
		weight = authorWeightHuman
	}

	ageInDays := time.Since(entry.CreatedAt).Hours() / 24
	decay := math.Exp(-ageInDays * math.Log(2) / halfLifeDays)

	corrections := len(entry.History)
	correctionPenalty := math.Min(float64(corrections)*correctionPenaltyPerCorrection, maxCorrectionPenalty)

	flagMultiplier := 1.0
	if entry.FlaggedAt != nil {
		flagMultiplier = flagTrustMultiplier
	}

	score := weight * decay * (1 - correctionPenalty) * flagMultiplier
	return float32(math.Max(0, math.Min(1, score)))
}

// ComputeStalenessScore returns a value in [0,1] representing how stale
// this memory entry is. Higher = more stale (more likely to need review).
//
// Formula: access_age_normalized + flag_bonus + correction_bonus
func ComputeStalenessScore(entry Entry) float32 {
	lastAccess := entry.LastAccessedAt
	if lastAccess.IsZero() {
		lastAccess = entry.CreatedAt
	}
	daysSinceAccess := time.Since(lastAccess).Hours() / 24
	base := math.Min(daysSinceAccess/maxStalenessAccessDays, 1.0)

	flagBonus := 0.0
	if entry.FlaggedAt != nil {
		flagBonus = flagStalenessBonus
	}

	corrections := len(entry.History)
	correctionBonus := math.Min(float64(corrections)*correctionStalenessPerItem, maxCorrectionStaleness)

	return float32(math.Min(1.0, base+flagBonus+correctionBonus))
}
