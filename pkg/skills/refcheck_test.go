// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/yuin/goldmark/ast"
)

// refFrontmatter is the SKILL.md frontmatter used by reference tests; the
// body starts on line 5.
const refFrontmatter = "---\nname: sk\ndescription: d\n---\n"

// refHit is the part of a reference finding the tests compare.
type refHit struct {
	Rule string
	File string
	Line int
}

// writeTree writes files (slash-separated path -> content) under dir.
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o750))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	}
}

// refHits validates dir with opts and returns its reference findings.
func refHits(t *testing.T, dir string, opts ...ValidateOption) []refHit {
	t.Helper()
	result, err := ValidateSkillDir(dir, opts...)
	require.NoError(t, err)
	require.True(t, result.Valid, "errors: %v", result.Errors)
	var hits []refHit
	for _, f := range result.Findings {
		switch f.Rule {
		case RuleRefUnresolved, RuleRefEscapesRoot, RuleScriptDirectExec, RuleRefFindingsTruncated:
			assert.Equal(t, SeverityWarning, f.Severity)
			assert.NotEmpty(t, f.Message)
			hits = append(hits, refHit{f.Rule, f.File, f.Line})
		}
	}
	return hits
}

func TestValidateSkillDir_References(t *testing.T) {
	t.Parallel()

	// Files present in every case besides SKILL.md.
	base := map[string]string{
		"scripts/x.sh":    "#!/bin/sh\n",
		"scripts/x.py":    "print(1)\n",
		"scripts/run":     "",
		"ref.md":          "# Ref\n",
		"my file.md":      "# Spaced\n",
		"assets/logo.png": "png",
	}

	tests := []struct {
		name    string
		body    string            // SKILL.md body, starting on line 5
		files   map[string]string // extra files
		outside map[string]string // files next to the skill directory
		want    []refHit
	}{
		// ref-unresolved
		{name: "existing link", body: "[r](ref.md)\n"},
		{name: "existing directory link", body: "[s](scripts/)\n"},
		{name: "existing image", body: "![l](assets/logo.png)\n"},
		{name: "percent-encoded link", body: "[f](my%20file.md)\n"},
		{name: "fragment and line suffix stripped", body: "[a](ref.md#top) [b](ref.md:12) [c](ref.md?x=1)\n"},
		{
			name: "missing link",
			body: "intro\n\n[m](missing.md)\n",
			want: []refHit{{RuleRefUnresolved, "SKILL.md", 7}},
		},
		{
			name: "missing image",
			body: "![m](img/none.png)\n",
			want: []refHit{{RuleRefUnresolved, "SKILL.md", 5}},
		},
		{
			name: "missing link with fragment",
			body: "[m](missing.md#sec)\n",
			want: []refHit{{RuleRefUnresolved, "SKILL.md", 5}},
		},
		{
			name: "empty link text uses block line",
			body: "\n[](missing.md)\n",
			want: []refHit{{RuleRefUnresolved, "SKILL.md", 6}},
		},
		{
			name: "reference definition reported once at its line",
			body: "[a][x] and [x] and [b][x]\n\n[x]: missing.md\n",
			want: []refHit{{RuleRefUnresolved, "SKILL.md", 7}},
		},
		{
			name: "unused reference definition",
			body: "text\n\n[unused]: gone.md\n",
			want: []refHit{{RuleRefUnresolved, "SKILL.md", 7}},
		},
		{
			name: "each inline occurrence reported",
			body: "[a](gone.md)\n\n[b](gone.md)\n",
			want: []refHit{{RuleRefUnresolved, "SKILL.md", 5}, {RuleRefUnresolved, "SKILL.md", 7}},
		},

		// ref-escapes-root
		{
			name: "link escapes",
			body: "[s](../shared/x.md)\n",
			want: []refHit{{RuleRefEscapesRoot, "SKILL.md", 5}},
		},
		{
			name:    "code span escapes",
			body:    "see `../shared/x.md`\n",
			outside: map[string]string{"shared/x.md": ""},
			want:    []refHit{{RuleRefEscapesRoot, "SKILL.md", 5}},
		},
		{name: "link staying inside", body: "[r](scripts/../ref.md)\n"},
		{name: "code span without slash", body: "go up with `..`\n"},
		{name: "code span staying inside", body: "`scripts/../ref.md`\n"},
		{
			name:    "fence argument and flag value escape",
			body:    "```bash\ncat ../a.md\nrun --file=../b.md\n```\n",
			outside: map[string]string{"a.md": "", "b.md": ""},
			want:    []refHit{{RuleRefEscapesRoot, "SKILL.md", 6}, {RuleRefEscapesRoot, "SKILL.md", 7}},
		},
		{
			name:    "non-shell fence still checked for escapes",
			body:    "```yaml\npath: ../shared/x.md\n```\n",
			outside: map[string]string{"shared/x.md": ""},
			want:    []refHit{{RuleRefEscapesRoot, "SKILL.md", 6}},
		},
		{
			name: "code paths about the user's project are not reported",
			body: "```ts\nimport { foo } from '../lib/foo'\n```\n\nRun `cd ../ && npm test`.\n\n" +
				"```bash\ngit diff ../other-repo\ncd .. && cd ../..\n```\n",
		},

		// script-direct-exec
		{
			name: "direct exec in bash fence",
			body: "```bash\n./scripts/x.sh\n```\n",
			want: []refHit{{RuleScriptDirectExec, "SKILL.md", 6}},
		},
		{
			name: "direct exec without dot slash",
			body: "```sh\nscripts/x.sh --flag\n```\n",
			want: []refHit{{RuleScriptDirectExec, "SKILL.md", 6}},
		},
		{
			name: "direct exec after operators and prefixes",
			body: "```\ncd a && ./scripts/x.sh\necho $(./scripts/x.sh)\nFOO=1 sudo -E ./scripts/x.sh | tee\n```\n",
			want: []refHit{
				{RuleScriptDirectExec, "SKILL.md", 6},
				{RuleScriptDirectExec, "SKILL.md", 7},
				{RuleScriptDirectExec, "SKILL.md", 8},
			},
		},
		{
			name: "console prompt lines only",
			body: "```console\n$ ./scripts/x.sh\n./scripts/x.sh\n```\n",
			want: []refHit{{RuleScriptDirectExec, "SKILL.md", 6}},
		},
		{
			name: "indented code block",
			body: "text\n\n    ./scripts/x.sh\n",
			want: []refHit{{RuleScriptDirectExec, "SKILL.md", 7}},
		},
		{
			name: "code span with dot slash",
			body: "Run `./scripts/x.sh` now.\n",
			want: []refHit{{RuleScriptDirectExec, "SKILL.md", 5}},
		},
		{name: "code span naming a file", body: "The helper is `scripts/x.sh`.\n"},
		{name: "interpreter", body: "```bash\nbash scripts/x.sh\npython3 scripts/x.py\n```\n"},
		{name: "non-shell fence", body: "```python\n./scripts/x.sh\n```\n"},
		{
			name: "fence language is case-insensitive and ignores attributes",
			body: "```Bash {.numberLines}\n./scripts/x.sh\n```\n",
			want: []refHit{{RuleScriptDirectExec, "SKILL.md", 6}},
		},
		{name: "missing script is not reported", body: "```bash\n./configure && ./gradlew build\n```\n"},
		{name: "continued line is an argument", body: "```bash\nrun \\\n  ./scripts/x.sh\n```\n"},
		{name: "comment", body: "```bash\n# ./scripts/x.sh\necho hi # ./scripts/x.sh\n```\n"},
		{name: "quoted argument", body: "```bash\necho './scripts/x.sh'\n```\n"},
		{name: "untagged listing without dot slash", body: "```\nscripts/x.sh\nref.md\n| scripts/x.sh | Runs X |\n```\n"},
		{name: "document is not a script", body: "```bash\n./ref.md\nassets/logo.png\n```\n"},
		{name: "code span in link text", body: "See [`./scripts/x.sh`](scripts/x.sh).\n"},
		{name: "command -v only looks up", body: "```bash\ncommand -v ./scripts/x.sh\ncommand -V ./scripts/x.sh\n```\n"},
		{
			name: "file without extension",
			body: "```bash\n./scripts/run\n```\n",
			want: []refHit{{RuleScriptDirectExec, "SKILL.md", 6}},
		},
		{
			name: "substitution inside double quotes",
			body: "```bash\nOUT=\"$(./scripts/x.sh)\"\necho \"`./scripts/x.py`\"\n```\n",
			want: []refHit{{RuleScriptDirectExec, "SKILL.md", 6}, {RuleScriptDirectExec, "SKILL.md", 7}},
		},
		{
			name: "prompt in bash fence",
			body: "```bash\n$ ./scripts/x.sh\n```\n",
			want: []refHit{{RuleScriptDirectExec, "SKILL.md", 6}},
		},
		{
			name: "continuation on the last line",
			body: "```bash\n./scripts/x.sh \\\n```\n",
			want: []refHit{{RuleScriptDirectExec, "SKILL.md", 6}},
		},
		{
			name: "escaped backslash and comment do not continue",
			body: "```bash\necho a\\\\\n./scripts/x.sh\n# note \\\n./scripts/x.py\n```\n",
			want: []refHit{{RuleScriptDirectExec, "SKILL.md", 7}, {RuleScriptDirectExec, "SKILL.md", 9}},
		},
		{
			name:    "code span in link reports the link only",
			body:    "[`../shared/x.md`](../shared/x.md)\n",
			outside: map[string]string{"shared/x.md": ""},
			want:    []refHit{{RuleRefEscapesRoot, "SKILL.md", 5}},
		},
		{
			name: "code paths far above the skill are not stat'ed",
			body: "```bash\ncat ../../../../../../../../../../../../../../etc/hosts\n```\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := makeSkillDir(t, "sk", refFrontmatter+tt.body)
			writeTree(t, dir, base)
			writeTree(t, dir, tt.files)
			writeTree(t, filepath.Dir(dir), tt.outside)
			assert.Equal(t, tt.want, refHits(t, dir))
		})
	}
}

