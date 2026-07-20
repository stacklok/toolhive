// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

// EntryType is the typed discriminator for the entries.entry_type column.
// The entries table is shared across installed skills and plugins so that a
// skill and a plugin can share the same name without colliding; the
// (entry_type, name) pair is unique, not name alone. Both skill_store.go and
// plugin_store.go reference these constants without importing each other's
// domain package.
type EntryType string

const (
	// EntryTypeSkill identifies entries that own installed_skills rows.
	EntryTypeSkill EntryType = "skill"
	// EntryTypePlugin identifies entries that own installed_plugins rows.
	EntryTypePlugin EntryType = "plugin"
)
