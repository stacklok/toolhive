// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package memory defines the types and interfaces for ToolHive's shared long-term memory system.
package memory

import "time"

// Type distinguishes the two long-term memory namespaces.
type Type string

const (
	// TypeSemantic represents factual, aggregated knowledge and world-state memories
	// (e.g. "company does not sponsor visas"). Contrast with TypeEpisodic.
	TypeSemantic Type = "semantic"
	// TypeProcedural represents how-to knowledge and step-based memories.
	TypeProcedural Type = "procedural"
	// TypeEpisodic represents time-indexed event records tied to a specific
	// moment (e.g. "recruiter archived candidate on 2024-03-15 — visa required").
	// Use CreatedAfter/CreatedBefore in ListFilter to query timelines.
	TypeEpisodic Type = "episodic"
)

// AuthorType records whether a memory was written by a human or an agent.
type AuthorType string

const (
	// AuthorHuman indicates the memory was written by a human user.
	AuthorHuman AuthorType = "human"
	// AuthorAgent indicates the memory was written by an AI agent.
	AuthorAgent AuthorType = "agent"
)

// SourceType records whether a memory entry originates from the store or is a
// read-only index of an installed Skill.
type SourceType string

const (
	// SourceMemory indicates the entry originates from the writable memory store.
	SourceMemory SourceType = "memory"
	// SourceSkill indicates the entry is a read-only index of an installed Skill.
	SourceSkill SourceType = "skill"
)

// EntryStatus is the lifecycle state of a memory entry.
type EntryStatus string

const (
	// EntryStatusActive indicates the entry is in normal use.
	EntryStatusActive EntryStatus = "active"
	// EntryStatusFlagged indicates the entry has been marked for review.
	EntryStatusFlagged EntryStatus = "flagged"
	// EntryStatusExpired indicates the entry has passed its TTL.
	EntryStatusExpired EntryStatus = "expired"
	// EntryStatusArchived indicates the entry has been moved to the archive.
	EntryStatusArchived EntryStatus = "archived"
)

// ArchiveReason records why an entry was archived.
type ArchiveReason string

const (
	// ArchiveReasonConsolidated indicates the entry was merged into a newer entry.
	ArchiveReasonConsolidated ArchiveReason = "consolidated"
	// ArchiveReasonCrystallized indicates the entry was promoted to a skill.
	ArchiveReasonCrystallized ArchiveReason = "crystallized"
	// ArchiveReasonManual indicates the entry was manually archived.
	ArchiveReasonManual ArchiveReason = "manual"
	// ArchiveReasonExpired indicates the entry exceeded its TTL.
	ArchiveReasonExpired ArchiveReason = "expired"
)

// Entry is the core domain type representing one stored memory.
type Entry struct {
	ID               string
	Type             Type
	Content          string
	Tags             []string
	Author           AuthorType
	AgentID          string
	SessionID        string
	Source           SourceType
	SkillRef         string
	Status           EntryStatus
	TrustScore       float32
	StalenessScore   float32
	AccessCount      int
	LastAccessedAt   time.Time
	FlaggedAt        *time.Time
	FlagReason       string
	TTLDays          *int
	ExpiresAt        *time.Time
	ArchivedAt       *time.Time
	ConsolidatedInto string
	CrystallizedInto string
	History          []Revision
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Revision records a single correction to a memory entry.
type Revision struct {
	Content        string
	Author         AuthorType
	CorrectionNote string
	Timestamp      time.Time
}

// ListFilter restricts results returned by MemoryStore.List.
type ListFilter struct {
	Type          *Type
	Author        *AuthorType
	Tags          []string
	Source        *SourceType
	Status        *EntryStatus
	CreatedAfter  *time.Time
	CreatedBefore *time.Time
	Limit         int
	Offset        int
}

// VectorFilter restricts similarity search to a subset of entries.
type VectorFilter struct {
	Type   *Type
	Status *EntryStatus
}

// ScoredID pairs an entry ID with its cosine similarity to a query.
type ScoredID struct {
	ID         string
	Similarity float32
}

// ScoredEntry pairs a full Entry with its similarity to a query.
type ScoredEntry struct {
	Entry      Entry
	Similarity float32
}

// ConflictResult describes a potentially conflicting existing memory returned
// during a write conflict check.
type ConflictResult struct {
	ID         string
	Content    string
	Similarity float32
	TrustScore float32
}