func TestValidateSkillDir_ReferenceSkipRules(t *testing.T) {
	t.Parallel()

	// Each link would otherwise be unresolved or escaping.
	targets := []string{
		"https://example.com/../x.md",
		"mailto:a@b.c",
		"#anchor",
		"<placeholder>/../x.md",
		"${VAR}/../x.md",
		"$VAR/../x.md",
		"{{ dir }}/../x.md",
		"../*.md",
		"../[ab].md",
		"/abs/../x.md",
		"~/x/../../y.md",
		"C:/x/../../y.md",
	}

	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			dir := makeSkillDir(t, "sk", refFrontmatter+fmt.Sprintf("[l](%s)\n", target))
			assert.Empty(t, refHits(t, dir))
		})
	}
}

func TestValidateSkillDir_LinkedMarkdown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		body  string
		files map[string]string
		want  []refHit
	}{
		{
			name: "linked file is scanned relative to itself",
			body: "[a](docs/a.md)\n",
			files: map[string]string{
				"docs/a.md":    "# A\n\n[b](b.md) [gone](gone.md)\n",
				"docs/b.md":    "# B\n",
				"scripts/x.sh": "",
			},
			want: []refHit{{RuleRefUnresolved, "docs/a.md", 3}},
		},
		{
			name: "linked file lines count its frontmatter",
			body: "[a](a.md)\n",
			files: map[string]string{
				"a.md": "---\ntitle: a\n---\n\n[gone](gone.md)\n",
			},
			want: []refHit{{RuleRefUnresolved, "a.md", 5}},
		},
		{
			name: "cycle is scanned once",
			body: "[a](a.md)\n",
			files: map[string]string{
				"a.md": "[b](b.md) [gone](gone.md)\n",
				"b.md": "[a](a.md) [skill](SKILL.md)\n",
			},
			want: []refHit{{RuleRefUnresolved, "a.md", 1}},
		},
		{
			name: "code in linked file resolves against skill dir or its own dir",
			body: "[a](docs/a.md)\n",
			files: map[string]string{
				"docs/a.md":    "```bash\n./scripts/x.sh\ncat ../ref.md\ncat ../../out.md\n```\n",
				"scripts/x.sh": "",
				"ref.md":       "",
				"../out.md":    "",
			},
			want: []refHit{
				{RuleScriptDirectExec, "docs/a.md", 2},
				{RuleRefEscapesRoot, "docs/a.md", 4},
			},
		},
		{
			name: "non-markdown targets are not scanned",
			body: "[c](conf.txt)\n",
			files: map[string]string{
				"conf.txt": "[gone](gone.md)\n",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := makeSkillDir(t, "sk", refFrontmatter+tt.body)
			writeTree(t, dir, tt.files)
			assert.Equal(t, tt.want, refHits(t, dir))
		})
	}
}

