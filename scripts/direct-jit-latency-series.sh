#!/usr/bin/env bash
set -euo pipefail

repository=${GHA_REPOSITORY:-NDDev-OpenNetwork/github-actions}
workflow=${GHA_WORKFLOW:-self-hosted-canary.yml}
ref=${GHA_REF:-main}
runner_label=${GHA_RUNNER_LABEL:-nddev-linux-standard}
server=${GHA_SERVER:-server-example-legacy}
required_samples=${GHA_REQUIRED_SAMPLES:-20}
max_load_1=${GHA_MAX_LOAD_1:-4}
output=${1:-config/direct-jit-nddev21-latency-audit.json}

expected_provider_version=${GHA_PROVIDER_VERSION:-v0.1.5-nddev.21}
target_milliseconds=5000

[[ ${required_samples} =~ ^[1-9][0-9]*$ ]] || {
  printf 'GHA_REQUIRED_SAMPLES must be a positive integer\n' >&2
  exit 2
}
[[ ${max_load_1} =~ ^[0-9]+([.][0-9]+)?$ ]] || {
  printf 'GHA_MAX_LOAD_1 must be a non-negative number\n' >&2
  exit 2
}
[[ ${output} != / && ${output} != */ ]] || {
  printf 'output must be a file path\n' >&2
  exit 2
}
command -v gh >/dev/null
command -v jq >/dev/null
command -v ssh >/dev/null
command -v sha256sum >/dev/null
gh auth status >/dev/null

head_sha=$(gh api "repos/${repository}/commits/${ref}" --jq .sha)
[[ ${head_sha} =~ ^[0-9a-f]{40}$ ]]

# The workflow revision and the deployed provider build are independent
# identities. Evidence-only commits may advance the workflow ref without a
# provider rollout, so bind the series to the identity recorded on the actual
# Incus worker instead of assuming both commits are equal.
provider_identity=$(ssh -o BatchMode=yes -o ConnectTimeout=10 "${server}" bash -s <<'REMOTE'
set -eu
instances=$(incus list --project gha-fleet --format csv -c n)
count=$(printf '%s\n' "${instances}" | sed '/^$/d' | wc -l)
[[ ${count} == 1 ]]
instance=$(printf '%s\n' "${instances}" | sed '/^$/d')
incus config show "${instance}" --project gha-fleet --expanded |
  awk -F ': ' '
    $1 == "  user.nddev.provider-version" {version=$2}
    $1 == "  user.nddev.provider-commit" {commit=$2}
    END {printf "%s\t%s\n", version, commit}
  '
REMOTE
)
IFS=$'\t' read -r provider_version provider_commit <<<"${provider_identity}"
[[ ${provider_version} == "${expected_provider_version}" ]]
[[ ${provider_commit} =~ ^[0-9a-f]{40}$ ]]

atomic_filter() {
  local filter=$1
  shift
  local temporary
  temporary=$(mktemp "${output}.tmp.XXXXXX")
  jq "$@" "${filter}" "${output}" >"${temporary}"
  mv -- "${temporary}" "${output}"
}

if [[ -e ${output} ]]; then
  jq -e \
    --arg repository "${repository}" \
    --arg workflow "${workflow}" \
    --arg ref "${ref}" \
    --arg head_sha "${head_sha}" \
    --arg provider_version "${provider_version}" \
    --arg provider_commit "${provider_commit}" \
    --argjson required "${required_samples}" \
    '.schema_version == 2 and
     .series.repository == $repository and
     .series.workflow == $workflow and
     .series.ref == $ref and
     .series.head_sha == $head_sha and
     .series.provider_version == $provider_version and
     .series.provider_commit == $provider_commit and
     .series.required_samples == $required and
     (.samples | type == "array")' \
    "${output}" >/dev/null || {
      printf 'existing output does not match this immutable series\n' >&2
      exit 2
    }
