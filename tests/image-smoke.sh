#!/bin/sh
# Runtime image smoke test for docker-fclones-scheduler. Invoked by the central
# CI docker job:  sh tests/image-smoke.sh <image-ref>
#
# Starts the assembled image in external-trigger mode (FCLONES_INTERVAL=off) and
# waits for the container's own HEALTHCHECK - the distroless `wrapper health`
# file-marker probe - to report "healthy": proves the binary runs in the real
# distroless base, config parsing works, the /cache write-preflight passes, and
# the file-marker health probe works.
#
# Why external mode: runExternal sets the health marker healthy at boot (an
# idle, not-yet-triggered container is healthy by design) WITHOUT running
# fclones, so no scan directory, no real duplicate files, and no external
# scheduler are needed. Built-in mode would require fclones to actually scan a
# directory to flip healthy, and run-once exits after a single scan so it never
# stays running to report healthy. The only runtime requirement external mode
# has is a writable /cache: bootstrap's verifyCacheDir does a MkdirAll + write
# probe there before the marker is set, and the container runs nonroot (UID
# 65532), so /cache is supplied as a world-writable tmpfs.
set -eu

IMG="${1:?usage: image-smoke.sh <image-ref>}"
NAME="smoke-fclones-$$"
TIMEOUT=90 # must cover the healthcheck start-period (15s) + a few 30s intervals

# shellcheck disable=SC2317,SC2329  # invoked indirectly via trap
cleanup() {
  code=$?
  # Dump container logs only on failure (a passing run stays quiet).
  if [ "$code" -ne 0 ]; then
    printf '%s\n' "--- container logs (tail) ---" >&2
    docker logs "$NAME" 2>&1 | tail -40 >&2 || true
  fi
  docker rm -f "$NAME" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# FCLONES_INTERVAL=off selects external (idle) mode, which marks the container
# healthy at boot without running fclones. verifyCacheDir write-probes /cache
# before that and the container is nonroot, so mount /cache as a world-writable
# tmpfs (mode 1777, matching the sibling pg-autodump smoke test).
docker run -d --name "$NAME" \
  -e FCLONES_INTERVAL=off \
  --tmpfs /cache:size=16m,mode=1777 \
  "$IMG" >/dev/null

i=0
status=starting
while [ "$i" -lt "$TIMEOUT" ]; do
  # Fail fast on an early exit: poll .State.Running before the health status so
  # a crash-boot is caught by its exit code (more debuggable than "unhealthy")
  # and the verdict never depends on what health a stopped container reports.
  if [ "$(docker inspect --format '{{ .State.Running }}' "$NAME" 2>/dev/null || echo missing)" != "true" ]; then
    ec=$(docker inspect --format '{{ .State.ExitCode }}' "$NAME" 2>/dev/null || echo '?')
    printf 'FAIL: docker-fclones-scheduler container exited early (exit code %s)\n' "$ec" >&2
    exit 1
  fi
  status=$(docker inspect --format '{{ if .State.Health }}{{ .State.Health.Status }}{{ else }}no-healthcheck{{ end }}' "$NAME" 2>/dev/null || echo gone)
  case "$status" in
    healthy)
      printf 'docker-fclones-scheduler image smoke: ok (healthy after %ss)\n' "$i"
      exit 0
      ;;
    unhealthy)
      printf 'FAIL: docker-fclones-scheduler reported unhealthy\n' >&2
      exit 1
      ;;
    no-healthcheck)
      printf 'FAIL: image has no HEALTHCHECK to assert against\n' >&2
      exit 1
      ;;
    gone)
      printf 'FAIL: docker-fclones-scheduler container is gone\n' >&2
      exit 1
      ;;
  esac
  i=$((i + 1))
  sleep 1
done
printf 'FAIL: docker-fclones-scheduler did not become healthy within %ss (last status: %s)\n' "$TIMEOUT" "$status" >&2
exit 1
