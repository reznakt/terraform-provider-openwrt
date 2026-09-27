---
page_title: "Handling secrets"
---

# Handling secrets

Wi-Fi keys, WireGuard keys, PPPoE and RADIUS passwords and rpcd password
hashes are secrets. The provider treats an option as secret when its name
matches `key|pass|psk|secret|token|auth` (with a short allow-list of harmless
names such as `public_key`).

## Three levels

| Attribute | In plan output | In state | Drift detection |
|---|---|---|---|
| `key = var.x` (sensitive) | hidden | yes | exact |
| `key_wo = var.x` + `key_wo_version` | no | **no** | salted fingerprint in private state |
| `ephemeral "openwrt_file"` | no | no | n/a (read-only) |

With write-only attributes the value is sent only when `*_wo_version`
changes. If someone changes the secret on the router, the provider notices
(the fingerprint no longer matches) and the next plan sends it again.

## Generic sections

`openwrt_uci_section` keeps secret-looking options out of `options`: use
`sensitive_options`, or `sensitive_options_wo` with
`sensitive_options_wo_version`. On import, secret-looking options land in
`sensitive_options`. Putting one in `options` produces a warning.

## Data sources

`openwrt_uci_section(s)` withhold secret-looking values unless
`include_sensitive = true`, and list their names in `redacted_options`.
`openwrt_wireless_status` never reads keys.

## State and logs

* Encrypt state with OpenTofu's `encryption` block (see `examples/takeover`).
* Feed secrets from `sensitive`/`ephemeral` variables sourced from sops,
  agenix or the environment; never literals.
* Provider logs (even at `TRACE`) mask the password, session ids and any
  secret-looking field; the test suite checks this.
