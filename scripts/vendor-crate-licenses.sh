#!/bin/sh
# Commit the license text of every crate fclones links, so the image ships it under
# /usr/share/licenses on amd64 too: that build takes upstream's prebuilt binary and so has
# no cargo registry for the arm64 sibling scripts/collect-cargo-licenses.sh to walk.
# usage: vendor-crate-licenses.sh [--dockerfile FILE] [--out DIR], run from the repo root.
# Re-run it on every FCLONES_VERSION bump and commit the result: the arm64 source build
# diffs what it collects against the MANIFEST written here and refuses to build when the
# two disagree. Deliberately uses no cargo: neither this container nor the amd64 build
# stage has one.
set -eu

DOCKERFILE=Dockerfile
OUT=licenses/crates
REGISTRY='registry+https://github.com/rust-lang/crates.io-index'

while [ $# -gt 0 ]; do
  case "$1" in
    --dockerfile)
      DOCKERFILE="${2:?--dockerfile needs a file}"
      shift 2
      ;;
    --out)
      OUT="${2:?--out needs a directory}"
      shift 2
      ;;
    *)
      printf 'vendor-crate-licenses: unknown option %s\n' "$1" >&2
      exit 2
      ;;
  esac
done

version=$(awk -F= '$1 == "ARG FCLONES_VERSION" { print $2; exit }' "$DOCKERFILE")
if [ -z "$version" ]; then
  printf 'vendor-crate-licenses: no "ARG FCLONES_VERSION=" line in %s\n' "$DOCKERFILE" >&2
  exit 1
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM HUP
stage="$work/out"
mkdir -p "$stage"

fetch() {
  curl -fsSL --connect-timeout 10 --max-time 120 --retry 3 --retry-delay 5 -o "$1" "$2"
}

# declared_license CRATE_DIR: the crate's own license expression and repository, so a
# fail-closed report hands over what the by-hand fetch below needs.
declared_license() {
  awk -F' *= *' '
    $1 == "license" && lic == "" { lic = $2 }
    $1 == "repository" && repo == "" { repo = $2 }
    END { gsub(/"/, "", lic); gsub(/"/, "", repo); printf "license=%s repository=%s", lic, repo }
  ' "$1/Cargo.toml"
}

# carry_override NAME VERSION: adopt a committed directory for a crate whose publisher
# ships no license file at all. The version in its SOURCE file is what makes a bump
# re-fetch by hand instead of inheriting a stale text.
carry_override() {
  override="$OUT/$1"
  [ -f "$override/SOURCE" ] || return 1
  grep -qxF "crate: $1 $2" "$override/SOURCE" || return 1
  mkdir -p "$stage/$1"
  for kept in "$override"/*; do
    [ -f "$kept" ] || continue
    cp -f "$kept" "$stage/$1/${kept##*/}"
  done
  return 0
}

fetch "$work/Cargo.lock" "https://raw.githubusercontent.com/pkolaczk/fclones/$version/Cargo.lock"

# Every [[package]] record with a source, which is every crate that is not a member of
# fclones' own workspace.
awk '
BEGIN { RS = "" }
$1 != "[[package]]" { next }
{
  name = ""; ver = ""; src = ""
  for (i = 2; i < NF; i++) {
    if ($i != "=") { continue }
    key = $(i - 1); val = $(i + 1)
    gsub(/^"|",?$/, "", val)
    if (key == "name") { name = val }
    else if (key == "version") { ver = val }
    else if (key == "source") { src = val }
  }
  if (name != "" && ver != "" && src != "") { print name, ver, src }
}
' "$work/Cargo.lock" | sort >"$work/crates"

if [ ! -s "$work/crates" ]; then
  printf 'vendor-crate-licenses: fclones %s Cargo.lock lists no dependency crate\n' "$version" >&2
  exit 1
fi

crates=0
files=0
missing=""

while read -r name ver src; do
  if [ "$src" != "$REGISTRY" ]; then
    printf 'vendor-crate-licenses: %s %s does not come from crates.io (%s)\n' "$name" "$ver" "$src" >&2
    exit 1
  fi
  case "$name$ver" in
    *[!A-Za-z0-9._+-]*)
      printf 'vendor-crate-licenses: "%s %s" is not a single path component\n' "$name" "$ver" >&2
      exit 1
      ;;
  esac
  rm -rf "$work/x"
  mkdir -p "$work/x"
  fetch "$work/crate.tar.gz" "https://static.crates.io/crates/$name/$name-$ver.crate"
  tar -xzf "$work/crate.tar.gz" -C "$work/x"
  root="$work/x/$name-$ver"
  if [ ! -d "$root" ]; then
    printf 'vendor-crate-licenses: %s-%s.crate does not unpack to %s-%s/\n' "$name" "$ver" "$name" "$ver" >&2
    exit 1
  fi
  copied=0
  for f in "$root"/*; do
    [ -f "$f" ] || continue
    base=${f##*/}
    case "$(printf '%s\n' "$base" | tr '[:lower:]' '[:upper:]')" in
      LICENSE* | LICENCE* | COPYING* | NOTICE*) ;;
      *) continue ;;
    esac
    mkdir -p "$stage/$name"
    cp -f "$f" "$stage/$name/$base"
    copied=$((copied + 1))
  done
  if [ "$copied" -eq 0 ]; then
    if carry_override "$name" "$ver"; then
      crates=$((crates + 1))
    else
      missing="$missing
$name $ver ($(declared_license "$root"))"
      continue
    fi
  else
    crates=$((crates + 1))
    files=$((files + copied))
  fi
  printf '%s %s\n' "$name" "$ver" >>"$stage/MANIFEST"
done <"$work/crates"

if [ -n "$missing" ]; then
  printf 'vendor-crate-licenses: no LICENSE*, LICENCE*, COPYING* or NOTICE* file at the crate root of:\n' >&2
  printf '%s\n' "$missing" | sed '/^$/d; s/^/  /' >&2
  printf 'For each, fetch the license text from that repository at that version into %s/<crate>/ and record the URL and version in a SOURCE file beside it whose first line is "crate: <name> <version>". Never write a license text from memory; where upstream publishes none, say so in SOURCE and add no text.\n' "$OUT" >&2
  exit 1
fi

rm -rf "$OUT"
mkdir -p "$OUT"
cp -R "$stage/." "$OUT/"
printf 'vendor-crate-licenses: %s crates, %s files under %s (fclones %s)\n' "$crates" "$files" "$OUT" "$version"