else
  install -d -m 0755 "$(dirname -- "${output}")"
  jq -n \
    --arg captured_at "$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)" \
    --arg repository "${repository}" \
    --arg workflow "${workflow}" \
    --arg ref "${ref}" \
    --arg head_sha "${head_sha}" \
    --arg runner_label "${runner_label}" \
    --arg server "${server}" \
    --arg provider_version "${provider_version}" \
    --arg provider_commit "${provider_commit}" \
    --arg quantile_method nearest-rank \
    --arg latency_definition github-job-created-to-started-observed \
    --argjson required_samples "${required_samples}" \
    --argjson max_load_1 "${max_load_1}" \
    --argjson target_milliseconds "${target_milliseconds}" \
    '{
      schema_version: 2,
      captured_at: $captured_at,
      series: {
        repository: $repository,
        workflow: $workflow,
        ref: $ref,
        head_sha: $head_sha,
        runner_label: $runner_label,
        server: $server,
        provider_version: $provider_version,
        provider_commit: $provider_commit,
        required_samples: $required_samples,
        nominal_preflight_max_load_1: $max_load_1,
        latency_definition: $latency_definition,
        quantile_method: $quantile_method,
        target_p95_milliseconds_exclusive: $target_milliseconds
      },
      samples: [],
      statistics: null,
      postconditions: null,
      verdict: {
        complete: false,
        warm_queue_to_online_p95_gate_complete: false
      }
    }' >"${output}"
fi

snapshot() {
  ssh -o BatchMode=yes -o ConnectTimeout=10 "${server}" \
    'curl --fail --silent http://127.0.0.1:9464/snapshot'
}

provider_journal() {
  ssh -o BatchMode=yes -o ConnectTimeout=10 "${server}" \
    'sudo -n cat /var/lib/gha-fleet/provider-journal.json'
}

failed_units() {
  ssh -o BatchMode=yes -o ConnectTimeout=10 "${server}" \
    'systemctl --failed --no-legend | wc -l'
}

retained_state() {
  ssh -o BatchMode=yes -o ConnectTimeout=10 "${server}" '
    set -eu
    listeners=$(pgrep -af "Runner.Listener" | grep -v grep | wc -l)
    example-platform=$(docker ps --format "{{.Names}}|{{.Status}}" | awk -F "|" '\''
      $1 == "example-platform-backend" || $1 == "example-platform-ml-server" {
        found++
        if ($2 !~ /healthy/) bad++
      }
      END { print (found == 2 && bad == 0) ? "true" : "false" }
    '\'')
    captcha=$(docker ps --format "{{.Names}}|{{.Status}}" | awk -F "|" '\''
      $1 == "nddev-captcha" || $1 == "nddev-captcha-valkey" {
        found++
        if ($2 !~ /healthy/) bad++
      }
      END { print (found == 2 && bad == 0) ? "true" : "false" }
    '\'')
    jq -n --argjson listeners "${listeners}" --argjson example-platform "${example-platform}" --argjson captcha "${captcha}" \
      "{legacy_listeners:\$listeners,example_platform_healthy:\$example-platform,captcha_healthy:\$captcha}"
  '
}

wait_for_preflight() {
  local deadline=$((SECONDS + 1800))
  local observed
  while ((SECONDS < deadline)); do
    observed=$(snapshot)
    if jq -e \
      --argjson max_load "${max_load_1}" \
      '.healthy and
       .host.maintenance.system_state == "running" and
       .host.maintenance.reboot_required == false and
       .host.root_filesystem.free_percent >= 20 and
       .host.cpu.load_1 <= $max_load and
       .host.legacy_runners.listeners == 12 and
       .journal.leases == 1 and .journal.claims == 0 and
       .journal.by_state["warm-ready"] == 1 and
       .queue.active == 0 and .queue.in_flight == 0 and
       .queue.uncovered_running == 0 and
       .incus.visible_instances == 1 and
       .incus.orphan_instances == 0 and .incus.missing_instances == 0 and
       .diagnostic_export_sync.state == "synchronized" and
       .diagnostic_export.pending_bundles == 0' <<<"${observed}" >/dev/null &&
      [[ $(failed_units) == 0 ]] &&
      jq -e '.legacy_listeners == 12 and .example_platform_healthy and .captcha_healthy' \
        <<<"$(retained_state)" >/dev/null; then
      printf '%s\n' "${observed}"
      return
    fi
    sleep 10
  done
  printf 'nominal preflight did not converge within 30 minutes\n' >&2
  return 1
}

wait_for_workflow_idle() {
  local deadline=$((SECONDS + 900))
  local active
  while ((SECONDS < deadline)); do
    active=$(gh run list --repo "${repository}" --workflow "${workflow}" \
      --limit 20 --json status --jq '[.[] | select(.status != "completed")] | length')
    if [[ ${active} == 0 ]]; then
      return
    fi
    sleep 5
  done
  printf 'canary workflow did not become idle within 15 minutes\n' >&2
  return 1
}

