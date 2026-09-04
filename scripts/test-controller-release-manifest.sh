#!/usr/bin/env bash
set -Eeuo pipefail

repo_root=$(git rev-parse --show-toplevel)
scratch=$(mktemp -d)
trap 'rm -rf -- "${scratch}"' EXIT

printf '#!/usr/bin/env bash\nexit 0\n' >"${scratch}/one"
printf '#!/usr/bin/env bash\nexit 0\n# two\n' >"${scratch}/two"
chmod +x "${scratch}/one" "${scratch}/two"

"${repo_root}/scripts/write-controller-release-manifest.sh" \
  v1.2.3 0123456789abcdef0123456789abcdef01234567 "${scratch}/manifest.json" \
  "${scratch}/one" "${scratch}/two"

python3 - "${scratch}/manifest.json" "${scratch}/one" "${scratch}/two" <<'PY'
import hashlib
import json
from pathlib import Path
import sys

manifest = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
assert manifest["schema_version"] == 1
assert manifest["version"] == "v1.2.3"
assert manifest["source_commit"] == "0123456789abcdef0123456789abcdef01234567"
assert manifest["build"]["cgo_enabled"] is False
assert manifest["build"]["trimpath"] is True
assert manifest["build"]["buildvcs"] is False
for path_text in sys.argv[2:]:
    path = Path(path_text)
    digest = hashlib.sha256(path.read_bytes()).hexdigest()
    assert manifest["binaries"][path.name] == f"sha256:{digest}"
PY
