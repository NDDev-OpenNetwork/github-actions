"""Run the real publisher with isolated API fixtures, no token or network."""
import copy
import importlib.util
import json
import pathlib
import re
import unittest
from unittest import mock

PATH = pathlib.Path(__file__).resolve().parents[1] / "actions/ci-feedback/feedback.py"
SPEC = importlib.util.spec_from_file_location("feedback", PATH)
feedback = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(feedback)
REPO = "example-org/project"
RUN = {"id": 100, "run_attempt": 2, "repository": {"id": 10, "full_name": REPO},
       "status": "completed", "conclusion": "failure", "head_sha": "a" * 40,
       "workflow_id": 50, "created_at": "2026-09-07T00:00:00Z"}


class API:
    def __init__(self):
        self.run = copy.deepcopy(RUN)
        self.issues = []
        self.jobs = [{"id": 101, "run_id": 100, "conclusion": "failure", "name": "untrusted $(payload)"}]
        self.calls = []
        self.posts = []

    def request(self, path, data=None):
        self.calls.append(path)
        if data is not None:
            self.posts.append(data)
            return {"number": 1}
        if "/issues?" in path:
            return self.issues
        if "/jobs?" in path:
            return {"jobs": self.jobs, "total_count": len(self.jobs)}
        return self.run