discover_run() {
  local previous=$1
  local deadline=$((SECONDS + 120))
  local candidate
  while ((SECONDS < deadline)); do
    candidate=$(gh run list --repo "${repository}" --workflow "${workflow}" \
      --event workflow_dispatch --limit 1 --json databaseId,headSha,createdAt,status)
    if [[ $(jq -r '.[0].databaseId // 0' <<<"${candidate}") != "${previous}" ]] &&
      [[ $(jq -r '.[0].headSha // ""' <<<"${candidate}") == "${head_sha}" ]]; then
      jq -r '.[0].databaseId' <<<"${candidate}"
      return
    fi
    sleep 2
  done
  printf 'dispatched workflow run was not discovered\n' >&2
  return 1
}

find_diagnostic_archive() {
  local runner_name=$1
  local since_epoch=$2
  ssh -o BatchMode=yes -o ConnectTimeout=10 "${server}" bash -s -- \
    "${runner_name}" "${since_epoch}" <<'REMOTE'
set -eu
runner_name=$1
since_epoch=$2
find /var/lib/gha-fleet/diagnostics -maxdepth 1 -type f \
  -name 'runner-diagnostics-v1-*.tar.gz' -newermt "@${since_epoch}" -print0 |
  while IFS= read -r -d '' archive; do
    if tar -xOzf "${archive}" --wildcards 'runner/Runner_*.log' 2>/dev/null |
      grep -Fq "\"AgentName\": \"${runner_name}\""; then
      printf '%s\n' "${archive}"
      exit 0
    fi
  done
REMOTE
}

extract_runner_session() {
  local archive=$1
  ssh -o BatchMode=yes -o ConnectTimeout=10 "${server}" bash -s -- "${archive}" <<'REMOTE'
set -eu
archive=$1
[[ ${archive} == /var/lib/gha-fleet/diagnostics/runner-diagnostics-v1-*.tar.gz ]]
tar -xOzf "${archive}" --wildcards 'runner/Runner_*.log' 2>/dev/null |
  sed -n 's/^\[\([^]]*Z\) INFO BrokerMessageListener\] Session created\.$/\1/p' |
  tail -1
REMOTE
}

extract_runner_phases() {
  local archive=$1
  ssh -o BatchMode=yes -o ConnectTimeout=10 "${server}" bash -s -- "${archive}" <<'REMOTE'
set -eu
archive=$1
[[ ${archive} == /var/lib/gha-fleet/diagnostics/runner-diagnostics-v1-*.tar.gz ]]
tar -xOzf "${archive}" runner/nddev-direct-jit-phase.log 2>/dev/null |
  jq -sc '
    if length != 2 or
       .[0].schema_version != 1 or .[0].phase != "assignment-script-started" or
       .[1].schema_version != 1 or .[1].phase != "runner-exec" or
       (.[0].unix_ns | type) != "number" or (.[1].unix_ns | type) != "number" or
       .[1].unix_ns < .[0].unix_ns
    then error("invalid direct-JIT phase evidence") else . end
  '
REMOTE
}

garm_events() {
  local since_epoch=$1
  ssh -o BatchMode=yes -o ConnectTimeout=10 "${server}" \
    "journalctl -u garm --since '@${since_epoch}' --output=cat --no-pager" |
    jq -Rsc '[split("\n")[] | fromjson? | select(type == "object")]'
}

milliseconds_between() {
  local start=$1
  local finish=$2
  local start_ns finish_ns
  start_ns=$(date -u -d "${start}" +%s%N)
  finish_ns=$(date -u -d "${finish}" +%s%N)
  printf '%d\n' "$(((finish_ns - start_ns) / 1000000))"
}

