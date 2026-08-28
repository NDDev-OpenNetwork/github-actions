#!/usr/bin/env bash
set -Eeuo pipefail
set +x
umask 077

operation=${NDDEV_PACKAGE_CACHE_OPERATION:?}
ecosystem=${NDDEV_PACKAGE_CACHE_ECOSYSTEM:?}
runtime=${NDDEV_PACKAGE_CACHE_RUNTIME:?}
lock_input=${NDDEV_PACKAGE_CACHE_LOCK_FILE:?}

case "$operation" in restore|save) ;; *) echo "unsupported package-cache operation" >&2; exit 2 ;; esac
case "$ecosystem" in go|npm|pnpm|yarn|bun|uv|pip|cargo|maven|gradle|pub) ;; *) echo "unsupported package-cache ecosystem" >&2; exit 2 ;; esac
[[ "$runtime" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ ]]
: "${GITHUB_WORKSPACE:?}"
: "${GITHUB_OUTPUT:?}"
: "${HOME:?}"

printf 'enabled=false\nhit=false\n' >>"$GITHUB_OUTPUT"

assignment_names=(
  AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_REGION AWS_CA_BUNDLE
  SCCACHE_BUCKET SCCACHE_ENDPOINT NDDEV_CACHE_DELIVERY_ID
  NDDEV_CACHE_ROLE NDDEV_CACHE_MODE NDDEV_CACHE_PREFIX_ROOT
)
present=0
for name in "${assignment_names[@]}"; do
  [[ -n "${!name:-}" ]] && ((present += 1))
done
if (( present == 0 )); then
  printf 'NDDev package cache disabled: no fleet cache assignment\n'
  exit 0
