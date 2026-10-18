# SPDX-License-Identifier: Apache-2.0
"""Run the Go test suite and record the results.

    python3 scripts/record-tests.py            run tests, write test/results/<os>-<arch>.json
    python3 scripts/record-tests.py --annotate also print GitHub annotations (used by CI)
    python3 scripts/record-tests.py --summarize  rebuild test/results/README.md from the JSON files
    python3 scripts/record-tests.py --from-ci RUN_ID  copy each platform's results from a CI run
                                                   (needs gh); then run --summarize

Set RENDERCACHE_TEST_CHROME to include the real-browser tests. Lines that tests
log as "RESULT name=value" are collected as measured results. Every number on
rendercache.com comes from these files.
"""
import datetime
import json
import os
import pathlib
import platform
import subprocess
import sys

RESULTS = pathlib.Path("test/results")

# The promises RenderCache makes, and the tests that check each one.
PROMISES = [
    ("TestKilledWriterNeverCorruptsSnapshot", "Killing the process during a save never leaves a damaged snapshot"),
    ("TestConcurrentAtomicReplace", "Concurrent refreshes and reads never see a partial snapshot"),
    ("TestOriginDownStillServesSavedPages", "With the origin down, crawlers still get the saved copy"),
    ("TestCrawlerNeverWaitsForBrowser", "A crawler never waits for Chrome"),
    ("TestStaleIsImmediateAndRefreshDeduplicated", "A stale page is served at once and refreshed only once"),
    ("TestFailedRefreshKeepsOldAndPrivateWithdraws", "A failed refresh keeps the old copy, and a page that turns private is withdrawn"),
    ("TestFreshConditionalHeadAndPrivacyBypass", "Requests with cookies or credentials never get a shared snapshot"),
    ("TestServeWarmsBeforeBotAndReusesDisk", "Sitemap pages are rendered before the first crawler and survive a restart"),
    ("TestChromeRealRenderingAndNetworkPolicy", "Chrome captures the finished page and off-origin requests are blocked"),
    ("TestRejectSymlinksInsideAndOutsideRoot", "Data files never follow links"),
    ("TestExclusiveProcessLock", "Only one process can own a data directory"),
    ("TestTrustedEdgeForwardingHeaders", "Only a trusted edge can set forwarding headers for the origin"),
]


def go_env(name):
    return subprocess.run(["go", "env", name], capture_output=True, text=True, check=True).stdout.strip()


def record(annotate):
    command = ["go", "test", "-race", "-json", "-count=1", "./..."]
    process = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, encoding="utf-8", errors="replace")
    status, output, results, packages = {}, {}, {}, set()
    for line in process.stdout:
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            sys.stdout.write(line)
            continue
        package, test, action = event.get("Package", ""), event.get("Test"), event.get("Action")
        packages.add(package)
        if not test or "/" in test:
            if action == "fail" and not test:
                status.setdefault(f"{package} (package)", "fail")
            continue
        key = f"{package}.{test}"
        if action == "output":
            text = event.get("Output", "")
            output.setdefault(key, []).append(text)
            marker = text.find("RESULT ")
            if marker >= 0:
                name, _, value = text[marker + 7:].strip().partition("=")
                try:
                    results[name] = int(value)
                except ValueError:
                    results[name] = value
        elif action in ("pass", "fail", "skip"):
            status[key] = action
    code = process.wait()
    counts = {state: sum(1 for s in status.values() if s == state) for state in ("pass", "fail", "skip")}
    by_name = {key.rsplit(".", 1)[-1]: state for key, state in status.items()}
    promises = [{"test": name, "promise": text, "status": by_name.get(name, "missing")} for name, text in PROMISES]
    goos, goarch = go_env("GOOS"), go_env("GOARCH")
    report = {
        "recorded_at": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "commit": subprocess.run(["git", "rev-parse", "HEAD"], capture_output=True, text=True).stdout.strip(),
        "go": go_env("GOVERSION"),
        "os": goos,
        "arch": goarch,
        "os_version": platform.platform(),
        "real_chrome": bool(os.environ.get("RENDERCACHE_TEST_CHROME")),
        "race_detector": True,
        "packages": len([p for p in packages if p]),
        "tests": {"passed": counts["pass"], "failed": counts["fail"], "skipped": counts["skip"]},
        "promises": promises,
        "results": dict(sorted(results.items())),
    }
    RESULTS.mkdir(parents=True, exist_ok=True)
    path = RESULTS / f"{goos}-{goarch}.json"
    path.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    failed = [key for key, state in status.items() if state == "fail"]
    for key in failed:
        print(f"--- FAIL: {key}")
        for text in output.get(key, [])[-30:]:
            sys.stdout.write("    " + text)
    print(f"{path}: {counts['pass']} passed, {counts['fail']} failed, {counts['skip']} skipped")
    if annotate:
        for key in failed:
            # Include messages from testing.go (cleanup) and panics, not only _test.go lines.
            lines = [t.strip() for t in output.get(key, []) if ".go:" in t or "panic" in t or "Error" in t]
            for text in lines[-6:]:
                print(f"::error title=go test {goos}::{key}: {text[:900]}")
        print(f"::notice title=test-results {goos}-{goarch}::{json.dumps(report, separators=(',', ':'))}")
    if code == 0 and failed:
        code = 1
    return code


