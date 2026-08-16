#!/bin/sh
set -eu

cd /opt/benchmark
sha256sum --check payload.sha256
