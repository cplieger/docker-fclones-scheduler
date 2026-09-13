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
# carries the ARG-derived version + purl, and the purl carries the provenance
# of the fetch this arch's build performed — a hardcoded version would drift
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
  # version field. Asserted as a prefix, because the qualifiers that follow the
  # version differ per arch (below).
  purl=$(sed -n 's/.*"purl": "\([^"]*\)".*/\1/p' "$SBOM")
  case "$purl" in
    "pkg:cargo/fclones@${expected}?"*) ;;
    *)
      err "FAIL: embedded SBOM fragment purl is not pkg:cargo/fclones@${expected} with provenance qualifiers (got: ${purl:-none})"
      fail=1
      ;;
  esac
  # The purl must also carry the provenance of the fetch the build ACTUALLY
  # performed, since the fragment is the only record of this component in the
  # signed SBOM. The two arches fetch differently and this stage builds on
  # whichever arch the runner is, so both shapes are accepted: amd64 downloads a
  # release asset and sha256-verifies it (download_url + checksum), arm64 clones
  # the repo at the tag and verifies a commit (vcs_url, and NO checksum, because
  # a git commit is not a content digest of a fetched artifact). Neither shape
  # may claim a crates.io download despite the pkg:cargo type: `cargo build
  # --locked` pulls fclones' dependencies from crates.io, never fclones itself.
  case "$purl" in
    *"?download_url=https://github.com/pkolaczk/fclones/releases/download/${FCLONES_EXPECTED_VERSION}/"*"&checksum=sha256:"[0-9a-f]*)
      log "sbom fragment purl provenance: amd64 release asset + verified sha256"
      ;;
    *"?vcs_url=git%2Bhttps://github.com/pkolaczk/fclones.git%40"[0-9a-f]*)
      case "$purl" in
        *checksum=*)
          err "FAIL: embedded SBOM fragment purl claims a checksum for the arm64 git clone; a commit pin is not a content digest"
          fail=1
          ;;
        *)
          log "sbom fragment purl provenance: arm64 git clone at verified commit"
          ;;
      esac
      ;;
    *)
      err "FAIL: embedded SBOM fragment purl carries no provenance for the fetch this build performed (want download_url + checksum on amd64, vcs_url on arm64; got: ${purl:-none})"
      fail=1
      ;;
  esac
fi

[ "$fail" -eq 0 ] && log "sbom fragment smoke: ok"
exit "$fail"