fi
if (( present != ${#assignment_names[@]} )); then
  echo "incomplete NDDev cache assignment" >&2
  exit 1
fi

[[ "$AWS_ACCESS_KEY_ID" =~ ^AKIA[0-9A-F]{16}$ ]]
[[ "$AWS_SECRET_ACCESS_KEY" =~ ^[A-Za-z0-9_-]{64}$ ]]
[[ "$AWS_REGION" =~ ^[a-z]{2}-[a-z]+-[1-9][0-9]*$ ]]
[[ "$SCCACHE_BUCKET" =~ ^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]-cache$ ]]
[[ "$SCCACHE_ENDPOINT" == https://* ]]
endpoint_authority=${SCCACHE_ENDPOINT#https://}
[[ -n "$endpoint_authority" && "$endpoint_authority" != *['/?#@ ']* ]]
[[ "$NDDEV_CACHE_DELIVERY_ID" =~ ^[0-9a-f]{64}$ ]]
case "$NDDEV_CACHE_ROLE:$NDDEV_CACHE_MODE" in
  trusted-writer:read-write|untrusted-writer:read-write|release-reader:read-only) ;;
  *) echo "unsupported NDDev cache trust assignment" >&2; exit 1 ;;
esac
[[ "$NDDEV_CACHE_PREFIX_ROOT" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/trust/(trusted|untrusted|promoted)$ ]]
test -f "$AWS_CA_BUNDLE"
test ! -L "$AWS_CA_BUNDLE"

workspace=$(realpath --canonicalize-existing -- "$GITHUB_WORKSPACE")
case "$lock_input" in /*) lock_candidate=$lock_input ;; *) lock_candidate=$workspace/$lock_input ;; esac
lock_file=$(realpath --canonicalize-existing -- "$lock_candidate")
case "$lock_file" in "$workspace"/*) ;; *) echo "package-cache lock file is outside GITHUB_WORKSPACE" >&2; exit 1 ;; esac
test ! -L "$lock_candidate"
test -f "$lock_file"
lock_bytes=$(stat --format='%s' -- "$lock_file")
[[ "$lock_bytes" =~ ^[0-9]+$ ]]
(( lock_bytes > 0 && lock_bytes <= 16 * 1024 * 1024 ))
lock_sha=$(sha256sum -- "$lock_file" | cut -d' ' -f1)
key_sha=$(printf 'nddev-package-cache-v1\0%s\0%s\0%s\0%s\0%s' \
  "$ecosystem" "$runtime" "$lock_sha" "${RUNNER_OS:-Linux}" "${RUNNER_ARCH:-X64}" | \
  sha256sum | cut -d' ' -f1)
printf 'key_sha256=%s\nenabled=true\n' "$key_sha" >>"$GITHUB_OUTPUT"

cache_paths=()
case "$ecosystem" in
  go) cache_paths+=("$(go env GOMODCACHE)") ;;
  npm) cache_paths+=("$(npm config get cache)") ;;
  pnpm) cache_paths+=("$(pnpm store path --silent)") ;;
  yarn) cache_paths+=("$(yarn config get globalFolder)") ;;
  bun) cache_paths+=("$(bun pm cache)") ;;
  uv) cache_paths+=("$(uv cache dir)") ;;
  pip) cache_paths+=("$(python -m pip cache dir)") ;;
  cargo)
    cargo_home=${CARGO_HOME:-$HOME/.cargo}
    cache_paths+=("$cargo_home/registry" "$cargo_home/git")
    ;;
  pub)
    # Dart has no command that prints its cache directory, and the default has
    # moved between SDK versions, so the location is not guessed. Every Flutter
    # setup action exports PUB_CACHE before dependency resolution; requiring it
    # fails loudly rather than caching some other directory that happens to
    # exist.
    if [[ -z "${PUB_CACHE:-}" ]]; then
      echo "package-cache pub requires PUB_CACHE to be exported by the Flutter or Dart setup step" >&2
      exit 2
    fi
    cache_paths+=("$PUB_CACHE")
    ;;
  maven) cache_paths+=("$HOME/.m2/repository") ;;
  gradle)
    gradle_home=${GRADLE_USER_HOME:-$HOME/.gradle}
    cache_paths+=("$gradle_home/caches/modules-2" "$gradle_home/wrapper/dists")
    ;;
esac

home=$(realpath --canonicalize-existing -- "$HOME")
relative_paths=()
for path in "${cache_paths[@]}"; do
  [[ "$path" == /* ]]
  case "$path" in "$home"/*) ;; *) echo "package-cache path is outside HOME" >&2; exit 1 ;; esac
  relative=${path#"$home"/}
  [[ "$relative" =~ ^[A-Za-z0-9._+-]+(/[A-Za-z0-9._+@=-]+)*$ ]]
  relative_paths+=("$relative")
done

object_key="$NDDEV_CACHE_PREFIX_ROOT/packages/v1/$ecosystem/$runtime/$key_sha.tar.zst"
object_url="${SCCACHE_ENDPOINT%/}/$SCCACHE_BUCKET/$object_key"
sigv4="aws:amz:$AWS_REGION:s3"
scratch=$(mktemp -d "${RUNNER_TEMP:-/tmp}/nddev-package-cache.XXXXXXXXXX")
archive=$scratch/cache.tar.zst
staging=$scratch/staging
install -d -m 0700 "$staging"
cleanup() {
  find "$scratch" -mindepth 1 -delete 2>/dev/null || true
  rmdir "$scratch" 2>/dev/null || true
}
trap cleanup EXIT

curl_common=(
  --silent --show-error --connect-timeout 3 --max-time 180
  --retry 2 --retry-all-errors --cacert "$AWS_CA_BUNDLE"
  --aws-sigv4 "$sigv4" --user "$AWS_ACCESS_KEY_ID:$AWS_SECRET_ACCESS_KEY"
)
started=$(date +%s%3N)

if [[ "$operation" == restore ]]; then
  if ! http_code=$(curl "${curl_common[@]}" --output "$archive" --write-out '%{http_code}' "$object_url"); then
    printf '::warning::NDDev package cache unavailable; continuing uncached\n'
    exit 0
  fi
  case "$http_code" in
    200)
      archive_bytes=$(stat --format='%s' "$archive")
      (( archive_bytes > 0 && archive_bytes <= 1024 * 1024 * 1024 ))
      while IFS= read -r entry; do
        entry=${entry%/}
        [[ -n "$entry" && "$entry" != /* && "$entry" != *'/../'* && "$entry" != ../* && "$entry" != *'/..' ]]
        allowed=false
        for relative in "${relative_paths[@]}"; do
          case "$entry" in "$relative"|"$relative"/*) allowed=true; break ;; esac
        done
        [[ "$allowed" == true ]]
      done < <(tar --list --zstd --file "$archive")
      tar --extract --zstd --file "$archive" --directory "$staging" --no-same-owner --no-same-permissions
      if find "$staging" -mindepth 1 ! -type f ! -type d -print -quit | grep -q .; then
        echo "package-cache archive contains a special file" >&2
        exit 1
      fi
      for relative in "${relative_paths[@]}"; do
        [[ -d "$staging/$relative" ]] || continue
        install -d -m 0700 "$home/$relative"
        cp -a -- "$staging/$relative/." "$home/$relative/"
      done
      elapsed=$(( $(date +%s%3N) - started ))
      printf 'hit=true\n' >>"$GITHUB_OUTPUT"
      printf 'nddev_package_cache_event={"operation":"restore","result":"hit","ecosystem":"%s","key_sha256":"%s","bytes":%s,"duration_ms":%s}\n' \
        "$ecosystem" "$key_sha" "$archive_bytes" "$elapsed"
      ;;
    404)
      elapsed=$(( $(date +%s%3N) - started ))
      printf 'nddev_package_cache_event={"operation":"restore","result":"miss","ecosystem":"%s","key_sha256":"%s","bytes":0,"duration_ms":%s}\n' \
        "$ecosystem" "$key_sha" "$elapsed"
      ;;
    401|403) echo "NDDev package cache restore denied by trust policy (HTTP $http_code)" >&2; exit 1 ;;
    5??) printf '::warning::NDDev package cache returned HTTP %s; continuing uncached\n' "$http_code" ;;
    *) echo "unexpected NDDev package cache restore response HTTP $http_code" >&2; exit 1 ;;
  esac
  exit 0
fi

if [[ "$NDDEV_CACHE_MODE" != read-write ]]; then
  printf 'nddev_package_cache_event={"operation":"save","result":"read-only","ecosystem":"%s","key_sha256":"%s","bytes":0,"duration_ms":0}\n' \
    "$ecosystem" "$key_sha"
  exit 0
fi

existing=()
for relative in "${relative_paths[@]}"; do
  [[ -d "$home/$relative" ]] && existing+=("$relative")
done
if (( ${#existing[@]} == 0 )); then
  printf 'nddev_package_cache_event={"operation":"save","result":"empty","ecosystem":"%s","key_sha256":"%s","bytes":0,"duration_ms":0}\n' \
    "$ecosystem" "$key_sha"
  exit 0
fi

if ! head_code=$(curl "${curl_common[@]}" --head --output /dev/null --write-out '%{http_code}' "$object_url"); then
  printf '::warning::NDDev package cache unavailable; skipping save\n'
  exit 0
fi
case "$head_code" in
  200) printf 'nddev_package_cache_event={"operation":"save","result":"exists","ecosystem":"%s","key_sha256":"%s","bytes":0,"duration_ms":0}\n' "$ecosystem" "$key_sha"; exit 0 ;;
  404) ;;
  401|403) echo "NDDev package cache save denied by trust policy (HTTP $head_code)" >&2; exit 1 ;;
  5??) printf '::warning::NDDev package cache returned HTTP %s; skipping save\n' "$head_code"; exit 0 ;;
  *) echo "unexpected NDDev package cache HEAD response HTTP $head_code" >&2; exit 1 ;;
esac

tar --create --sort=name --mtime='@0' --owner=0 --group=0 --numeric-owner \
  --use-compress-program='zstd -1 -T0' --file "$archive" --directory "$home" -- "${existing[@]}"
archive_bytes=$(stat --format='%s' "$archive")
(( archive_bytes > 0 && archive_bytes <= 1024 * 1024 * 1024 ))
if ! put_code=$(curl "${curl_common[@]}" --request PUT --upload-file "$archive" --output /dev/null --write-out '%{http_code}' "$object_url"); then
  printf '::warning::NDDev package cache unavailable; save did not complete\n'
  exit 0
fi
case "$put_code" in
  200|201|204)
    elapsed=$(( $(date +%s%3N) - started ))
    printf 'nddev_package_cache_event={"operation":"save","result":"stored","ecosystem":"%s","key_sha256":"%s","bytes":%s,"duration_ms":%s}\n' \
      "$ecosystem" "$key_sha" "$archive_bytes" "$elapsed"
    ;;
  401|403) echo "NDDev package cache upload denied by trust policy (HTTP $put_code)" >&2; exit 1 ;;
  5??) printf '::warning::NDDev package cache returned HTTP %s; save skipped\n' "$put_code" ;;
  *) echo "unexpected NDDev package cache upload response HTTP $put_code" >&2; exit 1 ;;
esac
