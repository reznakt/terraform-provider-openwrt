#!/usr/bin/env bash
# Usage: with-dev-provider.sh <command> [args...]
# Builds the provider from the working tree and runs the command with an
# OpenTofu CLI config whose dev_overrides point reznakt/openwrt at that build.
set -euo pipefail

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

go build -o "$work/bin/terraform-provider-openwrt" .
cat >"$work/tofurc" <<RC
provider_installation {
  dev_overrides {
    "reznakt/openwrt" = "$work/bin"
  }
  direct {}
}
RC

TF_CLI_CONFIG_FILE="$work/tofurc" "$@"
