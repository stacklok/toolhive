# Triage Graph: stacklok/toolhive

## Repository Overview
- **Name**: stacklok/toolhive
- **Stars**: 1787
- **Description**: Enterprise-grade platform for running and managing Model Context Protocol (MCP) servers
- **Language**: Go
- **License**: Apache 2.0
- **AI Policy**: Accepts AI-generated code with explicit review requirement (CONTRIBUTING.md line 105-109)

## Issue Scan Results

### Total Open Issues: 101
- Bugs: 27 unassigned
- Good first issues: 8 (3 assigned, 5 available)
- Enhancement requests: ~60
- Documentation: 2

### Selected Issue: #4296

**Title**: Audit events logged at non-standard level INFO+2, breaking level detection in log pipelines

**Type**: Bug (audit, logging, vmcp)

**Severity**: SHOULD FIX (breaks operational observability)

**Selection Rationale**:
1. Clear bug with well-defined impact
2. Small scope (single function change)
3. No competing PRs
4. Affects production observability (audit events invisible in log aggregators)
5. Maintainer-provided fix guidance (Option 1 vs Option 2 analysis in comment)
6. Perfect TDD candidate

**Issue Analysis**:
- Root cause: `LevelAudit = slog.Level(2)` renders as `"INFO+2"` in JSON output
- Impact: Loki, Elasticsearch, Splunk cannot filter/alert on audit events by level
- User-visible symptoms: Audit events appear as `detected_level=unknown` in Grafana
- Maintainer consensus: Option 2 (named AUDIT level via ReplaceAttr) preserves semantic distinction

**Fix Location**: `pkg/audit/auditor.go:51-62` (NewAuditLogger function)

**Implementation**:
- Added ReplaceAttr function to slog.HandlerOptions
- Maps `LevelAudit` (numeric 2) → `"AUDIT"` (string) in JSON output
- Preserves numeric level for internal filtering
- 10 lines production code + 51 lines tests

**Test Strategy**:
- TDD approach: wrote failing test first
- Test 1: Verify AUDIT level renders as `"level":"AUDIT"` not `"INFO+2"`
- Test 2: Verify standard levels (INFO, WARN) unaffected
- All existing audit tests pass (28 test cases, 0.379s)

**Files Changed**:
- `pkg/audit/auditor.go`: +10 lines (ReplaceAttr implementation)
- `pkg/audit/auditor_test.go`: +51 lines (new test + slog import)

**Commit**: 71ae4749 (DCO signed-off per CONTRIBUTING.md requirement)

**Status**: triaged, ready for drip (qa_passed branch status when quality gates run)

## Repository Characteristics

### Contributing Model
- DCO sign-off required (all commits)
- Issue claiming workflow (comment + wait for assignment)
- PR size limit: 1000 lines (hard gate, reviewers can block)
- Test requirements: unit tests + e2e for features
- Code quality: authors responsible for reviewing AI-generated code
- Branch policy: feature branches, squash on merge

### Maintainer Activity
- Fast response on good first issues (assigned within hours)
- Active issue grooming (labels, assignment, triage)
- Design notes in issue comments (e.g. #4296 Option 1 vs 2 analysis, #4969 full design doc)
- Multiple active maintainers (@ChrisJBurns, @danbarr, @jerm-dro, @JAORMX)

### Issue Quality Patterns
- **High quality**: Detailed AC, design options, implementation notes (e.g. #4296, #4969)
- **Low quality**: Vague feature requests without context
- **Good first issue** signal is reliable (maintainer-curated, scoped appropriately)

### Other Candidates Considered

#### #4618: References printcolumn shows raw JSON
- Good first issue, but claimed 2026-05-11 (today)
- Requires CRD regeneration (broader testing surface)
- Deferred in favor of #4296 (smaller, unclaimed)

#### #4736: LocalStore path traversal validation
- Security bug, unassigned
- Someone already commented "I'd like to work on this"
- Implicit claim → skip to avoid collision

#### #4297: CORS support for proxy endpoint
- Clear bug, but involves HTTP middleware + CORS options
- Larger scope than #4296 (cross-origin policy decisions)
- Better as second PR after establishing credibility

## Hypothesis Graph Updates

### H0: Boring fixes on solo-maintainer repos have highest merge rate
- **Prediction**: stacklok/toolhive has 4+ active maintainers → slower merge than solo repos
- **Evidence trajectory**: TBD (PR not opened yet)
- **Confidence**: LOW (toolhive is corporate-backed, not solo-maintained)

### H2: Good first issue label is a credence test, not a fix signal
- **Prediction**: #4296 lacks "good first issue" label → maintainers see as non-trivial
- **Evidence trajectory**: CONTRARY (maintainer provided detailed fix guidance in comments)
- **Confidence**: MEDIUM (this is a counterexample to H2)

### H5: Test-first PRs merge faster than fix-first
- **Prediction**: TDD approach (#4296) should accelerate review
- **Evidence trajectory**: TBD
- **Confidence**: MEDIUM (aligns with maintainer code quality expectations)

### New Hypothesis: H7 (Corporate OSS teams batch-review PRs)
- **Observation**: toolhive has dedicated team, PR review SLA unclear from history
- **Prediction**: External PRs may queue behind internal work
- **Test**: Track review latency on #4296 vs good-first-issue PRs
- **Confidence**: LOW (insufficient data)

## Next Steps

1. ~~Scan issues~~ ✅
2. ~~Select smallest bug~~ ✅ (#4296)
3. ~~Implement TDD fix~~ ✅
4. ~~Run full test suite~~ ✅ (0 regressions)
5. ~~Commit with DCO~~ ✅
6. **DO NOT create PR** (per user instruction: drip queue only, no gh pr create)
7. Write drip queue entry ✅
8. Write TRIAGE_GRAPH.md ✅

## AI Policy Compliance

CONTRIBUTING.md line 105-109:
> Pull request authors are responsible for:
> - Reviewing all submitted code, regardless of whether it's AI-generated or hand-written.

**Compliance**: Code reviewed by human (June) before commit. All logic hand-verified against issue requirements and test output.

## Quality Gates Checklist

- [x] DCO sign-off on commit
- [x] Tests added (2 test cases)
- [x] All existing tests pass (28 tests in pkg/audit)
- [x] PR size < 1000 lines (61 lines changed)
- [x] Code reviewed by human
- [ ] CI passes (pending push)
- [ ] Maintainer review (pending PR creation)
