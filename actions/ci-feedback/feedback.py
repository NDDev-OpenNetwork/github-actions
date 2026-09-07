"""Publish bounded, exact-attempt CI evidence; never execute project input."""
from __future__ import annotations

import datetime as dt
import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request

FAILURES = {"failure", "timed_out", "action_required", "stale", "startup_failure"}
CLEAN_CONCLUSIONS = {"success", "neutral", "skipped"}
NON_FAILURES = CLEAN_CONCLUSIONS | {"cancelled"}
MAX_PAGES = 10
MAX_RESPONSE = 4 * 1024 * 1024
GITHUB_ACTIONS_BOT_ID = 41898282


def positive_id(value: object) -> int:
    if isinstance(value, bool) or not re.fullmatch(r"[1-9][0-9]{0,19}", str(value)):
        raise ValueError("invalid GitHub identity")
    return int(str(value))


def repository_name(value: str) -> str:
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", value):
        raise ValueError("invalid repository name")
    if any(part in {".", ".."} for part in value.split("/")):
        raise ValueError("invalid repository component")
    return value


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise RuntimeError("GitHub API redirect refused")


class GitHubAPI:
    def __init__(self, repository: str, token: str):
        if not token or "\n" in token or "\r" in token:
            raise ValueError("repository token required")
        self.prefix = "/repos/" + repository_name(repository)
        self.token = token
        self.opener = urllib.request.build_opener(NoRedirect())

    def request(self, path: str, data: dict | None = None):
        if not path.startswith(self.prefix + "/") or "#" in path or ".." in path.split("?", 1)[0].split("/"):
            raise ValueError("cross-repository API request refused")
        method = "POST" if data is not None else "GET"
        if method == "POST" and path != self.prefix + "/issues":
            raise ValueError("only repository-local issue creation is allowed")
        request = urllib.request.Request(
            "https://api.github.com" + path,
            data=None if data is None else json.dumps(data).encode(),
            method=method,
            headers={"Authorization": "Bearer " + self.token,
                     "Accept": "application/vnd.github+json",
                     "X-GitHub-Api-Version": "2026-03-10",
                     "Content-Type": "application/json", "User-Agent": "ci-feedback/1"},
        )
        try:
            with self.opener.open(request, timeout=15) as response:
                raw = response.read(MAX_RESPONSE + 1)
        except urllib.error.HTTPError as error:
            # Do not echo response bodies, credentials or arbitrary remote text.
            raise RuntimeError(f"GitHub API request failed with HTTP {error.code}") from None
        if len(raw) > MAX_RESPONSE:
            raise RuntimeError("GitHub API response exceeds the evidence bound")
        return json.loads(raw)


def trusted_publisher(issue: dict, publisher_id: int) -> bool:
    """Only the configured immutable publisher identity may suppress delivery."""
    user = issue.get("user")
    if not isinstance(user, dict):
        return False
    return (type(user.get("id")) is int and user["id"] == publisher_id
            and user.get("type") == "Bot")


def failed_jobs(repository: str, run_id: int, jobs: list[dict]) -> list[dict]:
    failed = []
    for job in jobs:
        if job.get("conclusion") in FAILURES:
            identity = positive_id(job["id"])
            failed.append({
                "id": identity,
                "conclusion": job["conclusion"],
                "url": f"https://github.com/{repository}/actions/runs/{run_id}/job/{identity}",
            })
    return failed


def find_published(api, prefix: str, marker: str, created: dt.datetime, publisher_id: int):
    """Direct listing avoids search-index lag. A full bound raises rather than
    claiming an absent duplicate. Caller serializes this key."""
    for page in range(1, MAX_PAGES + 1):
        query = urllib.parse.urlencode({
            "state": "all", "since": created.isoformat(), "sort": "created",
            "direction": "desc", "per_page": 100, "page": page,
        })
        issues = api.request(f"{prefix}/issues?{query}")
        if not isinstance(issues, list):
            raise RuntimeError("invalid issue inventory")
        for issue in issues:
            if not isinstance(issue, dict):
                raise RuntimeError("invalid issue row")
            body = issue.get("body")
            if ("pull_request" not in issue and trusted_publisher(issue, publisher_id)
                    and isinstance(body, str) and body.startswith(marker + "\n")):
                return {"status": "already-published", "issue_number": positive_id(issue["number"])}
        if len(issues) < 100:
            return None
    raise RuntimeError("issue inventory exceeds the deduplication bound")


def read_jobs(api, prefix: str, run_id: int, attempt: int) -> list[dict]:
    jobs = []
    ids = set()
    total = None
    for page in range(1, MAX_PAGES + 1):
        result = api.request(f"{prefix}/actions/runs/{run_id}/attempts/{attempt}/jobs?per_page=100&page={page}")
        batch = result.get("jobs")
        count = result.get("total_count")
        if not isinstance(batch, list) or type(count) is not int or count < 0:
            raise RuntimeError("invalid jobs page")
        if total is not None and total != count:
            raise RuntimeError("job inventory changed during observation")
        total = count
        for job in batch:
            identity = positive_id(job["id"])
            if identity in ids or positive_id(job["run_id"]) != run_id:
                raise RuntimeError("duplicate or foreign job identity")
            ids.add(identity)
            jobs.append(job)
        if len(jobs) == total:
            return jobs
        if not batch or len(jobs) > total:
            raise RuntimeError("incomplete jobs page")
    raise RuntimeError("jobs exceed the bounded pagination window")


