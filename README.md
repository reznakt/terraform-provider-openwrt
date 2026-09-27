# terraform-provider-openwrt

Terraform/OpenTofu provider that manages OpenWrt routers through the ubus
JSON-RPC API served by uhttpd, the same API LuCI uses. Every change is applied
with `uci apply` rollback protection: if a change cuts the provider off, the
router reverts it on its own.

* 44 typed resources generated from declarative specs (network, wireless,
  firewall, DHCP/DNS, system, services, SQM, qos-scripts), plus
  `openwrt_uci_section` for anything else and `openwrt_uci_order` for rule order.
* `openwrt_package`, `openwrt_file`, `openwrt_service`, `openwrt_exec`.
* Data sources for board/system info, UCI, interface and wireless status,
  packages, DHCP leases and files; an ephemeral `openwrt_file`.
* Secrets stay out of plans, and with `*_wo` attributes out of state too.

Documentation: https://reznakt.github.io/terraform-provider-openwrt/ (source in [docs/](docs/index.md)), including the
[router setup](docs/guides/router-setup.md) and
[secrets](docs/guides/secrets.md) guides.
`examples/takeover/` takes over a stock router by importing its sections.

## Using it locally

```sh
nix build            # result/share/terraform/plugins is a filesystem mirror
```

```hcl
# ~/.tofurc
provider_installation {
  filesystem_mirror {
    path    = "/path/to/result/share/terraform/plugins"
    include = ["reznakt/openwrt"]
  }
  direct { exclude = ["reznakt/openwrt"] }
}
```

## Development

```sh
nix develop
go test ./...           # unit tests + OpenTofu runs against an in-memory fake router
nix run .#test-acc      # acceptance tests against a throwaway OpenWrt VM (QEMU)
./scripts/gen-docs.sh   # regenerate docs/ (Markdown)
python -m mkdocs serve  # preview the HTML site; CI publishes it to GitHub Pages
nix flake check         # builds and runs the unit tests in the sandbox
```

The acceptance tests boot the official OpenWrt x86-64 image (current stable
release) with the provider's rpcd ACLs baked in (`nix/vm-image.nix`),
and refuse to run against anything but a loopback endpoint.
`nix run .#run-vm` / `.#stop-vm` start and stop the VM by hand.

| Path | Contents |
|---|---|
| `internal/ubus` | JSON-RPC client: login, session renewal, status codes |
| `internal/uci` | uci wrapper and the serialized apply/confirm/rollback manager |
| `internal/sections` | typed section specs (one table per config file) |
| `internal/resources` | spec engine, other resources, data sources |
| `internal/secrets` | the rule deciding which option names are secrets |
| `internal/ubustest` | fake uhttpd/rpcd for tests |
| `router/` | rpcd ACL files to install on the router |
| `scripts/` | VM helpers and doc generation |

Adding a section type means adding an entry to a table in `internal/sections`;
`TestSpecs` checks names and that every secret-looking option is classified.
