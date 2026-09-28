---
page_title: "Handling secrets"
---

# Handling secrets

Wi-Fi keys, WireGuard keys, PPPoE and RADIUS passwords and rpcd password
hashes are secrets. The provider treats an option as secret when its name
matches `key|pass|psk|secret|token|auth` (with a short allow-list of harmless
names such as `public_key`).

## Passing a secret

Every secret attribute of a typed resource (`key`, `private_key`,
`password`, ...) comes in three forms; the ephemeral `openwrt_file` reads
secrets generated on the router:

| Attribute | In plan output | In state | Drift detection |
|---|---|---|---|
| `key = var.x` (sensitive) | hidden | yes | exact |
| `key_wo = var.x` | no | **no** | salted fingerprint in private state |
| `key_file = "/run/secrets/x"` | no | path only | salted fingerprint in private state |
| `ephemeral "openwrt_file"` | no | no | n/a (read-only) |

`key_wo` and `key_file` are sent whenever the value changes: the provider
compares a salted fingerprint of the configured value with the one it
recorded when it last sent it, and plans a new `key_wo_version` when they
differ. The same fingerprint catches changes made on the router. Neither
the value nor an unsalted hash of it ever reaches state or plan files.
`key_wo` needs Terraform/OpenTofu >= 1.11.

`key_file` is read on the machine running `tofu`, at plan and at apply;
trailing newlines are dropped. It is the natural fit for
[sops-nix](https://github.com/Mic92/sops-nix) and
[agenix](https://github.com/ryantm/agenix), which decrypt secrets to files.

Set `key_wo_version` yourself only if you want the old behaviour: the value
is then sent only when you bump the number, and the plan warns when the
value changed without a bump.

## Generic sections and files

`openwrt_uci_section` keeps secret-looking options out of `options`: use
`sensitive_options` (stored in state), `sensitive_options_wo` (write-only)
or `sensitive_options_files` (option name to local path). The last two are
sent again whenever a value changes or a key is added or removed. On
import, secret-looking options land in `sensitive_options`. Putting one in
`options` produces a warning.

`openwrt_file` uploads secret files with `content_wo` or `sensitive_source`
(a local path). Neither stores the content or its MD5 in state. Plain
`source` stores the file's MD5, which is fine for configuration files but
can be brute-forced for short secrets.

## Data sources

`openwrt_uci_section(s)` withhold secret-looking values unless
`include_sensitive = true`, and list their names in `redacted_options`.
`openwrt_wireless_status` never reads keys.

## With sops and Nix

Secret values must never pass through a Nix expression: whatever Nix
evaluates can end up world-readable in `/nix/store`. Keep them in sops files
and let the configuration refer to paths or environment variables only.

### Decrypted files (sops-nix, agenix)

On a NixOS or home-manager machine that runs `tofu`, declare the secrets and
point the provider at the decrypted files:

```nix
# The files are root-only by default; let the user running tofu read them.
sops.secrets."openwrt/rpcd-password".owner = "deploy";
sops.secrets."openwrt/wifi-key".owner = "deploy";
sops.secrets."openwrt/wg0-private-key".owner = "deploy";
```

```terraform
provider "openwrt" {
  endpoint      = "https://192.168.1.1"
  password_file = "/run/secrets/openwrt/rpcd-password"
}

resource "openwrt_wireless_iface" "home" {
  # ...
  key_file = "/run/secrets/openwrt/wifi-key"
}

resource "openwrt_network_interface" "wg0" {
  section          = "wg0"
  proto            = "wireguard"
  private_key_file = "/run/secrets/openwrt/wg0-private-key"
}
```

Rotating a secret is `sops edit`, a rebuild that decrypts it again, then
`tofu apply`. The paths are plain strings, so this works unchanged when the
configuration is generated with [terranix](https://terranix.org).

### Environment variables (`sops exec-env`)

Without a module that decrypts to files, run OpenTofu under
`sops exec-env`, which exposes the decrypted values to one process only:

```yaml
# secrets.yaml, encrypted with sops
OPENWRT_PASSWORD: fake-rpcd-password
TF_VAR_wifi_key: fake-wifi-key
TF_VAR_state_passphrase: fake-state-passphrase-0123456789
```

```terraform
variable "wifi_key" {
  type      = string
  sensitive = true
  ephemeral = true
}

resource "openwrt_wireless_iface" "home" {
  # ...
  key_wo = var.wifi_key
}
```

```sh
sops exec-env secrets.yaml 'tofu apply'
```

OpenTofu lets an `ephemeral` variable reach resources only through
write-only attributes, so it rejects any attempt to store it by accident.

### Providers that decrypt sops files

Data sources of sops providers store the decrypted values in state. If you
use one, prefer its ephemeral resource (where available) and feed the
result into `*_wo` attributes only.

## State and logs

* Encrypt state and plan files with OpenTofu's `encryption` block (see
  `examples/takeover`); its passphrase can come from `sops exec-env` as
  `TF_VAR_state_passphrase`.
* Provider logs (even at `TRACE`) mask the password, session ids and any
  secret-looking field; the test suite checks this.
