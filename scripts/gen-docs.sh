#!/usr/bin/env bash
# Regenerates docs/ from the provider schema, templates/ and examples/.
# Run through `just docs`, which provides the dev_overrides CLI config.
set -euo pipefail

cfg=$(mktemp -d)
trap 'rm -rf "$cfg"' EXIT
cat >"$cfg/main.tf" <<HCL
terraform {
  required_providers {
    openwrt = { source = "reznakt/openwrt" }
  }
}
HCL

# tfplugindocs looks the schema up by the bare provider name.
tofu -chdir="$cfg" providers schema -json |
	jq '.provider_schemas |= with_entries(.key = "openwrt")' >"$cfg/schema.json"

tfplugindocs generate --provider-name openwrt --providers-schema "$cfg/schema.json"