func TestValidateSkillDir_ReferenceLimits(t *testing.T) {
	t.Parallel()

	t.Run("finding cap", func(t *testing.T) {
		t.Parallel()
		// Past the cap, code is no longer checked either.
		body := strings.Repeat("[x](gone.md)\n\n", maxRefFindings+5) + "```bash\n./scripts/x.sh\n```\n"
		dir := makeSkillDir(t, "sk", refFrontmatter+body)
		writeTree(t, dir, map[string]string{"scripts/x.sh": ""})
		hits := refHits(t, dir)
		require.Len(t, hits, maxRefFindings+1)
		assert.Equal(t, refHit{RuleRefFindingsTruncated, "SKILL.md", 0}, hits[maxRefFindings])
	})

	t.Run("file cap", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{}
		for i := range maxScannedFiles + 5 {
			files[fmt.Sprintf("f%d.md", i)] = fmt.Sprintf("[next](f%d.md)\n", i+1)
		}
		files[fmt.Sprintf("f%d.md", maxScannedFiles+5)] = ""
		dir := makeSkillDir(t, "sk", refFrontmatter+"[first](f0.md)\n")
		writeTree(t, dir, files)
		assert.Equal(t, []refHit{{RuleRefFindingsTruncated, "SKILL.md", 0}}, refHits(t, dir))
	})

	t.Run("unreadable file is skipped", func(t *testing.T) {
		t.Parallel()
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("file mode 0000 does not block reads on Windows or for root")
		}
		dir := makeSkillDir(t, "sk", refFrontmatter+"[locked](locked.md)\n")
		writeTree(t, dir, map[string]string{"locked.md": "[gone](gone.md)\n"})
		locked := filepath.Join(dir, "locked.md")
		require.NoError(t, os.Chmod(locked, 0o000))
		t.Cleanup(func() { _ = os.Chmod(locked, 0o600) })
		assert.Empty(t, refHits(t, dir))
	})

	t.Run("oversized file is not scanned", func(t *testing.T) {
		t.Parallel()
		dir := makeSkillDir(t, "sk", refFrontmatter+"[big](big.md)\n")
		writeTree(t, dir, map[string]string{
			"big.md": "[gone](gone.md)\n" + strings.Repeat("x", maxScannedFileSize),
		})
		assert.Empty(t, refHits(t, dir))
	})
}

