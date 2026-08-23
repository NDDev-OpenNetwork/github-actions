# Immutable tool cache

`actions/tool-cache` removes checksum-pinned standalone tool archives from the
private job critical path without making the cache an execution authority. The
object identity includes the exact upstream URL and expected SHA-256. Every
cache hit is size-bounded and hashed again before it reaches the caller.

```yaml
- id: actionlint-archive
  uses: NDDev-OpenNetwork/github-actions/actions/tool-cache@<full-sha>
  with:
    url: https://github.com/rhysd/actionlint/releases/download/v1.7.12/actionlint_1.7.12_linux_amd64.tar.gz
    sha256: 8aca8db96f1b94770f1b0d72b6dddcb1ebb8123cb3712530b08cc387b349a3d8
    output: ${{ runner.temp }}/tools/actionlint.tar.gz
    max-bytes: '16777216'
```

On a private Drakkars worker, the action first requests the immutable object
under the caller repository's injected trust prefix. A miss or cache failure
uses the ordinary upstream URL with bounded retries. A trusted or untrusted
writer stores a verified miss for later jobs; read-only release jobs never
write. An incomplete assignment, transport failure, 5xx response or bad cached
digest is visible in `nddev_tool_cache_event` but cannot suppress the verified
upstream fallback.

On GitHub-hosted runners no fleet assignment exists, so the same action goes
directly to upstream. It never needs an estate endpoint or credential in a
public workflow. The destination must be below `RUNNER_TEMP`; extraction and
executable permissions remain the calling workflow's responsibility.
