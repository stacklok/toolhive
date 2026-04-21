// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package memory defines the types and interfaces for ToolHive's shared long-term memory system.
package memory

import "time"

// MemoryType distinguishes the two long-term memory namespaces.
//
//nolint:revive // MemoryType is the canonical cross-package name; renaming to Type causes ambiguity.
type MemoryType string

const (
	// MemoryTypeSemantic represents factual knowledge and world-state memories.
	MemoryTypeSemantic MemoryType = "semantic"
	// MemoryTypeProcedural represents how-to knowledge and step-based memories.
	MemoryTypeProcedural MemoryType = "procedural"
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

// MemoryEntry is the core domain type representing one stored memory.
//
//nolint:revive // MemoryEntry is the canonical cross-package name; renaming to Entry conflicts with common identifiers.
type MemoryEntry struct {
	ID               string
	Type             MemoryType
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
	History          []MemoryRevision
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// MemoryRevision records a single correction to a memory entry.
//
//nolint:revive // MemoryRevision is the canonical cross-package name; renaming to Revision causes ambiguity.
type MemoryRevision struct {
	Content        string
	Author         AuthorType
	CorrectionNote string
	Timestamp      time.Time
}

// ListFilter restricts results returned by MemoryStore.List.
type ListFilter struct {
	Type   *MemoryType
	Author *AuthorType
	Tags   []string
	Source *SourceType
	Status *EntryStatus
	Limit  int
	Offset int
}

// VectorFilter restricts similarity search to a subset of entries.
type VectorFilter struct {
	Type   *MemoryType
	Status *EntryStatus
}

// ScoredID pairs an entry ID with its cosine similarity to a query.
type ScoredID struct {
	ID         string
	Similarity float32
}

// ScoredEntry pairs a full MemoryEntry with its similarity to a query.
type ScoredEntry struct {
	Entry      MemoryEntry
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
