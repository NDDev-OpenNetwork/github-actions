#!/usr/bin/env bash
set -Eeuo pipefail

repository=${GHA_REPOSITORY:-NDDev-OpenNetwork/github-actions}
workflow=representative-benchmark.yml
ref=${GHA_REF:-main}
server=${GHA_SERVER:-server-example-legacy}
samples_per_mode=${GHA_REQUIRED_SAMPLES:-20}
max_load_1=${GHA_MAX_LOAD_1:-4}
output=${1:-config/sccache-statistical-audit.json}

[[ ${samples_per_mode} =~ ^[1-9][0-9]*$ ]]
[[ ${max_load_1} =~ ^[0-9]+([.][0-9]+)?$ ]]
command -v gh >/dev/null
command -v jq >/dev/null
command -v ssh >/dev/null
command -v unzip >/dev/null
gh auth status >/dev/null

head_sha=$(gh api "repos/${repository}/commits/${ref}" --jq .sha)
[[ ${head_sha} =~ ^[0-9a-f]{40}$ ]]

atomic_filter() {
  local filter=$1
  shift
  local temporary
  temporary=$(mktemp "${output}.tmp.XXXXXX")
  jq "$@" "${filter}" "${output}" >"${temporary}"
  mv -- "${temporary}" "${output}"
}

if [[ -e ${output} ]]; then
  jq -e --arg repository "${repository}" --arg head "${head_sha}" \
    --argjson required "${samples_per_mode}" \
    '.schema_version == 1 and .series.repository == $repository and
     .series.head_sha == $head and .series.samples_per_mode == $required and
     (.cold_samples | type == "array") and (.warm_samples | type == "array")' \
    "${output}" >/dev/null || {
      printf 'existing output does not match this immutable series\n' >&2
      exit 2
    }
else
  output_directory=$(dirname -- "${output}")
  if [[ ! -d ${output_directory} ]]; then
    install -d -m 0755 "${output_directory}"
  fi
  jq -n --arg captured_at "$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)" \
    --arg repository "${repository}" --arg workflow "${workflow}" \
    --arg ref "${ref}" --arg head_sha "${head_sha}" \
    --arg server "${server}" --argjson samples "${samples_per_mode}" \
    --argjson max_load "${max_load_1}" \
    '{schema_version:1,captured_at:$captured_at,series:{repository:$repository,
      workflow:$workflow,ref:$ref,head_sha:$head_sha,server:$server,
      workload:"rust",environment:"nddev",samples_per_mode:$samples,
      nominal_preflight_max_load_1:$max_load,quantile_method:"nearest-rank",
      required_cache_hit_rate_percent_exclusive:70,
      required_median_speedup_factor:3},cold_samples:[],warm_samples:[],
      statistics:null,postconditions:null,verdict:{complete:false,
      cache_hit_rate_gate_complete:false,median_speedup_gate_complete:false,
      statistical_cache_gate_complete:false}}' >"${output}"
fi

snapshot() {
  ssh -o BatchMode=yes -o ConnectTimeout=10 "${server}" \
    'curl --fail --silent http://127.0.0.1:9464/snapshot'
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
      $1 == "example-platform-backend" || $1 == "example-platform-ml-server" {found++; if ($2 !~ /healthy/) bad++}
      END {print (found == 2 && bad == 0) ? "true" : "false"}'\'')
    captcha=$(docker ps --format "{{.Names}}|{{.Status}}" | awk -F "|" '\''
      $1 == "nddev-captcha" || $1 == "nddev-captcha-valkey" {found++; if ($2 !~ /healthy/) bad++}
      END {print (found == 2 && bad == 0) ? "true" : "false"}'\'')
    jq -n --argjson listeners "${listeners}" --argjson example-platform "${example-platform}" --argjson captcha "${captcha}" \
      "{legacy_listeners:\$listeners,example_platform_healthy:\$example-platform,captcha_healthy:\$captcha}"
  '
}

