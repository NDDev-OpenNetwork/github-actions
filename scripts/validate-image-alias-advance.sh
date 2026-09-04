#!/usr/bin/env bash
set -euo pipefail

base_ref="${1:?base ref is required}"
head_ref="${2:-HEAD}"

manifests=(
  config/golden-image.yaml
  config/golden-image-integration.yaml
  config/golden-image-container.yaml
  config/golden-image-container-integration.yaml
)

alias_at() {
  git show "$1:$2" | awk '$1 == "alias:" { print $2; exit }'
}

manifest_payload_at() {
  git show "$1:$2" |
    awk '
      /^[[:space:]]*($|#)/ { next }
      /^[[:space:]]*alias:[[:space:]]/ { next }
      { sub(/[[:space:]]+#.*$/, ""); print }
    '
}

changed_payload() {
  ! cmp -s \
    <(manifest_payload_at "${base_ref}" "$1") \
    <(manifest_payload_at "${head_ref}" "$1")
}

require_alias_advance() {
  local manifest="$1"
  local reason="$2"
  local before after
  before="$(alias_at "${base_ref}" "${manifest}")"
  after="$(alias_at "${head_ref}" "${manifest}")"
  if [[ -z "${before}" || -z "${after}" || "${before}" == "${after}" ]]; then
    printf '%s changed image bytes for %s without advancing its immutable alias (%s)\n' \
      "${reason}" "${manifest}" "${before:-missing}" >&2
    return 1
  fi
}

for manifest in "${manifests[@]}"; do
  if changed_payload "${manifest}"; then
    require_alias_advance "${manifest}" "manifest payload"
  fi
done

if ! git diff --quiet "${base_ref}" "${head_ref}" -- internal/imagebuild/assets/provision.sh; then
  for manifest in "${manifests[@]}"; do
    require_alias_advance "${manifest}" "shared provisioning"
  done
fi

if ! git diff --quiet "${base_ref}" "${head_ref}" -- internal/imagebuild/assets/docker-provision.sh; then
  require_alias_advance config/golden-image-integration.yaml "Docker provisioning"
  require_alias_advance config/golden-image-container-integration.yaml "Docker provisioning"
fi