sample_index=$(jq '.samples | length' "${output}")
while ((sample_index < required_samples)); do
  wait_for_workflow_idle
  pre=$(wait_for_preflight)
  pre_journal=$(provider_journal)
  pre_warm=$(jq -r '[.leases[] | select(.state == "warm-ready")][0].instance_name' <<<"${pre_journal}")
  [[ ${pre_warm} == warm-standard-* ]]
  pre_diagnostics=$(jq '.diagnostics.bundles' <<<"${pre}")
  pre_load=$(jq '.host.cpu.load_1' <<<"${pre}")
  previous_run=$(gh run list --repo "${repository}" --workflow "${workflow}" \
    --event workflow_dispatch --limit 1 --json databaseId --jq '.[0].databaseId // 0')
  dispatch_epoch=$(date -u +%s)
  dispatch_at=$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)

  printf 'sample %d/%d: dispatch from warm VM %s (load_1=%s)\n' \
    "$((sample_index + 1))" "${required_samples}" "${pre_warm}" "${pre_load}"
  gh workflow run "${workflow}" --repo "${repository}" --ref "${ref}" \
    -f runner_label="${runner_label}" -f mode=basic
  run_id=$(discover_run "${previous_run}")

  claim=''
  instance_metadata=''
  deadline=$((SECONDS + 900))
  while ((SECONDS < deadline)); do
    run_state=$(gh run view "${run_id}" --repo "${repository}" --json status,conclusion)
    if [[ -z ${claim} ]]; then
      current_journal=$(provider_journal)
      candidate=$(jq -c 'if (.claims | length) == 1 then .claims | to_entries[0].value else empty end' \
        <<<"${current_journal}")
      if [[ -n ${candidate} ]]; then
        claim=${candidate}
        physical=$(jq -r .instance_name <<<"${claim}")
        [[ ${physical} == warm-standard-* ]]
        instance_metadata=$(ssh -o BatchMode=yes -o ConnectTimeout=10 "${server}" \
          "incus config show '${physical}' --project gha-fleet --expanded" |
          awk -F ': ' '
            $1 == "  user.nddev.garm-job-name" {job=$2}
            $1 == "  user.nddev.provider-version" {version=$2}
            $1 == "  user.nddev.provider-commit" {commit=$2}
            END {printf "{\"job_name\":\"%s\",\"provider_version\":\"%s\",\"provider_commit\":\"%s\"}\n", job, version, commit}
          ')
      fi
    fi
    if [[ $(jq -r .status <<<"${run_state}") == completed ]]; then
      break
    fi
    sleep 2
  done
  [[ $(jq -r .status <<<"${run_state}") == completed ]]
  [[ $(jq -r .conclusion <<<"${run_state}") == success ]] || {
    printf 'run %s did not succeed: %s\n' "${run_id}" "${run_state}" >&2
    exit 1
  }
  [[ -n ${claim} && -n ${instance_metadata} ]] || {
    printf 'run %s completed without an observed durable warm claim\n' "${run_id}" >&2
    exit 1
  }

  jobs=$(gh api "repos/${repository}/actions/runs/${run_id}/jobs?filter=latest")
  job=$(jq -c '[.jobs[] | select(.name == "Official runner one-job canary")][0]' <<<"${jobs}")
  [[ ${job} != null ]]
  job_id=$(jq -r .id <<<"${job}")
  runner_name=$(jq -r .runner_name <<<"${job}")
  [[ ${runner_name} =~ ^nddev-[a-z0-9]+$ ]]
  [[ $(jq -r .conclusion <<<"${job}") == success ]]
  [[ $(jq -r .job_name <<<"${claim}") == "${runner_name}" ]]
  [[ $(jq -r .instance_name <<<"${claim}") == "${pre_warm}" ]]
  jq -e \
    --arg runner "${runner_name}" \
    --arg version "${provider_version}" \
    --arg commit "${provider_commit}" \
    '.job_name == $runner and .provider_version == $version and .provider_commit == $commit' \
    <<<"${instance_metadata}" >/dev/null

  events=$(garm_events "${dispatch_epoch}")
  started_event=$(jq -c --arg runner "${runner_name}" \
    '[.[] | select(.msg == "job started" and .runner_name == $runner)] | last' <<<"${events}")
  opaque_job_id=$(jq -r .job_id <<<"${started_event}")
  assignment_event=$(jq -c --arg job_id "${opaque_job_id}" \
    '[.[] | select(.msg == "new job assigned" and .job_id == $job_id)] | last' <<<"${events}")
  completed_event=$(jq -c --arg runner "${runner_name}" \
    '[.[] | select(.msg == "job completed" and .runner_name == $runner)] | last' <<<"${events}")
  [[ ${started_event} != null && ${assignment_event} != null && ${completed_event} != null ]]
  assignment_at=$(jq -r .time <<<"${assignment_event}")
  provider_started_event=$(jq -c --arg runner "${runner_name}" \
    '[.[] | select(.msg == "direct JIT phase" and .phase == "provider-create-started" and .runner_name == $runner)] | last' <<<"${events}")
  provider_completed_event=$(jq -c --arg runner "${runner_name}" \
    '[.[] | select(.msg == "direct JIT phase" and .phase == "provider-create-completed" and .runner_name == $runner)] | last' <<<"${events}")
  [[ ${provider_started_event} != null && ${provider_completed_event} != null ]]
  provider_started_at=$(jq -r .time <<<"${provider_started_event}")
  provider_completed_at=$(jq -r .time <<<"${provider_completed_event}")
  provider_duration_ms=$(jq -r .duration_ms <<<"${provider_completed_event}")
  assignment_to_provider_start_ms=$(milliseconds_between "${assignment_at}" "${provider_started_at}")
  assignment_to_provider_complete_ms=$(milliseconds_between "${assignment_at}" "${provider_completed_at}")

  archive=''
  deadline=$((SECONDS + 180))
  while ((SECONDS < deadline)); do
    archive=$(find_diagnostic_archive "${runner_name}" "${dispatch_epoch}")
    if [[ -n ${archive} ]]; then
      break
    fi
    sleep 2
  done
  [[ ${archive} == /var/lib/gha-fleet/diagnostics/runner-diagnostics-v1-*.tar.gz ]]
  session_at=$(extract_runner_session "${archive}")
  [[ ${session_at} =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}.*Z$ ]]
  runner_phases=$(extract_runner_phases "${archive}")
  assignment_script_started_ns=$(jq -r '.[0].unix_ns' <<<"${runner_phases}")
  runner_exec_ns=$(jq -r '.[1].unix_ns' <<<"${runner_phases}")
  guest_setup_ms=$(((runner_exec_ns - assignment_script_started_ns) / 1000000))
  latency_ms=$(milliseconds_between "$(jq -r .created_at <<<"${job}")" "$(jq -r .started_at <<<"${job}")")
  ((latency_ms >= 0))
  diagnostic_sha=$(ssh -o BatchMode=yes -o ConnectTimeout=10 "${server}" \
    "sha256sum '${archive}'" | awk '{print $1}')
  [[ ${diagnostic_sha} =~ ^[0-9a-f]{64}$ ]]
  token_matches=$(ssh -o BatchMode=yes -o ConnectTimeout=10 "${server}" bash -s -- "${archive}" <<'REMOTE'
set -eu
archive=$1
[[ ${archive} == /var/lib/gha-fleet/diagnostics/runner-diagnostics-v1-*.tar.gz ]]
tar -xOzf "${archive}" 2>/dev/null |
  awk '
    BEGIN { count=0 }
    /gh[pousr]_[A-Za-z0-9_]{20,}/ { count++ }
    /github_pat_[A-Za-z0-9_]{20,}/ { count++ }
    /eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}/ { count++ }
    END { print count }
  '