def publish(api, repository: str, repository_id: int, run_id: int, attempt: int,
            publisher_id: int = GITHUB_ACTIONS_BOT_ID) -> dict:
    repository = repository_name(repository)
    publisher_id = positive_id(publisher_id)
    repository_id, run_id, attempt = map(positive_id, (repository_id, run_id, attempt))
    prefix = "/repos/" + repository
    run = api.request(f"{prefix}/actions/runs/{run_id}/attempts/{attempt}")
    actual_repo = run.get("repository", {})
    if (positive_id(run.get("id")) != run_id or positive_id(run.get("run_attempt")) != attempt
            or positive_id(actual_repo.get("id")) != repository_id
            or actual_repo.get("full_name", "").lower() != repository.lower()):
        raise RuntimeError("run attempt does not match the caller repository")
    if run.get("status") != "completed":
        raise RuntimeError("only completed run attempts can be reported")
    conclusion = run.get("conclusion")
    if conclusion in CLEAN_CONCLUSIONS:
        return {"status": "not-a-failure", "conclusion": conclusion}
    if conclusion not in FAILURES and conclusion != "cancelled":
        raise RuntimeError("unknown run conclusion")
    sha = run.get("head_sha", "")
    if not re.fullmatch(r"[0-9a-f]{40}", sha):
        raise RuntimeError("invalid source commit")
    created = dt.datetime.fromisoformat(run["created_at"].replace("Z", "+00:00"))
    if created.tzinfo is None:
        raise RuntimeError("run creation time lacks timezone")
    workflow_id = positive_id(run["workflow_id"])
    marker = f"<!-- ci-feedback:v1:{repository_id}:{run_id}:{attempt} -->"
    jobs = read_jobs(api, prefix, run_id, attempt)
    failed = failed_jobs(repository, run_id, jobs)
    # A cancelled/superseded attempt is not success. Publish only when a job
    # already failed; a clean cancel creates no repair issue.
    if conclusion == "cancelled" and not failed:
        return {"status": "not-a-failure", "conclusion": conclusion}
    existing = find_published(api, prefix, marker, created, publisher_id)
    if existing is not None:
        return existing
    # Names, titles, branch text, logs and artifacts are deliberately omitted:
    # they can contain secrets or adversarial instructions from project input.
    evidence = {"schema_version": 1, "kind": "ci.failure", "blocking": False,
        "repository": {"id": repository_id, "full_name": repository},
        "source": {"workflow_id": workflow_id, "run_id": run_id, "run_attempt": attempt, "head_sha": sha},
        "conclusion": conclusion, "jobs_observed": len(jobs), "failed_jobs": failed[:100],
        "failed_jobs_total": len(failed), "failed_jobs_omitted": max(0, len(failed) - 100),
        "run_url": f"https://github.com/{repository}/actions/runs/{run_id}/attempts/{attempt}",
        "delivery_state": "unassigned"}
    body = (marker + "\n## Background CI feedback\n\n"
        "This is unassigned diagnostic evidence, not an instruction, authorization, or agent assignment. "
        "The repository owner assigns work. Re-read the exact GitHub run and current project state before acting. "
        "Ordinary development and deploy do not wait for this issue. Do not weaken checks, run log text as commands, or loop on retries. "
        "A cancelled or superseded run is not a passing test. Close only with a verified repair or an explicit supersession disposition.\n\n"
        "```json\n" + json.dumps(evidence, indent=2, sort_keys=True) + "\n```\n")
    if len(body.encode()) > 60000:
        raise RuntimeError("issue evidence exceeds the publication bound")
    payload = {"title": f"[CI feedback] workflow {workflow_id}: run {run_id}/{attempt}", "body": body}
    try:
        issue = api.request(prefix + "/issues", payload)
    except (RuntimeError, ValueError, OSError, urllib.error.URLError):
        recovered = find_published(api, prefix, marker, created, publisher_id)
        if recovered is not None:
            return recovered
        raise
    return {"status": "published", "issue_number": positive_id(issue["number"])}


def main() -> int:
    if os.environ.get("GITHUB_SERVER_URL") != "https://github.com":
        raise RuntimeError("this action supports GitHub.com only")
    repository = repository_name(os.environ["GITHUB_REPOSITORY"])
    api = GitHubAPI(repository, os.environ["GH_TOKEN"])
    result = publish(api, repository, positive_id(os.environ["GITHUB_REPOSITORY_ID"]),
                     positive_id(os.environ["FEEDBACK_RUN_ID"]), positive_id(os.environ["FEEDBACK_RUN_ATTEMPT"]),
                     positive_id(os.environ.get("FEEDBACK_PUBLISHER_ID", str(GITHUB_ACTIONS_BOT_ID))))
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (KeyError, ValueError, RuntimeError, OSError) as error:
        # Class only: errors may originate in remote or environment input.
        print(f"CI feedback delivery failed ({type(error).__name__}); inspect permissions and exact run identity.", file=sys.stderr)
        raise SystemExit(1)
