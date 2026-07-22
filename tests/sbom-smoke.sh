#!/bin/sh
# Build-time smoke test for the embedded CycloneDX SBOM fragment.
#
# Runs in the Dockerfile `sbom-test` stage (FROM the fclones builder, which
# has a shell; the distroless final stage does not), so the centralized
# `ci / validate` docker build-ability gate executes it on every PR and push
# — the final stage COPYs the fragment from the sbom-test stage, so a failure
# here fails the image build. Asserts the fragment that makes the Rust-built
# fclones payload visible to the signed release SBOM (see the Dockerfile
# comment) ships correct: exists, JSON-object-shaped, names fclones, and
# carries the ARG-derived version + purl — a hardcoded version would drift
# silently on the next Renovate bump, which is exactly the failure mode the
# fragment exists to prevent. Pins the final-stage COPY directive too, so
# "present in the builder" cannot drift apart from "shipped in the image".
#
# Run locally:
#   FCLONES_EXPECTED_VERSION=v0.35.0 DOCKERFILE=./Dockerfile \
#   SBOM_FRAGMENT=/path/to/fclones-scheduler.cdx.json sh tests/sbom-smoke.sh
set -eu

fail=0
log() { printf '%s\n' "$*"; }
err() { printf '%s\n' "$*" >&2; }

: "${DOCKERFILE:?DOCKERFILE must name the Dockerfile under test}"
: "${FCLONES_EXPECTED_VERSION:?FCLONES_EXPECTED_VERSION must carry the pinned fclones version}"
SBOM="${SBOM_FRAGMENT:-/usr/src/fclones-scheduler.cdx.json}"
expected=${FCLONES_EXPECTED_VERSION#v}

# Pin the final-stage COPY directive: the distroless stage has no shell, so
# this grep is the only guard that the asserted file IS the shipped file.
grep -Fqx -- 'COPY --from=sbom-test /usr/src/fclones-scheduler.cdx.json /usr/share/sbom/fclones-scheduler.cdx.json' "$DOCKERFILE" || {
  err "FAIL: Dockerfile does not ship the SBOM fragment COPY directive"
  fail=1
}

if [ ! -s "$SBOM" ]; then
  err "FAIL: embedded SBOM fragment missing or empty: $SBOM"
  fail=1
else
  # The builder ships no jq, so assert shape with head/tail and grep:
  # non-empty, starts with { and ends with } (tail -c 2 reads the closing
  # brace plus trailing newline; command substitution strips the newline).
  if [ "$(head -c 1 "$SBOM")" != "{" ] || [ "$(tail -c 2 "$SBOM")" != "}" ]; then
    err "FAIL: embedded SBOM fragment is not a JSON object (bad first/last byte)"
    fail=1
  fi
  grep -q '"name": "fclones"' "$SBOM" || {
    err "FAIL: embedded SBOM fragment missing component: fclones"
    fail=1
  }
  # Exactly one version-shaped component version ("version": 1 — the BOM
  # serial, unquoted — and "specVersion" don't match the pattern). grep -c
  # prints the count (0 included) even when it exits 1 on zero matches;
  # || true keeps set -e from aborting before the FAIL report.
  versions=$(grep -c '"version": "[0-9][0-9.]*"' "$SBOM" || true)
  if [ "$versions" -ne 1 ]; then
    err "FAIL: embedded SBOM fragment has $versions version-shaped component versions (want 1)"
    fail=1
  fi
  grep -qF "\"version\": \"${expected}\"" "$SBOM" || {
    err "FAIL: embedded SBOM fragment version is not ${expected} (ARG wiring broken?)"
    fail=1
  }
  # The purl must carry the same version: scanners match on the purl, so a
  # drifted purl would silently break advisory matching even with a correct
  # version field.
  grep -qF "\"purl\": \"pkg:cargo/fclones@${expected}\"" "$SBOM" || {
    err "FAIL: embedded SBOM fragment purl is not pkg:cargo/fclones@${expected}"
    fail=1
  }
fi

[ "$fail" -eq 0 ] && log "sbom fragment smoke: ok"
exit "$fail"
