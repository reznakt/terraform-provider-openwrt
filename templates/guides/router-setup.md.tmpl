---
page_title: "Router setup"
---

# Router setup

The provider works with the `root` login out of the box, but a dedicated
login with a narrower ACL is safer.

1. Copy the base ACL and one file per config you manage to the router.
   For `openwrt_dhcp_host` and `openwrt_dhcp_domain` that is:

   ```sh
   cd router
   scp -O terraform-acl.json terraform-acl-uci-dhcp.json root@router:/usr/share/rpcd/acl.d/
   ```

   The base file grants the login, the read-only data sources and the ubus
   `uci` methods, but no config. Each other file adds one feature:

   | ACL file (`router/`) | Group | Needed for |
   |---|---|---|
   | `terraform-acl.json` | `terraform` | always; `openwrt_board`, `openwrt_system_info`, `openwrt_network_interface`, `openwrt_wireless_status`, `openwrt_dhcp_leases` |
   | `terraform-acl-uci-dhcp.json` | `terraform-uci-dhcp` | `openwrt_dhcp_*` |
   | `terraform-acl-uci-network.json` | `terraform-uci-network` | `openwrt_network_*` |
   | `terraform-acl-uci-wireless.json` | `terraform-uci-wireless` | `openwrt_wireless_*` |
   | `terraform-acl-uci-firewall.json` | `terraform-uci-firewall` | `openwrt_firewall_*` (privileged, see below) |
   | `terraform-acl-uci-system.json` | `terraform-uci-system` | `openwrt_system`, `openwrt_system_ntp`, `openwrt_system_led` |
   | `terraform-acl-uci-sqm.json` | `terraform-uci-sqm` | `openwrt_sqm_queue` |
   | `terraform-acl-uci-qos.json` | `terraform-uci-qos` | `openwrt_qos_*` |
   | `terraform-acl-uci-dropbear.json` | `terraform-uci-dropbear` | `openwrt_dropbear` |
   | `terraform-acl-uci-uhttpd.json` | `terraform-uci-uhttpd` | `openwrt_uhttpd`, `openwrt_uhttpd_cert` (privileged) |
   | `terraform-acl-uci-rpcd.json` | `terraform-uci-rpcd` | `openwrt_rpcd`, `openwrt_rpcd_login` (root) |
   | `terraform-acl-uci-any.json` | `terraform-uci-any` | `openwrt_uci_section`, `openwrt_uci_order` and the `openwrt_uci_section(s)` data sources on any config (root) |
   | `terraform-acl-file.json` | `terraform-file` | `openwrt_file`, the `openwrt_file` data and ephemeral sources (root) |
   | `terraform-acl-package.json` | `terraform-package` | `openwrt_package`, `openwrt_packages` (root) |
   | `terraform-acl-service.json` | `terraform-service` | `openwrt_service` (all init scripts) |
   | `terraform-acl-exec.json` | `terraform-exec` | `openwrt_exec` (root) |

   The `openwrt_uci_section(s)` data sources on a config with its own file
   work with that file. Files marked *root* let the login become root:
   writing `/etc/init.d` or `/etc/shadow`, adding an rpcd login, running a
   package's scripts or a shell. *Privileged* ones can make fw4 or uhttpd
   load or run files by path. Only install what you need.

2. Create the login with a crypt hash instead of a plaintext password. The
   pattern `terraform*` grants every group whose file is installed, so the
   set of files in `/usr/share/rpcd/acl.d` decides what the login may do:

   ```sh
   hash=$(uhttpd -m 'a long random password')
   uci add rpcd login
   uci set rpcd.@login[-1].username=terraform
   uci set rpcd.@login[-1].password="$hash"
   uci add_list rpcd.@login[-1].read='terraform*'
   uci add_list rpcd.@login[-1].write='terraform*'
   uci commit rpcd && service rpcd restart
   ```

   To pin the login to certain groups instead, list them one by one
   (`read=terraform`, `read=terraform-uci-dhcp`, ... and the same for
   `write`).

3. Configure the provider with `username = "terraform"` and the password in
   `OPENWRT_PASSWORD` or `password_file`.

Once the provider works, you can manage the login itself with
`openwrt_rpcd_login` (using `password_wo`); that needs
`terraform-acl-uci-rpcd.json`, which makes the login root-equivalent.

## TLS

uhttpd's self-signed certificate usually has no IP SAN, so it fails
verification when addressed by IP. Either install a proper certificate and
set `ca_cert`, or use `insecure = true` on a network you trust. Avoid plain
`http://`: the password and every secret you manage would travel in the clear.