wait_preflight() {
  local deadline=$((SECONDS + 1800)) observed
  while ((SECONDS < deadline)); do
    observed=$(snapshot)
    if jq -e --argjson max_load "${max_load_1}" \
      '.healthy and .host.cpu.load_1 <= $max_load and
       .host.legacy_runners.listeners == 12 and
       .journal.leases == 1 and .journal.claims == 0 and
       .journal.by_state["warm-ready"] == 1 and
       .queue.active == 0 and .queue.in_flight == 0 and
       .incus.visible_instances == 1 and .incus.orphan_instances == 0 and
       .incus.missing_instances == 0 and
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
  printf 'nominal preflight did not converge\n' >&2
  return 1
}

wait_idle() {
  local active deadline=$((SECONDS + 1800))
  while ((SECONDS < deadline)); do
    active=$(gh run list --repo "${repository}" --workflow "${workflow}" \
      --limit 20 --json status --jq '[.[] | select(.status != "completed")] | length')
    [[ ${active} == 0 ]] && return
    sleep 5
  done
  return 1
}

discover_run() {
  local previous=$1 deadline=$((SECONDS + 120)) candidate
  while ((SECONDS < deadline)); do
    candidate=$(gh run list --repo "${repository}" --workflow "${workflow}" \
      --event workflow_dispatch --limit 1 --json databaseId,headSha)
    if [[ $(jq -r '.[0].databaseId // 0' <<<"${candidate}") != "${previous}" ]] &&
      [[ $(jq -r '.[0].headSha // ""' <<<"${candidate}") == "${head_sha}" ]]; then
      jq -r '.[0].databaseId' <<<"${candidate}"
      return
    fi
    sleep 2
  done
  return 1
}

