#!/usr/bin/env python3
"""Collect and rank flaky tests from GitHub Actions on main."""

import json
import re
import subprocess
import sys
from collections import defaultdict
from concurrent.futures import ThreadPoolExecutor, as_completed

REPO = "stacklok/toolhive"
WORKFLOW_NAME = "Main build"
PER_PAGE = 100
MAX_PAGES = 3  # Pages of all push-triggered workflow runs (not just Main build)


def gh_api(endpoint):
    """Call gh api and return parsed JSON."""
    result = subprocess.run(
        ["gh", "api", endpoint],
        capture_output=True, text=True, check=True,
    )
    return json.loads(result.stdout)


def fetch_all_runs():
    """Fetch workflow runs across multiple pages."""
    all_runs = []
    for page in range(1, MAX_PAGES + 1):
        data = gh_api(
            f"repos/{REPO}/actions/runs?branch=main&event=push"
            f"&per_page={PER_PAGE}&page={page}"
        )
        runs = [r for r in data["workflow_runs"] if r["name"] == WORKFLOW_NAME]
        all_runs.extend(runs)
        if len(data["workflow_runs"]) < PER_PAGE:
            break  # No more pages
        print(f"Fetched page {page}: {len(runs)} Main build runs", file=sys.stderr)
    return all_runs


def get_failed_logs(run_id):
    """Get failed job logs for a run."""
    result = subprocess.run(
        ["gh", "run", "view", str(run_id), "--repo", REPO, "--log-failed"],
        capture_output=True, text=True,
    )
    return result.stdout + result.stderr


def strip_ansi(text):
    """Remove ANSI escape sequences."""
    return re.sub(r'\x1b\[[0-9;]*m', '', text)


def extract_ginkgo_failures(log_lines):
    """Extract Ginkgo test names from [FAIL] lines."""
    failures = []
    for line in log_lines:
        if '[FAIL]' not in line:
            continue
        clean = strip_ansi(line)
        # Also strip literal ANSI-like codes that gh outputs as text
        clean = re.sub(r'\[\d+;\d+m', '', clean)
        clean = re.sub(r'\[0m', '', clean)
        match = re.search(r'\[FAIL\]\s+(.*?\[It\]\s+[^\[]+)', clean)
        if match:
            test_name = match.group(1).strip()
            failures.append(test_name)
    return failures


def extract_unit_test_failures(log_lines):
    """Extract Go unit test names from ❌ lines."""
    failures = []
    for line in log_lines:
        if '❌' not in line:
            continue
        clean = strip_ansi(line)
        clean = re.sub(r'\[\d+;\d+m', '', clean)
        clean = re.sub(r'\[0m', '', clean)
        match = re.search(r'❌\s+(\S+)', clean)
        if match:
            test_name = match.group(1).strip()
            failures.append(test_name)
    return failures


def extract_job_name(line):
    """Extract job name from log line prefix."""
    match = re.match(r'^(.+?)\t', line)
    return match.group(1).strip() if match else "unknown"


def extract_failure_mode(log_text):
    """Determine failure mode from log content."""
    clean = strip_ansi(log_text)
    # Also strip literal ANSI-like codes
    clean = re.sub(r'\[\d+;\d+m', '', clean)
    clean = re.sub(r'\[0m', '', clean)
    if re.search(r'Timed out after [\d.]+s', clean):
        match = re.search(r'Timed out after ([\d.]+)s', clean)
        return f"timeout ({match.group(1)}s)" if match else "timeout"
    if 'Server should be running' in clean:
        return "server startup timeout"
    if 'panic:' in clean:
        return "panic"
    if 'connection refused' in clean.lower():
        return "connection refused"
    if 'Expected' in clean and 'to equal' in clean:
        return "assertion"
    return "assertion"


def find_failure_context(log_lines, test_name, fail_line_idx):
    """Find the [FAILED] block associated with a test near its [FAIL] summary line.

    Ginkgo logs have two relevant markers:
    - [FAILED] with the failure reason (e.g., "Timed out after 120s") — appears
      in the failure block, potentially thousands of lines before the summary
    - [FAIL] with the test name — appears in the summary section at the end

    Search backwards from the [FAIL] line for the nearest [FAILED] block that
    belongs to this test, then extract context around it.
    """
    # Search backwards from the fail summary line for [FAILED].
    # Ginkgo emits multiple [FAILED] lines per test failure — the first has
    # the reason (e.g., "Timed out after 120s"), later ones are summaries.
    # Collect all [FAILED] lines in the block and return context around them.
    search_start = max(0, fail_line_idx - 5000)
    failed_lines = []
    for i in range(fail_line_idx, search_start, -1):
        clean_line = strip_ansi(log_lines[i])
        if '[FAILED]' in clean_line:
            failed_lines.append(i)
    if failed_lines:
        # Use the earliest (first) [FAILED] line — it has the failure reason
        earliest = min(failed_lines)
        latest = max(failed_lines)
        start = max(0, earliest - 5)
        end = min(len(log_lines), latest + 5)
        return "\n".join(log_lines[start:end])
    # Fallback: use lines around the [FAIL] summary
    start = max(0, fail_line_idx - 50)
    return "\n".join(log_lines[start:fail_line_idx + 1])


