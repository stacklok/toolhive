// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skills

import "testing"

// FuzzParseSkillMD checks that parsing an untrusted SKILL.md never panics.
func FuzzParseSkillMD(f *testing.F) {
	for _, seed := range []string{
		"---\nname: my-skill\ndescription: A test skill\n---\n# My Skill\n\nSome body content.\n",
		"---\nname: my-skill\ndescription: d\nversion: 1.2.3\nallowed-tools: Read Glob Grep\nlicense: Apache-2.0\nmetadata:\n  author: a\n---\n",
		"---\nname: s\ndescription: d\nallowed-tools:\n  - Read\n  - Bash\nrequires:\n  - ghcr.io/org/skill:v1\n---\n",
		"---\nname: [unclosed\n---\n",
		"---\na: &a [*a]\n---\n",
		"no frontmatter",
		"---\n",
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(_ *testing.T, content []byte) {
		_, _ = ParseSkillMD(content)
	})
}