func TestValidateSkillDir_PluginRoot(t *testing.T) {
	t.Parallel()

	pluginDir := filepath.Join(t.TempDir(), "plugin")
	skillDir := filepath.Join(pluginDir, "skills", "sk")
	writeTree(t, pluginDir, map[string]string{
		"skills/sk/SKILL.md": refFrontmatter +
			"[shared](../../shared/x.md)\n\n```bash\n./scripts/x.sh\ncat ../../../outside.md\n```\n\n[gone](gone.md)\n",
		"skills/sk/scripts/x.sh": "",
		"shared/x.md":            "[not scanned](missing.md)\n",
		"../outside.md":          "",
	})

	assert.Equal(t, []refHit{
		{RuleRefEscapesRoot, "skills/sk/SKILL.md", 9},
		{RuleRefUnresolved, "skills/sk/SKILL.md", 12},
	}, refHits(t, skillDir, WithPluginRoot(pluginDir)))

	// Standalone, the same skill reports the escaping link and the direct
	// execution. outside.md is above the skill's grandparent, where code paths
	// are not stat'ed.
	assert.Equal(t, []refHit{
		{RuleRefEscapesRoot, "SKILL.md", 5},
		{RuleScriptDirectExec, "SKILL.md", 8},
		{RuleRefUnresolved, "SKILL.md", 12},
	}, refHits(t, skillDir))
}

func TestValidateSkillDir_PluginRootInvalid(t *testing.T) {
	t.Parallel()

	skillDir := makeSkillDir(t, "sk", refFrontmatter)
	for name, root := range map[string]string{
		"relative":          "plugin",
		"does not contain":  t.TempDir(),
		"inside skill only": filepath.Join(skillDir, "sub"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := ValidateSkillDir(skillDir, WithPluginRoot(root))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "plugin root")
		})
	}
}

func TestRefTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		raw    string
		isLink bool
		want   string
		ok     bool
	}{
		{"ref.md#top", true, "ref.md", true},
		{"ref.md?x=1", true, "ref.md", true},
		{"ref.md:12:3", false, "ref.md", true},
		{"my%20file.md", true, "my file.md", true},
		{"my%20file.md", false, "my%20file.md", true},
		{"../x?.md", false, "", false},
		{"", false, "", false},
	}
	// Skipped in both modes.
	for _, raw := range []string{
		"https://example.com/../x.md", "mailto:a@b.c", "#anchor", "<placeholder>/../x.md",
		"${VAR}/../x.md", "$VAR/../x.md", "{{ dir }}/../x.md", "../*.md", "../[ab].md",
		"/abs/../x.md", "~/x/../../y.md", "C:/x/../../y.md",
	} {
		tests = append(tests, struct {
			raw    string
			isLink bool
			want   string
			ok     bool
		}{raw, true, "", false}, struct {
			raw    string
			isLink bool
			want   string
			ok     bool
		}{raw, false, "", false})
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s/link=%t", tt.raw, tt.isLink), func(t *testing.T) {
			t.Parallel()
			got, ok := refTarget(tt.raw, tt.isLink)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestScriptInterpreter(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"env.txt":  "#!/usr/bin/env -S python3 -u\n",
		"sh.txt":   "#!/bin/sh\n",
		"bare.txt": "#!\n",
		"a.py":     "print(1)\n",
		"noext":    "",
		"doc.md":   "# Doc\n",
		"sub/x":    "",
	})
	tests := []struct {
		file   string
		interp string
		ok     bool
	}{
		{"env.txt", "python3", true},
		{"sh.txt", "sh", true},
		{"bare.txt", "", true},
		{"a.py", "python3", true},
		{"noext", "", true},
		{"doc.md", "", false},
		{"sub", "", false},
		{"missing", "", false},
	}
	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		// An unreadable script has no readable shebang; its extension decides.
		writeTree(t, dir, map[string]string{"locked.sh": "#!/usr/bin/env python3\n"})
		require.NoError(t, os.Chmod(filepath.Join(dir, "locked.sh"), 0o000))
		tests = append(tests, struct {
			file   string
			interp string
			ok     bool
		}{"locked.sh", "bash", true})
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			t.Parallel()
			interp, ok := scriptInterpreter(filepath.Join(dir, tt.file))
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.interp, interp)
		})
	}
}

func TestRefPositionFallbacks(t *testing.T) {
	t.Parallel()

	// A node outside any document has no text and no enclosing block, and a
	// block without lines has no position: both report an unknown offset,
	// which maps to line 0.
	assert.Equal(t, -1, inlineOffset(ast.NewLink()))
	assert.Equal(t, -1, blockOffset(ast.NewParagraph()))
	assert.Equal(t, 0, newLineIndex([]byte("a\nb\n"), 5).at(-1))
	assert.Equal(t, 6, newLineIndex([]byte("a\nb\n"), 5).at(2))
}

func TestShellCommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		line     string
		wantCmds [][]string
		wantExec []string
	}{
		{"./a.sh x", [][]string{{"./a.sh", "x"}}, []string{"./a.sh"}},
		{"a && b || c; d | e & f", [][]string{{"a"}, {"b"}, {"c"}, {"d"}, {"e"}, {"f"}}, []string{"a", "b", "c", "d", "e", "f"}},
		{"x $(y z) `w`", [][]string{{"x"}, {"y", "z"}, {"w"}}, []string{"x", "y", "w"}},
		{`echo 'a b' "c \" d" e\ f`, [][]string{{"echo", "a b", `c " d`, "e f"}}, []string{"echo"}},
		{"A=1 B=2 env -i nohup ./run", [][]string{{"A=1", "B=2", "env", "-i", "nohup", "./run"}}, []string{"./run"}},
		{"if ./t; then ./u; fi", [][]string{{"if", "./t"}, {"then", "./u"}, {"fi"}}, []string{"./t", "./u", "fi"}},
		{"cat ${DIR}/x # comment", [][]string{{"cat", "${DIR}/x"}}, []string{"cat"}},
		{"A=1", [][]string{{"A=1"}}, []string{""}},
		{`OUT="$(./a)"`, [][]string{{"OUT="}, {"./a"}}, []string{"", "./a"}},
		{"echo \"`./b` c\" d", [][]string{{"echo"}, {"./b"}, {" c", "d"}}, []string{"echo", "./b", " c"}},
		{"command -v ./x", [][]string{{"command", "-v", "./x"}}, []string{""}},
		{"", nil, nil},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			t.Parallel()
			cmds := shellCommands(tt.line)
			assert.Equal(t, tt.wantCmds, cmds)
			var exec []string
			for _, words := range cmds {
				exec = append(exec, commandWord(words))
			}
			assert.Equal(t, tt.wantExec, exec)
		})
	}
}