def main():
    # Fetch all recent runs on main (paginated)
    all_runs = fetch_all_runs()
    failed_runs = [r for r in all_runs if r["conclusion"] == "failure"]
    success_runs = [r for r in all_runs if r["conclusion"] == "success"]

    total = len(all_runs)
    num_failed = len(failed_runs)

    print(f"=== FLAKE REPORT ===")
    print(f"Total Main build runs on main: {total}")
    print(f"Failed: {num_failed}")
    print(f"Succeeded: {len(success_runs)}")
    print(f"Failure rate: {num_failed/total*100:.1f}%" if total > 0 else "N/A")
    if all_runs:
        dates = sorted(r["created_at"][:10] for r in all_runs)
        print(f"Period: {dates[0]} to {dates[-1]}")
    print()

    # Collect failures from each run — fetch logs in parallel
    test_failures = defaultdict(list)  # test_name -> [{run_id, date, job, mode}]

    def process_run(run):
        """Fetch logs and extract failures for a single run."""
        run_id = run["id"]
        run_date = run["created_at"][:10]
        run_title = run["display_title"]
        print(f"Fetching logs for run {run_id} ({run_date}: {run_title[:60]})...",
              file=sys.stderr)

        log_text = get_failed_logs(run_id)
        log_lines = log_text.splitlines()

        results = []

        # Extract Ginkgo failures
        ginkgo_fails = extract_ginkgo_failures(log_lines)
        for test_name in ginkgo_fails:
            job = "unknown"
            fail_line_idx = None
            for i, line in enumerate(log_lines):
                if '[FAIL]' in line and test_name.split('[It]')[0].strip()[:20] in strip_ansi(line):
                    job = extract_job_name(line)
                    fail_line_idx = i
                    break
            # Find the [FAILED] block for this test to get accurate failure mode
            if fail_line_idx is not None:
                test_log = find_failure_context(log_lines, test_name, fail_line_idx)
            else:
                test_log = log_text
            mode = extract_failure_mode(test_log)
            results.append((test_name, {
                "run_id": run_id, "date": run_date, "job": job, "mode": mode,
            }))

        # Extract unit test failures
        unit_fails = extract_unit_test_failures(log_lines)
        for test_name in unit_fails:
            if '/' in test_name:
                parent = test_name.split('/')[0]
                if parent in unit_fails:
                    continue
            job = "unknown"
            fail_line_idx = None
            for i, line in enumerate(log_lines):
                if '❌' in line and test_name in line:
                    job = extract_job_name(line)
                    fail_line_idx = i
                    break
            # Extract per-test log context (50 lines before the ❌ line)
            if fail_line_idx is not None:
                start = max(0, fail_line_idx - 50)
                test_log = "\n".join(log_lines[start:fail_line_idx + 1])
            else:
                test_log = log_text
            mode = extract_failure_mode(test_log)
            results.append((test_name, {
                "run_id": run_id, "date": run_date, "job": job, "mode": mode,
            }))

        # Infra-only failures
        if not ginkgo_fails and not unit_fails:
            results.append(("[INFRA] " + run_title[:80], {
                "run_id": run_id, "date": run_date, "job": "infra", "mode": "infra",
            }))

        return results

    with ThreadPoolExecutor(max_workers=8) as pool:
        futures = {pool.submit(process_run, run): run for run in failed_runs}
        for future in as_completed(futures):
            run = futures[future]
            try:
                for test_name, occurrence in future.result():
                    test_failures[test_name].append(occurrence)
            except Exception as e:
                print(f"Warning: failed to process run {run['id']}: {e}",
                      file=sys.stderr)

    # Sort by failure count descending
    ranked = sorted(test_failures.items(), key=lambda x: -len(x[1]))

    # Print ranked table
    print()
    print("=== RANKED FAILURES ===")
    print(f"{'Rank':<5} {'Count':<6} {'Job':<45} {'Mode':<25} {'Test'}")
    print("-" * 140)
    for i, (test_name, occurrences) in enumerate(ranked, 1):
        job = occurrences[0]["job"]
        mode = occurrences[0]["mode"]
        count = len(occurrences)
        print(f"{i:<5} {count:<6} {job:<45} {mode:<25} {test_name}")

    # Print details per failure
    print()
    print("=== FAILURE DETAILS ===")
    for test_name, occurrences in ranked:
        print(f"\n## {test_name}")
        print(f"   Failures: {len(occurrences)}/{total} runs")
        for occ in occurrences:
            url = f"https://github.com/{REPO}/actions/runs/{occ['run_id']}"
            print(f"   - {occ['date']} | {occ['mode']} | {occ['job']} | {url}")


if __name__ == "__main__":
    main()
