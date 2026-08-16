FROM ubuntu:24.04@sha256:561618e2c15bf2397621dd04f96926663a3b5616c189cf7e38db7e82f5c538ea

ARG PAYLOAD_MIB=64
RUN install -d -m 0755 /opt/benchmark \
    && dd if=/dev/zero of=/opt/benchmark/payload.bin bs=1M count="${PAYLOAD_MIB}" status=none \
    && cd /opt/benchmark \
    && sha256sum payload.bin >payload.sha256
COPY --chmod=0555 entrypoint.sh /usr/local/bin/benchmark-entrypoint

USER 65534:65534
ENTRYPOINT ["/usr/local/bin/benchmark-entrypoint"]