REMOTE
  )
  [[ ${token_matches} == 0 ]]

  replacement=''
  post=''
  deadline=$((SECONDS + 600))
  while ((SECONDS < deadline)); do
    post=$(snapshot)
    post_journal=$(provider_journal)
    replacement=$(jq -r '[.leases[] | select(.state == "warm-ready")][0].instance_name // ""' \
      <<<"${post_journal}")
    if [[ ${replacement} == warm-standard-* && ${replacement} != "${pre_warm}" ]] &&
      jq -e \
        --argjson minimum_bundles "$((pre_diagnostics + 1))" \
        '.healthy and
         .journal.leases == 1 and .journal.claims == 0 and
         .journal.by_state["warm-ready"] == 1 and
         .queue.active == 0 and .queue.in_flight == 0 and
         .queue.uncovered_running == 0 and
         .incus.visible_instances == 1 and
         .incus.orphan_instances == 0 and .incus.missing_instances == 0 and
         .diagnostics.bundles >= $minimum_bundles and
         .diagnostic_export_sync.state == "synchronized" and
         .diagnostic_export.pending_bundles == 0' <<<"${post}" >/dev/null; then
      break
    fi
    sleep 5
  done
  [[ ${replacement} == warm-standard-* && ${replacement} != "${pre_warm}" ]]
  [[ $(failed_units) == 0 ]]
  post_retained=$(retained_state)
  jq -e '.legacy_listeners == 12 and .example_platform_healthy and .captcha_healthy' \
    <<<"${post_retained}" >/dev/null
  registrations=$(gh api "repos/${repository}/actions/runners" --jq .total_count)
  [[ ${registrations} == 0 ]]

  sample=$(jq -n \
    --argjson index "$((sample_index + 1))" \
    --argjson run_id "${run_id}" \
    --argjson job_id "${job_id}" \
    --arg opaque_job_id "${opaque_job_id}" \
    --arg dispatch_at "${dispatch_at}" \
    --arg created_at "$(jq -r .created_at <<<"${job}")" \
    --arg started_at "$(jq -r .started_at <<<"${job}")" \
    --arg completed_at "$(jq -r .completed_at <<<"${job}")" \
    --arg assignment_at "${assignment_at}" \
    --arg provider_started_at "${provider_started_at}" \
    --arg provider_completed_at "${provider_completed_at}" \
    --arg session_at "${session_at}" \
    --arg runner_name "${runner_name}" \
    --arg physical_instance "${pre_warm}" \
    --arg replacement_instance "${replacement}" \
    --arg diagnostic_archive "$(basename -- "${archive}")" \
    --arg diagnostic_sha256 "${diagnostic_sha}" \
    --argjson latency_ms "${latency_ms}" \
    --argjson provider_duration_ms "${provider_duration_ms}" \
    --argjson assignment_to_provider_start_ms "${assignment_to_provider_start_ms}" \
    --argjson assignment_to_provider_complete_ms "${assignment_to_provider_complete_ms}" \
    --argjson assignment_script_started_ns "${assignment_script_started_ns}" \
    --argjson runner_exec_ns "${runner_exec_ns}" \
    --argjson guest_setup_ms "${guest_setup_ms}" \
    --argjson pre_load_1 "${pre_load}" \
    --argjson legacy_workers "$(jq '.host.legacy_runners.workers' <<<"${pre}")" \
    --argjson token_matches "${token_matches}" \
    --argjson diagnostics_before "${pre_diagnostics}" \
    --argjson diagnostics_after "$(jq '.diagnostics.bundles' <<<"${post}")" \
    --argjson post_snapshot "${post}" \
    --argjson retained "${post_retained}" \
    '{
      index: $index,
      workflow_run_id: $run_id,
      job_id: $job_id,
      opaque_job_id: $opaque_job_id,
      dispatch_at: $dispatch_at,
      created_at: $created_at,
      started_at: $started_at,
      completed_at: $completed_at,
      garm_assignment_at: $assignment_at,
      provider_started_at: $provider_started_at,
      provider_completed_at: $provider_completed_at,
      runner_session_at: $session_at,
      latency_milliseconds: $latency_ms,
      phase_durations: {
        assignment_to_provider_start_milliseconds: $assignment_to_provider_start_ms,
        provider_create_milliseconds: $provider_duration_ms,
        assignment_to_provider_complete_milliseconds: $assignment_to_provider_complete_ms,
        guest_assignment_setup_milliseconds: $guest_setup_ms
      },
      guest_phase_clock: {
        assignment_script_started_unix_ns: $assignment_script_started_ns,
        runner_exec_unix_ns: $runner_exec_ns
      },
      preflight: {load_1: $pre_load_1, legacy_workers: $legacy_workers},
      runner_name: $runner_name,
      physical_instance: $physical_instance,
      replacement_instance: $replacement_instance,
      diagnostic_archive: $diagnostic_archive,
      diagnostic_sha256: $diagnostic_sha256,
      diagnostic_token_shape_matches: $token_matches,
      diagnostics_before: $diagnostics_before,
      diagnostics_after: $diagnostics_after,
      postcondition: {
        healthy: $post_snapshot.healthy,
        queue_active: $post_snapshot.queue.active,
        queue_in_flight: $post_snapshot.queue.in_flight,
        queue_uncovered_running: $post_snapshot.queue.uncovered_running,
        claims: $post_snapshot.journal.claims,
        warm_ready: $post_snapshot.journal.by_state["warm-ready"],
        visible_instances: $post_snapshot.incus.visible_instances,
        orphan_instances: $post_snapshot.incus.orphan_instances,
        missing_instances: $post_snapshot.incus.missing_instances,
        diagnostic_sync: $post_snapshot.diagnostic_export_sync.state,
        diagnostic_pending: $post_snapshot.diagnostic_export.pending_bundles,
        legacy_listeners: $retained.legacy_listeners,
        example_platform_healthy: $retained.example_platform_healthy,
        captcha_healthy: $retained.captcha_healthy,
        failed_systemd_units: 0,
        github_runner_registrations: 0
      }
    }')
  # The dollar-prefixed name is a jq variable, not a shell expansion.
  # shellcheck disable=SC2016
  atomic_filter '.samples += [$sample]' --argjson sample "${sample}"
  sample_index=$((sample_index + 1))
  printf 'sample %d/%d: %d ms, run %s, %s -> %s\n' \
    "${sample_index}" "${required_samples}" "${latency_ms}" "${run_id}" \
    "${pre_warm}" "${replacement}"
