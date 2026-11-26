# Skill and plugin validation

`thv skill validate <path>` and `thv ai-plugin validate <path>` check a local
skill or plugin directory before it is built. The same checks back the
`POST /api/v1beta/skills/validate` and `POST /api/v1beta/plugins/validate`
endpoints.

Validation never builds or installs anything. `thv skill build` and
`thv ai-plugin build` do not run these checks; the OCI packager applies its own
structural checks.

## Result format

`--format json` and the API return the same object:

```json
{
  "valid": true,
  "warnings": [
    "allowed-tools uses comma-delimited format, which is a ToolHive extension; the Agent Skills spec defines space-delimited as the canonical format"
  ],
  "findings": [
    {
      "severity": "warning",
      "rule": "allowed-tools-comma",
      "file": "SKILL.md",
      "line": 0,
      "message": "allowed-tools uses comma-delimited format, which is a ToolHive extension; the Agent Skills spec defines space-delimited as the canonical format"
    }
  ]
}
```

| Field | Meaning |
|---|---|
| `valid` | `false` when there is at least one error. The CLI exits non-zero only in that case; warnings never fail validation. |
| `errors`, `warnings` | Human-readable strings, unchanged from earlier releases. Omitted when empty. |
| `findings` | One entry per error or warning, with the fields below. Omitted when empty. |

Each finding has:

| Field | Meaning |
|---|---|
| `severity` | `error` or `warning`. |
| `rule` | Stable ID of the check that fired (see below). |
| `file` | Slash-separated path of the affected file, relative to the validated directory. |
| `line` | 1-based line in `file`, or `0` when the check has no line. |
| `message` | Description of the problem. |

Every `errors` or `warnings` entry corresponds to exactly one finding of the
same severity, in the same order. For a skill bundled in a plugin, `file` is
relative to the plugin directory (for example `skills/foo/SKILL.md`) and the
`errors`/`warnings` string is `bundled skill "<manifest path>": ` followed by
the finding's `message`. Warnings of bundled skills are reported as well as
errors.

## Rules

Rule IDs and the field names above are a public contract: rules may be added
in later releases, but an existing rule ID is never renamed or removed.
Consumers should ignore rule IDs they do not recognize. Skills and plugins
share rule IDs; `file` tells them apart.

| Rule | Severity | Reported when | `file` |
|---|---|---|---|
| `skill-md-missing` | error | The directory has no `SKILL.md`. | `SKILL.md` |
| `frontmatter-invalid` | error | The `SKILL.md` frontmatter is missing or cannot be parsed. | `SKILL.md` |
| `name-required` | error | The skill or plugin has no name. | `SKILL.md` or `.claude-plugin/plugin.json` |
| `name-invalid` | error | The name is not 2–64 lowercase alphanumerics and single hyphens. | `SKILL.md` or `.claude-plugin/plugin.json` |
| `name-dir-mismatch` | error | The name differs from the directory name. | `SKILL.md` or `.claude-plugin/plugin.json` |
| `description-required` | error | The skill has no description. | `SKILL.md` |
| `description-too-long` | error | The description exceeds 1024 characters. | `SKILL.md` |
| `compatibility-too-long` | error | The `compatibility` field exceeds 500 characters. | `SKILL.md` |
| `symlink-not-allowed` | error | The directory contains a symlink. | the symlink |
| `path-traversal` | error | A path inside the directory contains `..`. | the offending path |
| `manifest-invalid` | error | The plugin manifest is missing or invalid. | `.claude-plugin/plugin.json` |
| `bundled-skill-unreadable` | error | A skill listed in the plugin manifest cannot be read. | the bundled skill directory |
| `allowed-tools-comma` | warning | `allowed-tools` is comma-delimited instead of space-delimited. | `SKILL.md` |
| `skill-md-too-long` | warning | `SKILL.md` has more than 500 lines. | `SKILL.md` |
| `ref-unresolved` | warning | A relative link, image, or link reference definition points at a file that does not exist. | the markdown file |
| `ref-escapes-root` | warning | A reference leaves the skill directory (the plugin directory for a bundled skill). | the markdown file |
| `script-direct-exec` | warning | A file shipped in the skill is run directly instead of through an interpreter. Standalone skills only. | the markdown file |
| `ref-findings-truncated` | warning | Reference checking stopped at a limit (see below). | `SKILL.md` |

## Reference checks

The reference rules catch mistakes that pass the other checks but break once
the skill is installed. They are warnings: they never make a skill invalid.

**What is scanned.** `SKILL.md`, then every relative `.md` or `.markdown` file
it links to inside the skill directory, following links from file to file. Each
file is scanned once. Line numbers count from the top of the file, frontmatter
included. Inline links are reported at every occurrence; a link reference
definition is reported once, on its own line.

**`ref-unresolved`** checks link and image destinations and link reference
definitions, resolved against the directory of the file that contains them. A
link to an existing directory counts as resolved.

**`ref-escapes-root`** checks the same destinations, plus inline code spans
that contain `/` (outside link text) and words in fenced or indented code
blocks (in `console` blocks, prompt lines only). A standalone
skill is installed on its own, so `../shared/x.md` does not exist after
install. For a skill bundled in a plugin the whole plugin is installed, so a
reference is only reported when it leaves the plugin directory. Paths in code
are resolved against both the skill directory and the containing file's
directory, and reported only when both leave the root and the path exists
next to the skill (and is not a parent directory such as `..`). Commands run
from the agent's working directory, so a code path that does not exist there,
such as `import '../lib/foo'`, `cd ..` or `git diff ../other-repo`, refers to
the user's project and is not reported. Code paths more than two levels above
the root are not looked up at all. Links are reported whether or not the
target exists.

**`script-direct-exec`** reports a command such as `./scripts/run.sh` (also
after `&&`, `||`, `;`, `|`, `$(`, backticks, including inside double quotes,
variable assignments and `sudo`, `env` and similar wrappers) when the file
exists in the skill and looks like a script: it starts with `#!`, has a script
extension (`.sh`, `.py`, `.js`, ...), or has no extension. Standalone skills
are installed without executable bits, so the command fails; run it through an
interpreter instead (`bash scripts/run.sh`, `python3 scripts/run.py`). The
message suggests the interpreter from the `#!` line or the extension. It is
checked in `bash`, `sh`, `shell` and `zsh` fences for any command, and in
untagged fences, indented code blocks and inline code spans (outside link text)
only for commands that start with `./`, since those often just list or name
files. A leading `$ ` prompt is removed; in `console` blocks only lines starting
with a `$ ` or `# ` prompt are commands. `command -v` and `command -V` only
look a command up and are not reported. The check is skipped for skills
bundled in a plugin, which are installed with executable bits.

**Skipped references.** URLs and other schemes, `#anchors`, `<placeholders>`,
`${VAR}` and `$VAR`, `{{ templates }}`, glob characters (`* ? [ ] { }`),
absolute paths, and paths starting with `~`. A `#fragment`, a `?query` (in
links) and an editor-style `:line` or `:line:col` suffix are removed before a
path is resolved.

**Limits.** At most 100 reference findings and 50 scanned markdown files per
skill; reaching either adds one `ref-findings-truncated` warning. Linked files
over 1 MiB, unreadable files, and symlinks are not scanned.

Command detection is a heuristic, not a shell parser: heredocs and complex
quoting may be misread, and wrapper flags that take an argument
(`sudo -u user ./x.sh`) are not modelled. `chmod +x scripts/run.sh &&
./scripts/run.sh` is reported even though it works once installed; running the
script through an interpreter avoids both the warning and the `chmod`. Because
the rules are warnings, a misread never blocks a build or a validation.
