#!/bin/sh
# Copy every resolved crate's license files into the /usr/share/licenses tree of
# attribution.md section 4, for the Rust payload built from source in this image.
# usage: collect-cargo-licenses.sh [--out DIR] [--fallback DIR], run from the cargo workspace root
# after `cargo build`, so every crate is already unpacked in the registry cache.
# Repo-owned, unlike its Go sibling scripts/collect-licenses.sh: fclones is the only
# cargo build among the cplieger images, so there is nothing to share it with.
# A crate with no license file at its root takes the committed copy under
# --fallback DIR/<crate>/ (the by-hand texts scripts/vendor-crate-licenses.sh cannot
# fetch); with none there either it fails the build rather than being skipped, because
# a missing text is a section 4(a) breach and the fix is a human decision.
set -eu

OUT=/out/usr/share/licenses
FALLBACK=""
while [ $# -gt 0 ]; do
  case "$1" in
    --out)
      OUT="${2:?--out needs a directory}"
      shift 2
      ;;
    --fallback)
      FALLBACK="${2:?--fallback needs a directory}"
      shift 2
      ;;
    *)
      printf 'collect-cargo-licenses: unknown option %s\n' "$1" >&2
      exit 2
      ;;
  esac
done

meta=$(mktemp)
trap 'rm -f "$meta"' EXIT INT TERM HUP

crates=0
files=0
missing=""

# copy_licenses SRC_DIR DEST_DIR: copy the license-family files at SRC_DIR's root
# into DEST_DIR; sets $copied.
copy_licenses() {
  copied=0
  for f in "$1"/*; do
    [ -f "$f" ] || continue
    case "$(printf '%s' "${f##*/}" | tr '[:lower:]' '[:upper:]')" in
      *.RS) continue ;;
      LICENSE* | LICENCE* | COPYING* | NOTICE*) ;;
      *) continue ;;
    esac
    mkdir -p "$2"
    cp -f "$f" "$2/"
    copied=$((copied + 1))
  done
}

# copy_all SRC_DIR DEST_DIR: copy every regular file at SRC_DIR's root; sets $copied.
copy_all() {
  copied=0
  for f in "$1"/*; do
    [ -f "$f" ] || continue
    mkdir -p "$2"
    cp -f "$f" "$2/"
    copied=$((copied + 1))
  done
}

cargo metadata --format-version 1 --locked >"$meta"

# The whole resolved graph, minus the workspace's own packages, whose license is the
# upstream's and is placed by the Dockerfile. A crate the target platform never links
# keeps its text here: an extra license file costs bytes, a missing one is a breach.
# manifest_path resolves a crate wherever cargo unpacked it, registry or git.
resolved=$(jq -r '
  (.workspace_members) as $own
  | [.resolve.nodes[].id] as $ids
  | .packages[]
  | select((.id | IN($ids[])) and ((.id | IN($own[])) | not))
  | [.name, .version, (.manifest_path | rtrimstr("/Cargo.toml"))]
  | join("|")
' "$meta" | sort -u)

if [ -z "$resolved" ]; then
  printf 'collect-cargo-licenses: cargo metadata resolved no dependency crates\n' >&2
  exit 1
fi

while IFS='|' read -r name version dir; do
  [ -n "$name" ] || continue
  case "$name" in
    *[!A-Za-z0-9._-]*)
      printf 'collect-cargo-licenses: crate name is not a single path component: "%s"\n' "$name" >&2
      exit 1
      ;;
  esac
  if [ ! -d "$dir" ]; then
    printf 'collect-cargo-licenses: %s %s has no source directory (%s)\n' "$name" "$version" "$dir" >&2
    exit 1
  fi
  copy_licenses "$dir" "$OUT/$name"
  if [ "$copied" -eq 0 ] && [ -n "$FALLBACK" ] && [ -d "$FALLBACK/$name" ]; then
    copy_all "$FALLBACK/$name" "$OUT/$name"
  fi
  if [ "$copied" -eq 0 ]; then
    missing="$missing
$name $version (at $dir; add its license text under licenses/crates/$name/ or drop the dependency)"
    continue
  fi
  crates=$((crates + 1))
  files=$((files + copied))
done <<EOF
$resolved
EOF

if [ -n "$missing" ]; then
  printf 'collect-cargo-licenses: no LICENSE*, LICENCE*, COPYING* or NOTICE* file at the crate root of:\n' >&2
  printf '%s\n' "$missing" | sed '/^$/d; s/^/  /' >&2
  exit 1
fi
printf 'collect-cargo-licenses: %s crates, %s files under %s\n' "$crates" "$files" "$OUT"
