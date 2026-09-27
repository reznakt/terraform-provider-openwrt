# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

Terraform/OpenTofu provider (terraform-plugin-framework, protocol 6) that manages OpenWrt routers through the ubus JSON-RPC endpoint served by uhttpd (`/ubus`), applying every change with `uci apply` rollback protection.

## Commands

Everything runs inside `nix develop` (Go, OpenTofu, just, treefmt, golangci-lint, shellcheck, actionlint, tfplugindocs, MkDocs, VM helpers; it also exports the `TF_ACC_*` variables the tests need). Tasks are in the `justfile`:

```sh
just test                     # unit tests + OpenTofu-driven tests against the in-memory fake router
just test-one TestDHCPHost    # tests matching a pattern, verbose
just acc                      # TestAcc* against a throwaway OpenWrt QEMU VM (boots, runs, stops it)
just acc -run TestAccRollback # extra args go to `go test`; OPENWRT_ACC_PACKAGES=1 enables the package test
just vm-up / just vm-down     # VM by hand: http://127.0.0.1:18080, root, empty password
just fmt / just lint          # treefmt (gofmt, nixfmt, shfmt, tofu fmt); lint = format check + golangci-lint + shellcheck + actionlint
just docs / just docs-check   # regenerate docs/ (never edit it by hand) / fail if stale
just examples                 # tofu validate examples/takeover against the working-tree provider
just vendor-hash              # recompute vendorHash in nix/package.nix after go.mod/go.sum changes
just check                    # lint + test + docs-check + examples + nix flake check (CI minus the VM)
```

- `internal/provider` tests need a Terraform/OpenTofu binary via `TF_ACC_TERRAFORM_PATH` (set by the dev shell); they run without `TF_ACC`. Acceptance tests (`acc_test.go`) skip without `TF_ACC` and refuse any non-loopback `OPENWRT_ENDPOINT`.
- Never hand-edit `vendorHash`: a plain `nix build` silently reuses a cached vendor directory, so a stale hash can pass locally. `just vendor-hash` builds with `lib.fakeHash` to force a fresh download.
- `scripts/with-dev-provider.sh <cmd>` builds the provider and runs `<cmd>` with an OpenTofu `dev_overrides` config; docs generation and example validation use it.
- CI (`.github/workflows/ci.yml`): lint/docs/examples, `nix flake check`, and the VM acceptance suite (KVM enabled on the runner). `pages.yml` publishes `nix build .#docs-site`.

## Architecture

Request path: resource → `rpc.Client` → `uci.Manager` / `uci.Client` → `ubus.Client` → HTTP JSON-RPC.

- `internal/ubus`: JSON-RPC client. Logs in lazily with `session.login`, re-logs in once on JSON-RPC error -32002 (expired session), maps ubus status codes to `*ubus.Error` (`IsNotFound`, `HasStatus`). Its log hook receives only object/method/status, never arguments.
- `internal/uci`: typed wrapper over the rpcd `uci` object, plus `Manager.Transact`, the only way to mutate config. It holds a provider-wide mutex (rpcd allows one pending rollback, Terraform runs resources in parallel), reverts leftovers, stages changes in the rpcd session, skips apply when `changes` is empty, then `apply{rollback:true}` → holdoff → `confirm` over fresh connections. rpcd decides the outcome: confirm succeeds, or returns NO_DATA after it reverted (`ErrRolledBack`); the loop waits `Timeout + Grace` for that answer. `rollback = false` means commit + `reload_config`.
- `internal/rpc`: `rpc.Client` is the `ProviderData` handed to every resource; it bundles ubus + uci manager and wraps `file`, `rc` and the LuCI package-manager helper.
- `internal/sections`: typed UCI resources are data, not code. Each `Spec` (config, type, options with kind/enum/required/sensitive) becomes `openwrt_<Name>`. To add a section type, add a table entry. `TestSpecs` enforces attribute naming and that every option whose name matches the secret pattern is either `Sensitive` or explicitly `notSecret`.
- `internal/resources/section.go`: the engine turning a `Spec` into schema + CRUD. Semantics that matter:
  - A typed resource owns the options it declares: null attribute = option deleted on the router. Undeclared options are untouched unless listed in `extra_options`/`extra_lists`.
  - `section` is the UCI name. Changing it renames in place; `id` (`config.section`) is computed in `ModifyPlan` for that reason.
  - `Singleton` specs adopt the existing section on create (renaming an anonymous one) and only forget it on destroy.
  - Import accepts `name`, `config.name` or `@type[idx]`. `TypeFrom` builds dynamic types (`wireguard_<iface>`).
- `internal/resources/uci_section.go`: the generic `openwrt_uci_section`. It is authoritative for the whole section. On read, secret-looking options go to `sensitive_options`, unless prior state already placed the key in `options`.
- Write-only secrets (`<attr>_wo` + `<attr>_wo_version`, `sensitive_options_wo`, `openwrt_file.content_wo`): the value is sent only when the version changes. A salted SHA-256 fingerprint (`woState` in private state, `common.go`) detects drift, and Read clears the version to force a resend. Values never go to state.
- `internal/secrets`: the single rule for "looks like a secret" (`key|pass|psk|secret|token|auth` minus an allow-list). It is used for schema checks, validation warnings, data-source redaction and log masking (`maskedContext` in `internal/provider`).
- `internal/ubustest`: in-memory fake of uhttpd + rpcd (sessions, per-session staged uci, apply/confirm/rollback, file, rc, a few status objects). It must mirror real rpcd quirks, which VM runs revealed:
  - `uci get` of a missing section returns status 0 with no data, not NOT_FOUND.
  - `uci delete` of a missing option returns NOT_FOUND.
  - `uci add` into a config file that doesn't exist returns NOT_FOUND.
  - `rc list` for an unknown service returns an empty object.
  - LuCI's `package-manager-call` always exits 0 and reports `{code,stdout,stderr}` as JSON. With opkg, `list-installed` prints the opkg status file.
- `nix/`: `package.nix` (provider, `testEnv`), `vm-image.nix` (official x86-64 image with `router/*.json` ACLs injected offline via `inject-acls.sh`/debugfs), `vm.nix` (wraps `scripts/*.sh`), `docs-site.nix`. `flake.nix` is only an index.
- `router/`: rpcd ACL files documented for users and baked into the test VM. Any new ubus call or file path a resource needs must be granted here, or real routers answer "permission denied".

## Conventions

- Keep the repository generic: examples and fixtures use stock OpenWrt defaults (192.168.1.1, SSID `OpenWrt`) and obviously fake secrets, never data or details from a real deployment.
- Errors and diagnostics carry config/section/option names only, never values. `TestSecretsNeverLogged` checks TRACE logs for secret canaries.
- Acceptance tests must leave nothing destructive for the harness's final destroy. For example, the rollback test uses the `uhttpd` singleton (destroy only forgets it) instead of importing `network.lan`.
