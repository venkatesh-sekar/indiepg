#!/usr/bin/env bash
# Build indiepg-e2e-preinstalled:latest by running a REAL `indiepg install` inside
# a booted base container and committing the provisioned filesystem.
#
# `indiepg install` needs systemd (systemctl enable --now postgresql) and a live
# postmaster, so it cannot run during `docker build`. We boot the base image with
# systemd, install, wait until the panel answers /readyz, then `docker commit`.
#
# The admin password is fixed (E2E_ADMIN_PASSWORD) so every non-install scenario
# logs in deterministically — it MUST match harness.AdminPassword.
set -euo pipefail

export DOCKER_CONTEXT="${DOCKER_CONTEXT:-default}"

BASE_IMAGE="${BASE_IMAGE:-indiepg-e2e-base:latest}"
OUT_IMAGE="${OUT_IMAGE:-indiepg-e2e-preinstalled:latest}"
E2E_ADMIN_PASSWORD="${E2E_ADMIN_PASSWORD:-E2eTestAdminPassword-v1}"
PG_VERSION="${PG_VERSION:-}"   # empty => catalog default (17)
BUILDER="indiepg-e2e-preinstall-builder-$$"

cleanup() { docker rm -f "$BUILDER" >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo ">> booting base image to provision it..."
docker rm -f "$BUILDER" >/dev/null 2>&1 || true
docker run -d --name "$BUILDER" \
  --privileged --cgroupns=host \
  --tmpfs /run --tmpfs /run/lock --tmpfs /tmp \
  -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
  --security-opt apparmor=unconfined \
  "$BASE_IMAGE" /sbin/init >/dev/null

echo ">> waiting for systemd..."
for _ in $(seq 1 60); do
  state="$(docker exec "$BUILDER" systemctl is-system-running 2>/dev/null || true)"
  case "$state" in running|degraded) break;; esac
  sleep 1
done

echo ">> running indiepg install (pg-version='${PG_VERSION:-default}')..."
install_args=(install --password "$E2E_ADMIN_PASSWORD")
if [ -n "$PG_VERSION" ]; then install_args+=(--pg-version "$PG_VERSION"); fi
docker exec "$BUILDER" indiepg "${install_args[@]}"

echo ">> waiting for the panel to answer /readyz..."
ready=0
for _ in $(seq 1 60); do
  if docker exec "$BUILDER" sh -c 'curl -fsS http://127.0.0.1:8443/readyz >/dev/null 2>&1'; then
    ready=1; break
  fi
  sleep 1
done
if [ "$ready" != "1" ]; then
  echo "!! panel never became ready; dumping status" >&2
  docker exec "$BUILDER" systemctl --no-pager status indiepg postgresql >&2 || true
  exit 1
fi

# Stop the panel + Postgres CLEANLY before commit. This is critical: a committed
# data dir from a still-running cluster makes every boot do a ~100s crash-recovery
# fsync ("database system was interrupted"). The Debian `postgresql.service` is a
# wrapper that does NOT stop the instance, so we stop the real `postgresql@*-main`
# unit (glob matches the loaded instance) and then confirm the cluster is down.
docker exec "$BUILDER" systemctl stop indiepg >/dev/null 2>&1 || true
docker exec "$BUILDER" systemctl stop 'postgresql*.service' >/dev/null 2>&1 || true
# Belt-and-suspenders: a clean fast shutdown of every cluster, then verify down.
docker exec "$BUILDER" bash -c \
  'pg_lsclusters -h 2>/dev/null | awk "{print \$1, \$2}" | while read v n; do pg_ctlcluster "$v" "$n" stop -m fast 2>/dev/null || true; done' || true

echo ">> waiting for the cluster to be fully down (clean) ..."
down=0
for _ in $(seq 1 30); do
  if docker exec "$BUILDER" bash -c 'pg_lsclusters -h 2>/dev/null | grep -qw online'; then
    sleep 1
  else
    down=1; break
  fi
done
if [ "$down" != "1" ]; then
  echo "!! cluster did not stop cleanly before commit" >&2
  docker exec "$BUILDER" pg_lsclusters >&2 || true
  exit 1
fi

echo ">> committing -> $OUT_IMAGE"
docker commit \
  --change 'CMD ["/sbin/init"]' \
  --change 'STOPSIGNAL SIGRTMIN+3' \
  "$BUILDER" "$OUT_IMAGE" >/dev/null

echo ">> done: $OUT_IMAGE"