done

statistics=$(jq -c \
  --argjson target "${target_milliseconds}" '
    [.samples[].latency_milliseconds] | sort as $values |
    ($values | length) as $n |
    {
      samples: $n,
      minimum_milliseconds: $values[0],
      maximum_milliseconds: $values[-1],
      median_milliseconds: $values[((($n * 50 + 99) / 100 | floor) - 1)],
      p95_milliseconds: $values[((($n * 95 + 99) / 100 | floor) - 1)],
      target_milliseconds_exclusive: $target,
      passed: ($values[((($n * 95 + 99) / 100 | floor) - 1)] < $target)
    }
  ' "${output}")
final_snapshot=$(snapshot)
final_retained=$(retained_state)
final_failed_units=$(failed_units)
final_registrations=$(gh api "repos/${repository}/actions/runners" --jq .total_count)
final_post=$(jq -n \
  --arg completed_at "$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)" \
  --argjson snapshot "${final_snapshot}" \
  --argjson retained "${final_retained}" \
  --argjson failed_units "${final_failed_units}" \
  --argjson registrations "${final_registrations}" '
  {
    completed_at: $completed_at,
    observer_healthy: $snapshot.healthy,
    queue_active: $snapshot.queue.active,
    queue_in_flight: $snapshot.queue.in_flight,
    queue_uncovered_running: $snapshot.queue.uncovered_running,
    claims: $snapshot.journal.claims,
    warm_ready: $snapshot.journal.by_state["warm-ready"],
    visible_instances: $snapshot.incus.visible_instances,
    orphan_instances: $snapshot.incus.orphan_instances,
    missing_instances: $snapshot.incus.missing_instances,
    diagnostics_source: $snapshot.diagnostic_export.source_bundles,
    diagnostics_exported: $snapshot.diagnostic_export.exported_bundles,
    diagnostics_pending: $snapshot.diagnostic_export.pending_bundles,
    legacy_listeners: $retained.legacy_listeners,
    example_platform_healthy: $retained.example_platform_healthy,
    captcha_healthy: $retained.captcha_healthy,
    failed_systemd_units: $failed_units,
    github_runner_registrations: $registrations
  }')
# Dollar-prefixed names in this filter are jq variables.
# shellcheck disable=SC2016
atomic_filter \
  '.statistics = $statistics |
   .postconditions = $post |
   .verdict.complete = ((.samples | length) == .series.required_samples) |
   .verdict.warm_queue_to_online_p95_gate_complete = $statistics.passed' \
  --argjson statistics "${statistics}" --argjson post "${final_post}"

jq '{statistics,postconditions,verdict}' "${output}"
jq -e '
  .verdict.complete and
  .verdict.warm_queue_to_online_p95_gate_complete and
  .postconditions.observer_healthy and
  .postconditions.queue_active == 0 and
  .postconditions.queue_in_flight == 0 and
  .postconditions.queue_uncovered_running == 0 and
  .postconditions.claims == 0 and
  .postconditions.warm_ready == 1 and
  .postconditions.visible_instances == 1 and
  .postconditions.orphan_instances == 0 and
  .postconditions.missing_instances == 0 and
  .postconditions.diagnostics_source == .postconditions.diagnostics_exported and
  .postconditions.diagnostics_pending == 0 and
  .postconditions.legacy_listeners == 12 and
  .postconditions.example_platform_healthy and
  .postconditions.captcha_healthy and
  .postconditions.failed_systemd_units == 0 and
  .postconditions.github_runner_registrations == 0
' "${output}" >/dev/null
