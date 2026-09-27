# Tasks for terraform-provider-openwrt. Run inside `nix develop`.

set shell := ["bash", "-euo", "pipefail", "-c"]

# List recipes
default:
    @just --list

# Build the provider binary into ./terraform-provider-openwrt
build:
    go build -o terraform-provider-openwrt .

# Unit tests plus OpenTofu runs against the in-memory fake router
test *args:
    go test ./... {{ args }}

# Run tests matching a pattern, e.g. `just test-one TestDHCPHost`
test-one pattern:
    go test ./... -run '{{ pattern }}' -v

# Acceptance tests against a throwaway OpenWrt VM (QEMU)
acc *args:
    test-acc {{ args }}

# Start / stop the test VM by hand (http://127.0.0.1:18080, root, empty password)
vm-up:
    run-vm

vm-down:
    stop-vm

# Format Go, Nix, shell and HCL
fmt:
    treefmt

# Linters, formatting check and workflow lint
lint:
    treefmt --fail-on-change
    golangci-lint run ./...
    shellcheck scripts/*.sh nix/*.sh
    actionlint

# Regenerate docs/ from the provider schema, templates/ and examples/
docs:
    scripts/with-dev-provider.sh scripts/gen-docs.sh

# Fail if docs/ is out of date
docs-check: docs
    git diff --exit-code -- docs || (echo "docs/ is stale: run 'just docs'" >&2; exit 1)

# Preview the documentation site
docs-serve:
    python -m mkdocs serve

# Validate the example configurations against the current schema
examples:
    scripts/with-dev-provider.sh tofu -chdir=examples/takeover validate

# Recompute vendorHash after changing go.mod/go.sum
vendor-hash:
    scripts/vendor-hash.sh

# Everything CI runs except the VM suite
check: lint test docs-check examples
    nix flake check
