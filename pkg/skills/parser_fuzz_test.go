// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skills

import (
	"path/filepath"
	"testing"
)

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

// FuzzReferenceCheck checks that the SKILL.md body walk never panics on
// untrusted markdown.
func FuzzReferenceCheck(f *testing.F) {
	for _, seed := range []string{
		"[a](b.md) ![i](x.png) [r][x]\n\n[x]: ../y.md\n",
		"`./scripts/x.sh` `../a/b`\n",
		"```bash\ncd a && ./x.sh $(./y) `z` 'q' \"r\" \\\n  w # c\n```\n",
		"```console\n$ ./x\noutput\n```\n\n    ./indented\n",
		"[](<a b>) [x](#f) [y](https://e) [z](f.md:1:2)\n",
	} {
		f.Add([]byte(seed))
	}

	dir := filepath.Join(f.TempDir(), "sk")
	f.Fuzz(func(_ *testing.T, body []byte) {
		_ = newRefChecker(dir, dir).checkReferences(body, 1)
	})
}
