# Integration runner parity gate

`integration-parity.yml` is the manual-only runtime gate for the
`nddev-linux-integration` GARM Scale Set. It exercises the official runner in a
disposable full VM with a VM-local Docker daemon. It never mounts a CI-host
socket or returns an executed VM to a pool.

## Closed test shape

The Scale Set remains `min-idle=0`, `max-runners=1`. `mode=parity` runs three
dependent jobs, so GitHub and GARM must allocate three distinct one-job VMs in
order:

1. verify integration image provenance, exact `runner,docker,sudo` membership,
   Docker `29.1.3`, overlay2/systemd, the `/run` tmpfs socket and sealed local
   action base;
2. prove the prior VM sentinel is absent, build a local Docker action from that
   preloaded base, require the official runner's VM-local Docker socket mount
   and exercise command-file outputs;
3. pull digest-pinned Ubuntu and nginx images, run the job as the image-pinned
   runner identity `1001:1002` with zero capabilities beside a healthy service
   container,
   require the runner's VM-local Docker socket mount to be inaccessible to
   that user, exercise service DNS/network and execute a pinned JavaScript
   checkout action inside the job container.

The public parity images are exact multi-architecture registry manifests:

| Role | Immutable reference |
| --- | --- |
| job container | `ubuntu:24.04@sha256:561618e2c15bf2397621dd04f96926663a3b5616c189cf7e38db7e82f5c538ea` |
| service container | `nginx:1.29-alpine@sha256:5616878291a2eed594aee8db4dade5878cf7edcb475e59193904b198d9b830de` |

These first proofs intentionally exercise the official runner pull path.
Production workflows switch to the local Zot mirror only after its promotion
gate; a mutable tag or an unverified local cache is not a substitute.

The official runner `v2.336.0` deliberately bind-mounts
`/var/run/docker.sock` into Docker container actions ([pinned runner
source](https://github.com/actions/runner/blob/98aabcd429c4e8402406c56ce2d26387fed3b9ce/src/Runner.Worker/Handlers/ContainerActionHandler.cs#L193)).
It makes the same system mount for a job container ([pinned runner
source](https://github.com/actions/runner/blob/98aabcd429c4e8402406c56ce2d26387fed3b9ce/src/Runner.Worker/Container/ContainerInfo.cs#L57-L60)).
This gate requires those compatibility behaviors only after the preceding step
proves the source socket belongs to the private daemon inside the disposable
worker VM. Docker container actions need that VM-local socket for compatibility;
their capabilities and no-new-privileges settings are defense in depth, while
the disposable full VM remains the security boundary. The job container uses
the current immutable image mapping's numeric runner identity `1001:1002`.
This keeps its mounted
workspace and command files writable for JavaScript actions while making the
`root:docker` mode-`0660` socket neither readable nor writable. The gate also
retains zero effective capabilities and no-new-privileges. The socket is never
from the CI host, and the VM is destroyed after the one job.
Ubuntu's `/var/run` symlink makes the job-container mount visible at both
`/var/run/docker.sock` and `/run/docker.sock`; the gate requires both paths to
resolve to the same canonical `/run/docker.sock`. The minimal Docker action
base has no `/var/run` symlink, so it exposes only the explicit mount path.

The identity is not a distribution default. It must remain synchronized with
the exact `runner_uid` and `runner_gid` pinned for the integration image in the
provider configuration. Run `31315211771` proved that retaining the earlier
`1000:1000` contract after the image moved to `1001:1002` makes the official
runner command files unwritable and causes pinned JavaScript actions to fail
closed with `EACCES`.

## Run order

Dispatch and watch only through the repository-scoped CLI:

```bash
gh workflow run integration-parity.yml \
  --repo NDDev-OpenNetwork/github-actions \
  -f mode=parity
gh run list \
  --repo NDDev-OpenNetwork/github-actions \
  --workflow integration-parity.yml
```

Then run `mode=network-negative`. It must retain public HTTPS while denying
Incus, GARM administration, SSH, cloud metadata and representative RFC1918
destinations from both the VM and a nested Docker container.

Run `mode=timeout` separately. GitHub defines `jobs.<job_id>.timeout-minutes`
as the limit after which it [automatically cancels the
job](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#jobsjob_idtimeout-minutes),
so the expected workflow, job and long-running step conclusions are
`cancelled` after the one-minute limit. The runner, VM and registration must
still be removed within the cleanup lease. Finally dispatch
`mode=cancellation`, wait until the log says it is ready, cancel that run
through `gh run cancel`, and require conclusion `cancelled` plus the same
cleanup result.

## Acceptance evidence

After every mode require:

- no Incus instance, provider lease, offline runner registration, Docker disk
  or network owned by the completed job;
- a bounded external diagnostic bundle for every executed VM;
- RustFS source/exported bundle and byte counts equal, with zero pending;
- loopback observer healthy with zero orphan or missing instances;
- every retained legacy listener unchanged in count and state;
- ExamplePlatform and Captcha HTTP 200.

A successful application step is insufficient if teardown or external
diagnostics fail. Timeout and cancellation are successful gates only when their
expected GitHub conclusion and all infrastructure cleanup assertions both hold.
