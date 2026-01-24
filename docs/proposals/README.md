# ToolHive RFCs (Request for Comments)

Design proposals for ToolHive have been moved to a dedicated repository:

**[github.com/stacklok/toolhive-rfcs](https://github.com/stacklok/toolhive-rfcs)**

## Why a separate repository?

- Better visibility and discoverability of design proposals
- Cleaner separation between code and design discussions
- Easier to track and reference RFCs independently
- Serves the entire ToolHive ecosystem (CLI, Studio, Registry, Cloud UI)
- Community members can participate in design discussions without cloning the main codebase

## How to contribute a design proposal

1. Start a thread on [Discord](https://discord.gg/stacklok) to gather initial feedback (optional but recommended)
2. Fork the [toolhive-rfcs](https://github.com/stacklok/toolhive-rfcs) repository
3. Copy `rfcs/0000-template.md` to `rfcs/THV-XXXX-descriptive-name.md` (use the next available PR number)
4. Fill in the RFC template with your proposal
5. Submit a pull request

For detailed guidelines, see the [CONTRIBUTING.md](https://github.com/stacklok/toolhive-rfcs/blob/main/CONTRIBUTING.md) in the toolhive-rfcs repository.

## When to write an RFC

Write an RFC for:
- New features affecting multiple components
- Significant architectural changes
- Changes to public APIs or user-facing behavior
- Security-sensitive changes
- Breaking changes or deprecations

You probably don't need an RFC for:
- Bug fixes
- Documentation improvements
- Minor refactoring or isolated changes

For questions or discussions about RFCs, please use [Discord](https://discord.gg/stacklok) or the GitHub Discussions in the toolhive-rfcs repository.
