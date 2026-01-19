# Contributing to ToolHive <!-- omit from toc -->

First off, thank you for taking the time to contribute to ToolHive! :+1: :tada:
ToolHive is released under the Apache 2.0 license. If you would like to
contribute something or want to hack on the code, this document should help you
get started. You can find some hints for starting development in ToolHive's
[README](https://github.com/stacklok/toolhive/blob/main/README.md).

## Table of contents <!-- omit from toc -->

- [Code of conduct](#code-of-conduct)
- [Reporting security vulnerabilities](#reporting-security-vulnerabilities)
- [How to contribute](#how-to-contribute)
  - [Using GitHub Issues](#using-github-issues)
  - [Not sure how to start contributing?](#not-sure-how-to-start-contributing)
  - [Claiming an issue](#claiming-an-issue)
  - [What to expect](#what-to-expect)
  - [Pull request process](#pull-request-process)
  - [Contributing to docs](#contributing-to-docs)
  - [Contributing to design proposals](#contributing-to-design-proposals)
  - [Commit message guidelines](#commit-message-guidelines)

## Code of conduct

This project adheres to the
[Contributor Covenant](https://github.com/stacklok/toolhive/blob/main/CODE_OF_CONDUCT.md)
code of conduct. By participating, you are expected to uphold this code. Please
report unacceptable behavior to
[code-of-conduct@stacklok.dev](mailto:code-of-conduct@stacklok.dev).

## Reporting security vulnerabilities

If you think you have found a security vulnerability in ToolHive please DO NOT
disclose it publicly until we've had a chance to fix it. Please don't report
security vulnerabilities using GitHub issues; instead, please follow this
[process](https://github.com/stacklok/toolhive/blob/main/SECURITY.MD)

## How to contribute

### Using GitHub Issues

We use GitHub issues to track bugs and enhancements. If you have a general usage
question, please ask in
[ToolHive's discussion forum](https://discord.gg/stacklok).

If you are reporting a bug, please help to speed up problem diagnosis by
providing as much information as possible. Ideally, that would include a small
sample project that reproduces the problem.

### Not sure how to start contributing?

PRs to resolve existing issues are greatly appreciated, and issues labeled as
["good first issue"](https://github.com/stacklok/toolhive/issues?q=is%3Aopen+is%3Aissue+label%3A%22good+first+issue%22)
are a great place to start!

### Claiming an issue

If you'd like to work on an existing issue:

1. Leave a comment saying "I'd like to work on this"
2. Wait for a team member to assign you before starting work

This helps us avoid situations where multiple people work on the same thing.
If you create an issue with the intent to implement it yourself, mention that
in the description so we know you're planning to submit a PR.

### What to expect

Reviews of external contributions are on a best effort basis. ToolHive moves
fast and is corporate-backed, so priorities can shift. We may occasionally
need to pick up urgent issues ourselves, but we'll always try to coordinate
with active contributors first.

### Pull request process

- -All commits must include a Signed-off-by trailer at the end of each commit
  message to indicate that the contributor agrees to the Developer Certificate
  of Origin. For additional details, check out the [DCO instructions](dco.md).
- Create an issue outlining the fix or feature.
- Fork the ToolHive repository to your own GitHub account and clone it locally.
- Hack on your changes.
- Correctly format your commit messages, see
  [Commit message guidelines](#commit-message-guidelines) below.
- Open a PR by ensuring the title and its description reflect the content of the
  PR.
- Ensure that CI passes, if it fails, fix the failures.
- Every pull request requires a review from the core ToolHive team before
  merging.
- Once approved, all of your commits will be squashed into a single commit with
  your PR title.

### Testing requirements

- Add end-to-end tests for new features covering both API and CLI flows.
- Write unit tests for new code alongside the source files.

### Code quality expectations

Pull request authors are responsible for:

- Keeping PRs small and focused. PRs exceeding 1000 lines may be blocked and
  require splitting into multiple PRs or logical commits before review. If a
  large PR is unavoidable, include an explanation in the PR description
  justifying the size and describing how the changes are organized for review.
- Reviewing all submitted code, regardless of whether it's AI-generated or
  hand-written.
- Manually testing changes to verify new or existing features work correctly.
- Ensuring coding style guidelines are followed.
- Respecting architecture boundaries and design patterns.

### Contributing to docs

The ToolHive user documentation website is maintained in the
[docs-website](https://github.com/stacklok/docs-website) repository. If you want
to contribute to the documentation, please open a PR in that repo.

Please review the README and
[STYLE-GUIDE](https://github.com/stacklok/docs-website/blob/main/STYLE-GUIDE.md)
in the docs-website repository for more information on how to contribute to the
documentation.

### Contributing to design proposals

Design proposals for ToolHive should be placed in the `docs/proposals/` directory
and follow a specific naming convention to ensure proper organization and tracking.

#### Proposal file naming format

All proposal files must follow this naming pattern:
```
THV-{PR_NUMBER}-{descriptive-name}.md
```

Where:
- `THV-` is the required prefix
- `{PR_NUMBER}` is the pull request number (4 digits)
- `{descriptive-name}` is a descriptive name in kebab-case

#### Examples of valid proposal names:
- `THV-1234-new-feature-proposal.md`
- `THV-5678-api-improvements.md`
- `THV-9012-authentication-enhancement.md`

#### Proposal content guidelines:
- Use clear, descriptive titles
- Include a problem statement at the beginning
- Add examples where applicable
- Consider backward compatibility
- Include migration strategies if needed

The CI system will automatically validate that proposal files follow the correct
naming convention when they are added or modified in pull requests.

### Commit message guidelines

We follow the commit formatting recommendations found on
[Chris Beams' How to Write a Git Commit Message article](https://chris.beams.io/posts/git-commit/):

1. Separate subject from body with a blank line
1. Limit the subject line to 50 characters
1. Capitalize the subject line
1. Do not end the subject line with a period
1. Use the imperative mood in the subject line
1. Use the body to explain what and why vs. how
