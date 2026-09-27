#!/usr/bin/env bash
# Regenerates docs/ from the provider schema, templates/ and examples/.
# Run from the repository root inside `nix develop`.
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
mkdir "$work/cfg"
cat >"$work/cfg/main.tf" <<HCL
terraform {
  required_providers {
    openwrt = { source = "reznakt/openwrt" }
  }
}
HCL

# tfplugindocs looks the schema up by the bare provider name.
(cd "$work/cfg" && TF_CLI_CONFIG_FILE="$work/tofurc" tofu providers schema -json) |
	jq '.provider_schemas |= with_entries(.key = "openwrt")' >"$work/schema.json"

tfplugindocs generate --provider-name openwrt --providers-schema "$work/schema.json"