run_sample() {
  local mode=$1 index=$2 pre previous run_id run jobs rust_job artifact artifact_name
  local temporary record logs stats hits=0 misses=0 secret_matches elapsed machine
  wait_idle
  pre=$(wait_preflight)
  previous=$(gh run list --repo "${repository}" --workflow "${workflow}" \
    --event workflow_dispatch --limit 1 --json databaseId --jq '.[0].databaseId // 0')
  printf '%s sample %d/%d: dispatch (load_1=%s)\n' "${mode}" "${index}" \
    "${samples_per_mode}" "$(jq -r .host.cpu.load_1 <<<"${pre}")"
  gh workflow run "${workflow}" --repo "${repository}" --ref "${ref}" \
    -f environment=nddev -f cache_mode="${mode}" \
    -f iteration="sccache-${mode}-$(printf '%02d' "${index}")" -f workload=rust
  run_id=$(discover_run "${previous}")
  if ! gh run watch "${run_id}" --repo "${repository}" --exit-status --interval 5 \
    >/dev/null 2>&1; then
    gh run view "${run_id}" --repo "${repository}"
    return 1
  fi
  run=$(gh run view "${run_id}" --repo "${repository}" \
    --json status,conclusion,headSha,event,createdAt,updatedAt)
  jq -e --arg head "${head_sha}" \
    '.status == "completed" and .conclusion == "success" and .headSha == $head and
     .event == "workflow_dispatch"' <<<"${run}" >/dev/null
  jobs=$(gh api "repos/${repository}/actions/runs/${run_id}/jobs?filter=latest")
  [[ $(jq '.total_count' <<<"${jobs}") == 5 ]]
  jq -e '[.jobs[] | select(.name != "Rust build and test") | .conclusion] | all(. == "skipped")' \
    <<<"${jobs}" >/dev/null
  rust_job=$(jq -c '[.jobs[] | select(.name == "Rust build and test")][0]' <<<"${jobs}")
  jq -e '.conclusion == "success" and (.runner_name | startswith("nddev-"))' \
    <<<"${rust_job}" >/dev/null
  artifact=$(gh api "repos/${repository}/actions/runs/${run_id}/artifacts" \
    --jq '.artifacts | if length == 1 then .[0] else error("expected one artifact") end')
  artifact_name="benchmark-nddev-${mode}-sccache-${mode}-$(printf '%02d' "${index}")-rust"
  jq -e --arg name "${artifact_name}" --arg head "${head_sha}" \
    '.name == $name and .expired == false and .workflow_run.head_sha == $head and
     (.digest | test("^sha256:[0-9a-f]{64}$"))' <<<"${artifact}" >/dev/null
  temporary=$(mktemp -d)
  gh run download "${run_id}" --repo "${repository}" --name "${artifact_name}" \
    --dir "${temporary}"
  record=$(jq -c . "${temporary}/result.json")
  rm -f -- "${temporary}/result.json"
  rmdir -- "${temporary}"
  jq -e --arg mode "${mode}" --arg head "${head_sha}" --argjson run_id "${run_id}" \
    '.schema_version == 1 and .workload == "rust" and .environment == "nddev" and
     .cache_mode == $mode and .commit == $head and .run_id == $run_id and
     (.elapsed_ns | type == "number" and . > 0) and
     (.machine_id_sha256 | test("^[0-9a-f]{64}$"))' <<<"${record}" >/dev/null
  if [[ ${mode} == cold ]]; then
    jq -e '.cache_hit == "disabled"' <<<"${record}" >/dev/null
  else
    jq -e '.cache_hit == "true"' <<<"${record}" >/dev/null
  fi
  logs=$(gh run view "${run_id}" --repo "${repository}" --log)
  secret_matches=$(grep -Eoc 'gh[pousr]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{20,}|eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}' <<<"${logs}" || true)
  [[ ${secret_matches} == 0 ]]
  if [[ ${mode} == warm ]]; then
    stats=$(sed -n 's/.*sccache hits=\([0-9][0-9]*\) misses=\([0-9][0-9]*\).*/\1 \2/p' <<<"${logs}" | tail -1)
    read -r hits misses <<<"${stats}"
    [[ ${hits} =~ ^[1-9][0-9]*$ && ${misses} =~ ^[0-9]+$ ]]
  fi
  elapsed=$(jq -r .elapsed_ns <<<"${record}")
  machine=$(jq -r .machine_id_sha256 <<<"${record}")
  sample=$(jq -n --argjson index "${index}" --argjson run_id "${run_id}" \
    --argjson job_id "$(jq -r .id <<<"${rust_job}")" --arg runner "$(jq -r .runner_name <<<"${rust_job}")" \
    --arg machine "${machine}" --arg artifact_name "${artifact_name}" \
    --argjson artifact_id "$(jq -r .id <<<"${artifact}")" --arg digest "$(jq -r .digest <<<"${artifact}")" \
    --argjson elapsed_ns "${elapsed}" --argjson network_rx_bytes "$(jq -r .network_rx_bytes <<<"${record}")" \
    --argjson hits "${hits}" --argjson misses "${misses}" --argjson secret_matches "${secret_matches}" \
    '{index:$index,workflow_run_id:$run_id,job_id:$job_id,runner_name:$runner,
      machine_id_sha256:$machine,artifact_name:$artifact_name,artifact_id:$artifact_id,
      artifact_digest:$digest,elapsed_ns:$elapsed_ns,network_rx_bytes:$network_rx_bytes,
      sccache_hits:$hits,sccache_misses:$misses,github_log_secret_shape_matches:$secret_matches}')
  atomic_filter ".${mode}_samples += [\$sample]" --argjson sample "${sample}"
  printf '%s sample %d/%d: %.3f seconds, hits=%d misses=%d\n' "${mode}" "${index}" \
    "${samples_per_mode}" "$(awk -v ns="${elapsed}" 'BEGIN {print ns/1000000000}')" "${hits}" "${misses}"
}

for mode in cold warm; do
  completed=$(jq ".${mode}_samples | length" "${output}")
  while ((completed < samples_per_mode)); do
    run_sample "${mode}" "$((completed + 1))"
    completed=$((completed + 1))
  done
