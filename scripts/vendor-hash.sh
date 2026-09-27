#!/usr/bin/env bash
# Recomputes vendorHash in nix/package.nix after go.mod/go.sum changes.
# Building with lib.fakeHash forces a fresh download, so a cached vendor
# directory can never mask a stale hash.
set -euo pipefail

pkg=nix/package.nix
sed -i -E 's|vendorHash = "[^"]*";|vendorHash = lib.fakeHash;|' "$pkg"
got=$(nix build .#default.goModules --no-link 2>&1 | sed -n 's/^ *got: *//p' || true)
if [[ -z $got ]]; then
	git checkout -- "$pkg"
	echo "could not determine the vendor hash" >&2
	exit 1
fi
sed -i "s|vendorHash = lib.fakeHash;|vendorHash = \"$got\";|" "$pkg"
echo "vendorHash = $got"