class FeedbackTests(unittest.TestCase):
    def publish(self, api):
        return feedback.publish(api, REPO, 10, 100, 2)

    def test_failure_is_exact_and_has_no_project_text(self):
        api = API()
        self.assertEqual(self.publish(api), {"status": "published", "issue_number": 1})
        body = api.posts[0]["body"]
        evidence = json.loads(body.split("```json\n")[1].split("\n```")[0])
        self.assertEqual(evidence["source"]["run_attempt"], 2)
        self.assertEqual(evidence["source"]["head_sha"], "a" * 40)
        observed = feedback.dt.datetime.fromisoformat(evidence["observed_at"])
        self.assertEqual(observed.utcoffset(), feedback.dt.timedelta(0))
        self.assertEqual(evidence["failure"]["classification"], "unknown")
        self.assertEqual(evidence["failure"]["reason"], "1 job(s) failed on this exact attempt.")
        self.assertFalse(evidence["blocking"])
        self.assertEqual(evidence["delivery_state"], "unassigned")
        self.assertNotIn("repair_owner", evidence)
        self.assertNotIn("pending-agent-consumption", body)
        self.assertNotIn("untrusted $(payload)", body)
        self.assertIn("/attempts/2/jobs", " ".join(api.calls))
        self.assertNotIn("assignees", api.posts[0])

    def test_large_failure_set_is_explicitly_bounded(self):
        api = API()
        api.jobs = [{"id": n, "run_id": 100, "conclusion": "failure"} for n in range(1000, 1101)]
        self.publish(api)
        evidence = json.loads(api.posts[0]["body"].split("```json\n")[1].split("\n```")[0])
        self.assertEqual(len(evidence["failed_jobs"]), 100)
        self.assertEqual(evidence["failed_jobs_total"], 101)
        self.assertEqual(evidence["failed_jobs_omitted"], 1)

    def test_successful_runs_create_no_work(self):
        for conclusion in feedback.CLEAN_CONCLUSIONS:
            api = API()
            api.run["conclusion"] = conclusion
            self.assertEqual(self.publish(api)["status"], "not-a-failure")
            self.assertEqual(len(api.calls), 1)
            self.assertEqual(api.posts, [])

    def test_cancelled_run_without_failed_jobs_creates_no_work(self):
        api = API()
        api.run["conclusion"] = "cancelled"
        api.jobs = [{"id": 101, "run_id": 100, "conclusion": "cancelled"}]
        self.assertEqual(self.publish(api), {"status": "not-a-failure", "conclusion": "cancelled"})
        self.assertEqual(api.posts, [])
        self.assertTrue(any("/jobs?" in call for call in api.calls))

    def test_cancelled_run_with_failed_job_is_published_unassigned(self):
        api = API()
        api.run["conclusion"] = "cancelled"
        api.jobs = [
            {"id": 101, "run_id": 100, "conclusion": "failure"},
            {"id": 102, "run_id": 100, "conclusion": "cancelled"},
        ]
        self.assertEqual(self.publish(api), {"status": "published", "issue_number": 1})
        evidence = json.loads(api.posts[0]["body"].split("```json\n")[1].split("\n```")[0])
        self.assertEqual(evidence["conclusion"], "cancelled")
        self.assertEqual(evidence["delivery_state"], "unassigned")
        self.assertEqual(evidence["failed_jobs_total"], 1)
        self.assertEqual(evidence["failed_jobs"][0]["id"], 101)

    def test_wrong_repository_attempt_sha_and_incomplete_run_are_rejected(self):
        for updates in ({"run_attempt": 3}, {"repository": {"id": 11, "full_name": REPO}},
                        {"repository": {"id": 10, "full_name": "elsewhere/private"}},
                        {"head_sha": "bad"}, {"status": "in_progress"}, {"conclusion": "unknown"}):
            api = API()
            api.run.update(updates)
            with self.assertRaises((ValueError, RuntimeError)):
                self.publish(api)
            self.assertEqual(api.posts, [])

    def test_duplicate_including_closed_issue_is_not_republished(self):
        api = API()
        api.issues = [{"number": 9, "state": "closed", "user": {"id": feedback.GITHUB_ACTIONS_BOT_ID, "type": "Bot"},
                       "body": "<!-- ci-feedback:v1:10:100:2 -->\nprevious evidence"}]
        self.assertEqual(self.publish(api), {"status": "already-published", "issue_number": 9})
        self.assertEqual(api.posts, [])

    def test_app_bot_publisher_is_trusted_for_dedup(self):
        api = API()
        api.issues = [{"number": 4, "user": {"id": 700, "login": "example-reporter[bot]", "type": "Bot"},
                       "body": "<!-- ci-feedback:v1:10:100:2 -->\nprevious evidence"}]
        self.assertEqual(feedback.publish(api, REPO, 10, 100, 2, publisher_id=700),
                         {"status": "already-published", "issue_number": 4})
        self.assertEqual(api.posts, [])

    def test_explicit_user_publisher_requires_exact_id_and_type(self):
        for user, expected in (({"id": 700, "type": "User"}, "already-published"),
                               ({"id": 701, "type": "User"}, "published"),
                               ({"id": 700, "type": "Bot"}, "published"),
                               ({"id": True, "type": "User"}, "published")):
            with self.subTest(user=user):
                api = API()
                api.issues = [{"number": 4, "user": user,
                               "body": "<!-- ci-feedback:v1:10:100:2 -->\nprevious evidence"}]
                result = feedback.publish(api, REPO, 10, 100, 2,
                                          publisher_id=700, publisher_type="User")
                self.assertEqual(result["status"], expected)

    def test_unknown_publisher_type_fails_before_api_access(self):
        for value in ("Organization", "user", "", None, []):
            api = API()
            with self.assertRaises(ValueError):
                feedback.publish(api, REPO, 10, 100, 2, publisher_type=value)
            self.assertEqual(api.calls, [])

    def test_user_publisher_recovers_ambiguous_post_without_second_write(self):
        api = API()
        real = api.request
        writes = []
        def request(path, data=None):
            if data is not None:
                writes.append(data)
                api.issues = [{"number": 12, "user": {"id": 700, "type": "User"},
                               "body": "<!-- ci-feedback:v1:10:100:2 -->\npublished"}]
                raise TimeoutError("POST reply lost")
            return real(path, data)
        api.request = request
        result = feedback.publish(api, REPO, 10, 100, 2,
                                  publisher_id=700, publisher_type="User")
        self.assertEqual(result, {"status": "already-published", "issue_number": 12})
        self.assertEqual(len(writes), 1)

    def test_user_authored_marker_cannot_suppress_feedback(self):
        api = API()
        api.issues = [{"number": 9, "user": {"login": "other", "type": "User"},
                       "body": "<!-- ci-feedback:v1:10:100:2 -->\n"}]
        self.assertEqual(self.publish(api)["status"], "published")

    def test_lost_post_reply_recovers_durable_marker(self):
        api = API()
        real = api.request

        def request(path, data=None):
            if data is not None:
                api.issues = [{"number": 12, "user": {"id": feedback.GITHUB_ACTIONS_BOT_ID, "type": "Bot"},
                               "body": "<!-- ci-feedback:v1:10:100:2 -->\npublished"}]
                raise TimeoutError("POST reply lost")
            return real(path, data)

        api.request = request
        self.assertEqual(self.publish(api), {"status": "already-published", "issue_number": 12})
        self.assertEqual(api.posts, [])

    def test_unrelated_bot_or_forged_login_does_not_suppress_failure(self):
        for user in ({"id": 701, "type": "Bot", "login": "other[bot]"},
                     {"id": 701, "type": "Bot", "login": "github-actions[bot]"},
                     {"login": "github-actions[bot]"},
                     {"id": feedback.GITHUB_ACTIONS_BOT_ID, "type": "User"},
                     {"id": True, "type": "Bot"}):
            with self.subTest(user=user):
                api = API()
                api.issues = [{"number": 9, "user": user,
                               "body": "<!-- ci-feedback:v1:10:100:2 -->\nspoofed"}]
                self.assertEqual(self.publish(api)["status"], "published")

    def test_invalid_publisher_identity_fails_before_api_access(self):
        api = API()
        with self.assertRaises(ValueError):
            feedback.publish(api, REPO, 10, 100, 2, publisher_id=True)
        self.assertEqual(api.calls, [])

    def test_unknown_post_result_does_not_retry_write(self):
        api = API()
        real = api.request
        writes = []

        def request(path, data=None):
            if data is not None:
                writes.append(data)
                raise TimeoutError("unknown write outcome")
            return real(path, data)

        api.request = request
        with self.assertRaises(TimeoutError):
            self.publish(api)
        self.assertEqual(len(writes), 1)
        self.assertEqual(sum("/issues?" in call for call in api.calls), 2)

    def test_foreign_and_duplicate_jobs_rejected(self):
        for jobs in ([{"id": 101, "run_id": 999}], [{"id": 101, "run_id": 100}] * 2):
            api = API()
            api.jobs = jobs
            with self.assertRaises(RuntimeError):
                self.publish(api)
            self.assertEqual(api.posts, [])

    def test_run_level_failure_without_jobs_remains_failure(self):
        api = API()
        api.jobs = []
        api.run["conclusion"] = "startup_failure"
        self.assertEqual(self.publish(api)["status"], "published")
        self.assertIn('"jobs_observed": 0', api.posts[0]["body"])
        self.assertIn('"conclusion": "startup_failure"', api.posts[0]["body"])

    def test_bounded_dedup_never_assumes_absence(self):
        api = API()
        api.issues = [{"number": n} for n in range(100)]
        with self.assertRaises(RuntimeError):
            self.publish(api)
        self.assertEqual(api.posts, [])

    def test_incomplete_jobs_do_not_publish_partial_evidence(self):
        api = API()
        real = api.request
        def request(path, data=None):
            if "/jobs?" in path:
                return {"total_count": 2, "jobs": []}
            return real(path, data)
        api.request = request
        with self.assertRaises(RuntimeError):
            self.publish(api)
        self.assertEqual(api.posts, [])

    def test_token_is_not_forwarded_to_other_origin_or_mutation(self):
        api = feedback.GitHubAPI(REPO, "synthetic-token")
        with mock.patch.object(api.opener, "open") as opened:
            for path, data in (("https://elsewhere.invalid", None), ("/repos/other/private/issues", None),
                               ("/repos/example-org/project/actions/runs/100/cancel", {})):
                with self.assertRaises(ValueError):
                    api.request(path, data)
            opened.assert_not_called()
        with self.assertRaises(RuntimeError):
            feedback.NoRedirect().redirect_request(None, None, 302, "", {}, "https://elsewhere.invalid")

    def test_invalid_ids_and_repository_paths_rejected(self):
        for value in (True, 0, -1, "1/2", "1\n", "", " 1", "1;id"):
            with self.assertRaises(ValueError):
                feedback.positive_id(value)
        for value in ("../repo", "org/..", "org/repo/extra", "org/repo?token=x"):
            with self.assertRaises(ValueError):
                feedback.repository_name(value)

    def test_workflow_run_caller_invokes_publisher_on_cancelled(self):
        text = pathlib.Path(__file__).resolve().parents[1].joinpath(
            ".github/workflows/ci-feedback-events.yml").read_text()
        match = re.search(r"fromJSON\('(\[[^\]]+\])'\)", text)
        self.assertIsNotNone(match, "shipped workflow_run caller lost its conclusion filter")
        conclusions = json.loads(match.group(1))
        for value in ("failure", "timed_out", "action_required", "stale",
                      "startup_failure", "cancelled"):
            self.assertIn(value, conclusions)
        for value in ("success", "neutral", "skipped"):
            self.assertNotIn(value, conclusions)
        self.assertIn("uses: ./.github/workflows/ci-feedback.yml", text)


if __name__ == "__main__":
    unittest.main()