done

post_snapshot=$(wait_preflight)
post_retained=$(retained_state)
post=$(jq -n --argjson snapshot "${post_snapshot}" --argjson retained "${post_retained}" \
  --argjson failed_units "$(failed_units)" \
  --argjson registrations "$(gh api "repos/${repository}/actions/runners" --jq .total_count)" \
  '{observer_healthy:$snapshot.healthy,queue_active:$snapshot.queue.active,
    queue_in_flight:$snapshot.queue.in_flight,claims:$snapshot.journal.claims,
    warm_ready:$snapshot.journal.by_state["warm-ready"],
    visible_instances:$snapshot.incus.visible_instances,
    orphan_instances:$snapshot.incus.orphan_instances,
    missing_instances:$snapshot.incus.missing_instances,
    diagnostics_source:$snapshot.diagnostic_export.source_bundles,
    diagnostics_exported:$snapshot.diagnostic_export.exported_bundles,
    diagnostics_pending:$snapshot.diagnostic_export.pending_bundles,
    legacy_listeners:$retained.legacy_listeners,
    example_platform_healthy:$retained.example_platform_healthy,captcha_healthy:$retained.captcha_healthy,
    failed_systemd_units:$failed_units,github_runner_registrations:$registrations}')
# The single-quoted expression is a jq program; its dollar-prefixed names are
# jq bindings and must not be expanded by the shell.
# shellcheck disable=SC2016
atomic_filter '
  def nearest_median: sort | .[((length + 1) / 2 | floor) - 1];
  ([.cold_samples[].elapsed_ns] | nearest_median) as $cold |
  ([.warm_samples[].elapsed_ns] | nearest_median) as $warm |
  ([.warm_samples[].sccache_hits] | add) as $hits |
  ([.warm_samples[].sccache_misses] | add) as $misses |
  ([.cold_samples[].machine_id_sha256,.warm_samples[].machine_id_sha256] | unique | length) as $machines |
  ([.cold_samples[].artifact_digest,.warm_samples[].artifact_digest] | unique | length) as $artifacts |
  (($hits * 10000 / ($hits + $misses) | floor) / 100) as $rate |
  (($cold * 1000 / $warm | floor) / 1000) as $speedup |
  .statistics = {cold_median_ns:$cold,warm_median_ns:$warm,
    median_speedup_factor:$speedup,total_warm_hits:$hits,total_warm_misses:$misses,
    cache_hit_rate_percent:$rate,unique_machine_ids:$machines,unique_artifact_digests:$artifacts} |
  .postconditions = $post |
  .verdict.complete = ((.cold_samples|length) == .series.samples_per_mode and
    (.warm_samples|length) == .series.samples_per_mode and
    $machines == (.series.samples_per_mode * 2) and
    $artifacts == (.series.samples_per_mode * 2) and
    $post.observer_healthy and $post.queue_active == 0 and $post.queue_in_flight == 0 and
    $post.claims == 0 and $post.warm_ready == 1 and $post.visible_instances == 1 and
    $post.orphan_instances == 0 and $post.missing_instances == 0 and
    $post.diagnostics_source == $post.diagnostics_exported and $post.diagnostics_pending == 0 and
    $post.legacy_listeners == 12 and $post.example_platform_healthy and $post.captcha_healthy and
    $post.failed_systemd_units == 0 and $post.github_runner_registrations == 0) |
  .verdict.cache_hit_rate_gate_complete = ($rate > .series.required_cache_hit_rate_percent_exclusive) |
  .verdict.median_speedup_gate_complete = ($speedup >= .series.required_median_speedup_factor) |
  .verdict.statistical_cache_gate_complete = (.verdict.complete and
    .verdict.cache_hit_rate_gate_complete and .verdict.median_speedup_gate_complete)' \
  --argjson post "${post}"

jq '{statistics,postconditions,verdict}' "${output}"
jq -e '.verdict.statistical_cache_gate_complete' "${output}" >/dev/null
