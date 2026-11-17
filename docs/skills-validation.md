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
