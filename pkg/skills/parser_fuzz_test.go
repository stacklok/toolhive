// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
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

// FuzzReferenceCheck checks that the SKILL.md body walk and the shell
// tokenizer never panic on untrusted markdown.
func FuzzReferenceCheck(f *testing.F) {
	for _, seed := range []string{
		"[a](b.md) ![i](x.png) [r][x]\n\n[x]: ../y.md\n",
		"`./scripts/x.sh` `../a/b`\n",
		"```bash\ncd a && ./x.sh $(./y) `z` 'q' \"r\" \\\n  w # c\n```\n",
		"```console\n$ ./x\noutput\n```\n\n    ./indented\n",
		"[](<a b>) [x](#f) [y](https://e) [z](f.md:1:2)\n",
		"```bash\nOUT=\"$(./scripts/x.sh)\"\ncat ../outside.md\n```\n\n[d](doc.md)\n",
		strings.Repeat("`cat ../outside.md`\n\n", maxRefFindings+1),
	} {
		f.Add([]byte(seed))
	}

	// Files the seeds point at, so the fuzzer reaches the finding and cap paths.
	root := f.TempDir()
	dir := filepath.Join(root, "sk")
	for name, content := range map[string]string{
		"sk/scripts/x.sh": "#!/bin/sh\n",
		"sk/doc.md":       "```\n./scripts/x.sh\n```\n",
		"outside.md":      "",
	} {
		p := filepath.Join(root, filepath.FromSlash(name))
		require.NoError(f, os.MkdirAll(filepath.Dir(p), 0o750))
		require.NoError(f, os.WriteFile(p, []byte(content), 0o600))
	}
	f.Fuzz(func(_ *testing.T, body []byte) {
		_ = newRefChecker(dir, dir, true).checkReferences(body, 1)
		_ = shellCommands(string(body))
	})
}
