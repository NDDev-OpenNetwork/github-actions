# Trust-scoped package cache

`actions/package-cache` connects private Linux jobs to the same one-job,
repository-scoped RustFS identity already delivered for `sccache`. It does not
create another credential or cache service, and it disables itself on public
GitHub-hosted runners where no NDDev assignment exists.

Use one restore step after the language/package-manager toolchain is available
and one save step after successful validation:

```yaml
- uses: NDDev-OpenNetwork/github-actions/actions/package-cache@<full-sha>
  with:
    operation: restore
    ecosystem: go
    runtime-version: 1.26.6
    lock-file: go.sum

# dependency resolution, build, and tests

- if: ${{ success() }}
  uses: NDDev-OpenNetwork/github-actions/actions/package-cache@<same-full-sha>
  with:
    operation: save
    ecosystem: go
    runtime-version: 1.26.6
    lock-file: go.sum
```

The key binds schema, ecosystem, exact runtime, lock digest, OS and
architecture. Stored data contains package/module downloads only, never build
outputs. Supported ecosystems are Go, npm, pnpm, Yarn, Bun, uv, pip, Cargo,
Maven, Gradle and pub.

`pub` reads `PUB_CACHE` rather than deriving a path. Dart has no command that
prints its cache directory and the default has moved between SDK versions, so
the step fails when `PUB_CACHE` is absent instead of caching whichever
directory happens to exist. Every Flutter and Dart setup action exports it
before dependency resolution.

The action enforces the delivered trust mode. Trusted and untrusted writers can
read and populate only their own repository prefix. A release reader consumes
only promoted objects and never writes. Authentication denial fails the step;
temporary cache/network unavailability is logged and falls back to uncached
execution.

Each operation emits one `nddev_package_cache_event` JSON record with the
non-secret key digest, result, byte count and duration. Evaluate cache benefit
from real project jobs: transfer time must be lower than the dependency work it
replaces. A hit by itself is not evidence of an optimization.
