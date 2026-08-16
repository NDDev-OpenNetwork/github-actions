# ADR 0011: VM-local Docker integration workers

Status: accepted for the gated integration pilot on 2026-08-08; runtime
promotion remains blocked on image smoke and GitHub parity evidence.

## Context

GitHub requires Docker on a Linux self-hosted runner for Docker container
actions, `jobs.<job_id>.container`, and service containers. A Docker socket
mounted from the CI host would give workflow code control of the host daemon
and defeat the disposable-VM boundary. Adding Docker to every worker would
also expand the standard pool's attack surface and image size.

## Decision

Keep the standard image Docker-free. Build a separate `integration` image from
the same signed Canonical source and pinned official `actions/runner` archive.
Install Ubuntu's native `docker.io`, `docker-buildx`, and `docker-compose-v2`
packages inside that full VM. Pin the exact Docker toolchain package versions
in `config/golden-image-integration.yaml`; do not add Docker's external apt
repository or another signing key to the pilot supply chain.

The daemon uses its own `/var/lib/docker` and `/run/docker.sock` on the VM root
filesystem. The image pipeline never passes a host socket, disk, or device.
Docker uses `overlay2`, the systemd cgroup driver, BuildKit, bounded local logs,
and a dedicated default address pool. The daemon is stopped before snapshot
publication but remains enabled for the next boot.

Preload one minimal static BusyBox base by content ID for local Docker-action
build smoke. This is not a mutable registry input and contains no credentials.
The sealing gate permits exactly that one OCI image, no containers, no volumes,
and only Docker's three built-in networks.

The independent smoke VM must prove:

- Docker and containerd start on boot and the CLI is the real `/usr/bin/docker`
  binary rather than a wrapper;
- the socket and Docker data root resolve to the VM root filesystem;
- an unprivileged member of the VM-local `docker` group can run a container;
- BuildKit can build and execute a local container action;
- two containers can communicate on a disposable user-defined network;
- the runner cache remains unregistered, nested KVM and Incus sockets remain
  absent, SSH remains disabled, and host/private/metadata routes stay blocked.

The integration Scale Set remains cold with `max-runners=1` and
`min-idle-runners=0` until Docker-action, job-container, service-container,
timeout, cancellation, network-negative, teardown, and diagnostic-export
parity tests pass through GitHub.

## Consequences

- Docker's administrative boundary is the disposable VM, never the CI host.
- Standard jobs retain the smaller and less privileged image.
- Integration image builds and boots are heavier, so this pool receives its
  own benchmark and warm-capacity decision.
- Ubuntu package snapshots are still needed for bit-for-bit reproduction of
  the complete OS; exact Docker toolchain pins and the final package/image
  digests make the pilot artifact auditable in the meantime.
- GitHub always pulls job and service container images. Registry locality and
  scoped read credentials are a separate Zot promotion gate; the image smoke
  does not silently weaken registry authentication.

## References

- [GitHub self-hosted runner requirements](https://docs.github.com/en/actions/reference/runners/self-hosted-runners)
- [GitHub jobs in containers](https://docs.github.com/en/actions/how-tos/write-workflows/choose-where-workflows-run/run-jobs-in-a-container)
- [GitHub service containers](https://docs.github.com/en/actions/tutorials/use-containerized-services/use-docker-service-containers)
- [Docker Engine on Ubuntu](https://docs.docker.com/engine/install/ubuntu/)

