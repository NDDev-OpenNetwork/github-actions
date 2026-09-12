"""Run the real publisher with isolated API fixtures, no token or network."""
import copy
import importlib.util
import json
import pathlib
import re
import urllib.parse
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
            page = int(urllib.parse.parse_qs(urllib.parse.urlsplit(path).query)["page"][0])
            return {"jobs": self.jobs[(page - 1) * 100:page * 100], "total_count": len(self.jobs)}
        return self.run


class FeedbackTests(unittest.TestCase):
    def publish(self, api):
        return feedback.publish(api, REPO, 10, 100, 2)

    def early(self, api):
        return feedback.publish(api, REPO, 10, 100, 2, allow_in_progress=True)

    def active_api(self):
        api = API()
        api.run.update(status="in_progress", conclusion=None)
        api.jobs[0].update(status="completed", run_attempt=2, head_sha="a" * 40)
        api.jobs.append({"id": 102, "run_id": 100, "status": "queued", "conclusion": None})
        return api

    def test_early_failure_is_a_dated_unfinished_attempt_observation(self):
        api = self.active_api()
        result = self.early(api)
        self.assertEqual(result, {"status": "published", "issue_number": 1,
                                 "subjects": [{"job_id": 101, "status": "published", "issue_number": 1}],
                                 "attempt_complete": False, "run_status": "in_progress",
                                 "run_conclusion": None})
        body = api.posts[0]["body"]
        evidence = json.loads(body.split("```json\n")[1].split("\n```")[0])
        self.assertFalse(evidence["attempt_complete"])
        self.assertEqual(evidence["run_status"], "in_progress")
        self.assertIsNone(evidence["conclusion"])
        self.assertEqual(evidence["failed_jobs_total"], 1)
        self.assertEqual(evidence["jobs_observed"], 2)
        self.assertIn("workflow is unfinished", body)
        self.assertNotIn("untrusted $(payload)", body)
        self.assertEqual(evidence["source"]["run_attempt"], 2)

    def test_active_failure_requires_explicit_opt_in(self):
        api = self.active_api()
        with self.assertRaises(RuntimeError):
            self.publish(api)
        self.assertEqual(api.posts, [])
        for flag in ("true", 1, None, []):
            with self.assertRaises(ValueError):
                feedback.publish(api, REPO, 10, 100, 2, allow_in_progress=flag)

    def test_active_without_failed_jobs_stays_pending_not_success(self):
        for status in feedback.ACTIVE_STATUSES:
            api = self.active_api()
            api.run["status"] = status
            api.jobs = api.jobs[1:]
            result = self.early(api)
            self.assertEqual(result["status"], "pending")
            self.assertFalse(result["attempt_complete"])
            self.assertIsNone(result["run_conclusion"])
            self.assertEqual(api.posts, [])

    def test_early_refuses_uncompleted_failed_job_and_conflicting_identity(self):
        for updates in ({"status": "in_progress"}, {"status": None},
                        {"run_attempt": 1}, {"head_sha": "b" * 40}):
            api = self.active_api()
            api.jobs[0].update(updates)
            with self.assertRaises((ValueError, RuntimeError)):
                self.early(api)
            self.assertEqual(api.posts, [])

    def test_early_refuses_unknown_run_state_or_nonterminal_final_conclusion(self):
        for updates in ({"status": "unknown"}, {"status": None}, {"status": []},
                        {"conclusion": "failure"}, {"conclusion": {}}):
            api = self.active_api()
            api.run.update(updates)
            with self.assertRaises(RuntimeError):
                self.early(api)
            self.assertEqual(api.posts, [])

    def test_early_then_completed_reuses_one_issue_and_reports_terminal_observation(self):
        api = self.active_api()
        first = self.early(api)
        self.assertFalse(first["attempt_complete"])
        original_body = api.posts[0]["body"]
        api.issues = [{"number": 1, "user": {"id": feedback.GITHUB_ACTIONS_BOT_ID, "type": "Bot"},
                       "body": original_body}]
        api.run.update(status="completed", conclusion="cancelled")
        api.jobs[1].update(status="completed", conclusion="cancelled")
        final = self.early(api)
        self.assertEqual(final["status"], "already-published")
        self.assertTrue(final["attempt_complete"])
        self.assertEqual(final["run_conclusion"], "cancelled")
        self.assertEqual(len(api.posts), 1)
        self.assertEqual(api.issues[0]["body"], original_body)

    def test_later_failed_jobs_are_published_without_reopening_prior_issue(self):
        api = self.active_api()
        self.early(api)
        original = api.posts[0]["body"]
        api.issues = [{"number": 7, "state": "closed",
                       "user": {"id": feedback.GITHUB_ACTIONS_BOT_ID, "type": "Bot"},
                       "body": original}]
        api.jobs[1].update(status="completed", conclusion="timed_out")
        api.run.update(status="completed", conclusion="failure")
        result = self.early(api)
        self.assertEqual(len(api.posts), 2)
        second = json.loads(api.posts[1]["body"].split("```json\n")[1].split("\n```")[0])
        self.assertEqual([job["id"] for job in second["failed_jobs"]], [102])
        self.assertEqual(api.issues[0]["body"], original)
        self.assertTrue(result["attempt_complete"])
        self.assertEqual({subject["job_id"] for subject in result["subjects"]}, {101, 102})

    def test_legacy_snapshot_covers_only_exact_recorded_jobs(self):
        api = API()
        self.publish(api)
        original = api.posts[0]["body"]
        for body, expected_new in ((original, 1), (original.replace('"head_sha": "' + "a" * 40,
                                   '"head_sha": "' + "b" * 40), 2),
                                   (original.split("```json")[0] + "```json\n{}\n```", 2)):
            with self.subTest(body=body[:40]):
                api = self.active_api()
                api.jobs[1].update(status="completed", conclusion="failure")
                api.issues = [{"number": 8, "state": "closed", "body": body,
                               "user": {"id": feedback.GITHUB_ACTIONS_BOT_ID, "type": "Bot"}}]
                self.early(api)
                self.assertEqual(len(api.posts), expected_new)

    def test_ambiguous_job_post_recovers_only_its_exact_subject(self):
        api = self.active_api()
        real = api.request
        writes = []
        def request(path, data=None):
            if data is not None:
                writes.append(data)
                api.issues.append({"number": 12, "body": data["body"],
                                   "user": {"id": feedback.GITHUB_ACTIONS_BOT_ID, "type": "Bot"}})
                raise TimeoutError("POST reply lost")
            return real(path, data)
        api.request = request
        result = self.early(api)
        self.assertEqual(result["subjects"], [{"job_id": 101, "status": "already-published", "issue_number": 12}])
        self.assertEqual(len(writes), 1)

    def test_exhausted_job_is_reconciled_before_deferral_and_other_jobs_progress(self):
        api = self.active_api()
        api.jobs[1].update(status="completed", conclusion="failure")
        result = feedback.publish(api, REPO, 10, 100, 2, allow_in_progress=True, exhausted_job_ids=[101])
        self.assertEqual(result["status"], "partial")
        self.assertEqual(result["deferred_job_ids"], [101])
        self.assertEqual([subject["job_id"] for subject in result["subjects"]], [102])
        self.assertEqual(len(api.posts), 1)
        api.issues = [{"number": 9, "body": "<!-- ci-feedback-job:v1:10:100:2:101 -->\nold",
                       "user": {"id": feedback.GITHUB_ACTIONS_BOT_ID, "type": "Bot"}}]
        api.jobs = api.jobs[:1]
        result = feedback.publish(api, REPO, 10, 100, 2, allow_in_progress=True, exhausted_job_ids=[101])
        self.assertEqual(result["status"], "already-published")
        self.assertEqual(len(api.posts), 1)

    def test_terminal_observation_metadata_is_explicit_in_early_mode(self):
        api = API()
        api.run["conclusion"] = "success"
        result = self.early(api)
        self.assertEqual(result["status"], "not-a-failure")
        self.assertTrue(result["attempt_complete"])
        self.assertEqual(result["run_conclusion"], "success")
        self.assertEqual(api.posts, [])

    def test_early_input_boolean_parser_is_exact(self):
        self.assertTrue(feedback.early_mode("true"))
        self.assertFalse(feedback.early_mode("false"))
        for value in ("True", "1", "", " false "):
            with self.assertRaises(ValueError):
                feedback.early_mode(value)

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
