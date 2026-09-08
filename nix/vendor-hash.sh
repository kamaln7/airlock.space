#!/usr/bin/env bash
# Check or rewrite buildGoModule vendorHash in nix/package.nix.
# Uses host nix if present, otherwise the Colima VM (same path via virtiofs).
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
pkg=$root/nix/package.nix
dummy=sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
nix_profile=/nix/var/nix/profiles/default/etc/profile.d/nix-daemon.sh

usage() {
  echo "usage: $0 check | update" >&2
  exit 2
}

ensure_colima() {
  if ! command -v colima >/dev/null 2>&1; then
    echo "nix is not on PATH. Start Colima (or install Nix) and retry." >&2
    exit 1
  fi
  if ! colima status >/dev/null 2>&1; then
    echo "starting colima..." >&2
    colima start
  fi
}

# Re-exec inside the VM so the rest of the script can call nix normally.
if ! command -v nix >/dev/null 2>&1; then
  if [ -n "${AIRLOCK_NIX_INNER:-}" ]; then
    echo "nix is not on PATH inside Colima. Install Nix in the VM and retry." >&2
    exit 1
  fi
  ensure_colima
  exec colima ssh -- env AIRLOCK_NIX_INNER=1 bash -lc "
    [ -f $nix_profile ] && . $nix_profile
    cd $(printf '%q' "$root")
    exec ./nix/vendor-hash.sh $(printf '%q' "${1:-}")
  "
fi

current_hash() {
  awk -F '"' '/vendorHash = / { print $2; exit }' "$pkg"
}

set_hash() {
  awk -v h="$1" '
    /vendorHash = / { sub(/vendorHash = "[^"]*"/, "vendorHash = \"" h "\"") }
    { print }
  ' "$pkg" >"$pkg.tmp"
  mv "$pkg.tmp" "$pkg"
}

# Build only the Go-modules FOD. callPackage with currentSystem so this works
# on the Colima Linux VM even though the flake's default packages are linux.
build_gomodules() {
  nix --extra-experimental-features 'nix-command flakes' \
    build --no-link --impure --expr "
      let
        flake = builtins.getFlake \"path:$root\";
        pkgs = import flake.inputs.nixpkgs { system = builtins.currentSystem; };
      in (pkgs.callPackage $root/nix/package.nix {}).goModules
    "
}

got_hash_from() {
  printf '%s\n' "$1" | awk '/got:/{ print $NF; found=1 } END { exit !found }'
}

cmd=${1:-}
case $cmd in
  check)
    set +e
    log=$(build_gomodules 2>&1)
    status=$?
    set -e
    if [ "$status" -eq 0 ]; then
      echo "vendorHash ok ($(current_hash))"
      exit 0
    fi
    printf '%s\n' "$log" >&2
    if printf '%s\n' "$log" | grep -q 'hash mismatch'; then
      echo "vendorHash is stale. Run: make update-vendor-hash" >&2
    fi
    exit 1
    ;;
  update)
    orig=$(current_hash)
    if [ -z "$orig" ]; then
      echo "could not find vendorHash in $pkg" >&2
      exit 1
    fi
    set_hash "$dummy"
    restore() { set_hash "$orig"; }
    trap restore EXIT
    set +e
    log=$(build_gomodules 2>&1)
    status=$?
    set -e
    if [ "$status" -eq 0 ]; then
      echo "build succeeded with a dummy hash; that should not happen" >&2
      echo "$log" >&2
      exit 1
    fi
    got=$(got_hash_from "$log") || {
      echo "$log" >&2
      echo "nix build failed for a reason other than vendorHash" >&2
      exit 1
    }
    trap - EXIT
    set_hash "$got"
    if ! build_gomodules; then
      restore
      echo "updated vendorHash did not build" >&2
      exit 1
    fi
    echo "vendorHash=$got"
    ;;
  *)
    usage
    ;;
esac