def summarize():
    reports = [json.loads(p.read_text(encoding="utf-8")) for p in sorted(RESULTS.glob("*.json"))]
    if not reports:
        sys.exit("no results to summarize")
    lines = ["# Recorded test results", "",
             "Written by `scripts/record-tests.py` from full `go test -race` runs. "
             "Do not edit by hand; every figure on rendercache.com comes from these files.", ""]
    header = "| Promise | " + " | ".join(f"{r['os']} {r['arch']}" for r in reports) + " |"
    lines += [header, "|" + "---|" * (len(reports) + 1)]
    for index, (_, text) in enumerate(PROMISES):
        cells = []
        for r in reports:
            state = r["promises"][index]["status"]
            cells.append({"pass": "passed", "fail": "FAILED", "skip": "skipped"}.get(state, state))
        lines.append(f"| {text} | " + " | ".join(cells) + " |")
    lines += ["", "| Run | " + " | ".join(f"{r['os']} {r['arch']}" for r in reports) + " |",
              "|" + "---|" * (len(reports) + 1)]
    rows = [("Recorded (UTC)", lambda r: r["recorded_at"]), ("Commit", lambda r: r["commit"][:12]),
            ("Go", lambda r: r["go"]), ("Real Chrome", lambda r: "yes" if r["real_chrome"] else "no"),
            ("CI run", lambda r: r.get("ci_run", "local")),
            ("Tests passed", lambda r: r["tests"]["passed"]), ("Tests failed", lambda r: r["tests"]["failed"]),
            ("Tests skipped", lambda r: r["tests"]["skipped"])]
    names = sorted({name for r in reports for name in r["results"]})
    rows += [(name.replace("_", " "), lambda r, n=name: r["results"].get(n, "")) for name in names]
    for label, value in rows:
        lines.append(f"| {label} | " + " | ".join(str(value(r)) for r in reports) + " |")
    (RESULTS / "README.md").write_text("\n".join(lines) + "\n", encoding="utf-8")
    print(RESULTS / "README.md")


def from_ci(run_id):
    """Copy the test-results notices that CI printed for each platform."""
    repo = "rendercache/rendercache"
    jobs = json.loads(subprocess.run(["gh", "api", f"repos/{repo}/actions/runs/{run_id}/jobs?per_page=100"],
                                     capture_output=True, text=True, check=True).stdout)["jobs"]
    RESULTS.mkdir(parents=True, exist_ok=True)
    written = 0
    for job in jobs:
        if not job["name"].startswith("test ("):
            continue
        annotations = json.loads(subprocess.run(["gh", "api", f"repos/{repo}/check-runs/{job['id']}/annotations?per_page=100"],
                                                capture_output=True, text=True, check=True).stdout)
        for note in annotations:
            if (note.get("title") or "").startswith("test-results"):
                report = json.loads(note["message"])
                report["ci_run"] = f"https://github.com/{repo}/actions/runs/{run_id}"
                path = RESULTS / f"{report['os']}-{report['arch']}.json"
                path.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
                print(path, report["tests"])
                written += 1
    if not written:
        sys.exit("no test-results annotations found in that run")


if __name__ == "__main__":
    if "--from-ci" in sys.argv:
        from_ci(sys.argv[sys.argv.index("--from-ci") + 1])
    elif "--summarize" in sys.argv:
        summarize()
    else:
        sys.exit(record("--annotate" in sys.argv))
