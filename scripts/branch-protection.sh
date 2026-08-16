#!/usr/bin/env bash
# Compare or apply the declared branch protection in .github/branch-protection.yaml.
#
# The declaration is reviewed in this repository; the setting lives in GitHub and
# no test can reach it. This script is the only thing that crosses that gap, so
# it reads the declaration rather than restating it: every value it sends comes
# out of the file, and `--check` reports the difference rather than repairing it,
# because a protection change is a decision and not a reconciliation.
set -Eeuo pipefail

readonly repository="NDDev-OpenNetwork/github-actions"

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
readonly declaration="${repo_root}/.github/branch-protection.yaml"

usage() {
  cat >&2 <<'EOF'
usage: branch-protection.sh --check | --apply [--branch NAME]

  --check   read the live settings and report how they differ from the
            declaration; exit 1 if they differ at all
  --apply   send the declaration to GitHub, then re-read and report

The branch defaults to the one named in .github/branch-protection.yaml.
EOF
  exit 2
}

for command in gh jq yq; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    echo "required command is unavailable: ${command}" >&2
    exit 1
  fi
done

mode=""
branch=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --check | --apply)
      [[ -n "${mode}" ]] && usage
      mode="${1#--}"
      shift
      ;;
    --branch)
      [[ $# -ge 2 ]] || usage
      branch="$2"
      shift 2
      ;;
    *) usage ;;
  esac
done
[[ -n "${mode}" ]] || usage

if [[ ! -r "${declaration}" ]]; then
  echo "declaration is unreadable: ${declaration}" >&2
  exit 1
fi

schema=$(yq -o=json '.schema_version' "${declaration}")
if [[ "${schema}" != "1" ]]; then
  echo "declaration schema_version is ${schema}, this script speaks 1" >&2
  exit 1
fi
if [[ -z "${branch}" ]]; then
  branch=$(yq -r '.branch' "${declaration}")
fi
if [[ -z "${branch}" || "${branch}" == "null" ]]; then
  echo "declaration names no branch and none was given" >&2
  exit 1
fi

# schema_version and branch address the declaration; everything else is the
# payload, field for field. Dropping exactly two keys keeps a field added to the
# file from being silently withheld from GitHub.
desired=$(yq -o=json 'del(.schema_version, .branch)' "${declaration}" | jq -S .)

# The read shape is not the write shape: GitHub returns each boolean wrapped in
# an object with an `enabled` member and adds URLs that mean nothing to a
# comparison. Project it back onto the payload shape so the diff is about the
# decisions and not about the envelope.
observe() {
  local raw
  if ! raw=$(gh api "repos/${repository}/branches/${branch}/protection" 2>/dev/null); then
    echo 'null'
    return 0
  fi
  printf '%s' "${raw}" | jq -S '{
    required_status_checks: (
      if .required_status_checks == null then null
      else {strict: .required_status_checks.strict, contexts: .required_status_checks.contexts}
      end
    ),
    required_pull_request_reviews: (
      if .required_pull_request_reviews == null then null
      else {
        required_approving_review_count: .required_pull_request_reviews.required_approving_review_count,
        dismiss_stale_reviews: .required_pull_request_reviews.dismiss_stale_reviews,
        require_code_owner_reviews: .required_pull_request_reviews.require_code_owner_reviews,
        require_last_push_approval: .required_pull_request_reviews.require_last_push_approval
      }
      end
    ),
    enforce_admins: .enforce_admins.enabled,
    required_conversation_resolution: .required_conversation_resolution.enabled,
    required_linear_history: .required_linear_history.enabled,
    allow_force_pushes: .allow_force_pushes.enabled,
    allow_deletions: .allow_deletions.enabled,
    block_creations: .block_creations.enabled,
    lock_branch: .lock_branch.enabled,
    allow_fork_syncing: .allow_fork_syncing.enabled,
    restrictions: (if .restrictions == null then null else {
      users: [.restrictions.users[]?.login],
      teams: [.restrictions.teams[]?.slug],
      apps: [.restrictions.apps[]?.slug]
    } end)
  }'
}

report() {
  local live
  live=$(observe)
  if [[ "${live}" == 'null' ]]; then
    echo "${branch} has no branch protection at all" >&2
    return 1
  fi
  if [[ "${live}" == "${desired}" ]]; then
    echo "${repository}@${branch} matches the declaration"
    return 0
  fi
  echo "${repository}@${branch} differs from the declaration:" >&2
  diff <(printf '%s\n' "${desired}") <(printf '%s\n' "${live}") >&2 || true
  return 1
}

case "${mode}" in
  check)
    report
    ;;
  apply)
    printf '%s' "${desired}" |
      gh api --method PUT "repos/${repository}/branches/${branch}/protection" --input - >/dev/null
    echo "applied the declaration to ${repository}@${branch}"
    report
    ;;
esac
