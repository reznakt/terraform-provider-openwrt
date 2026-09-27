---
page_title: "Router setup"
---

# Router setup

The provider works with the `root` login out of the box, but a dedicated
login with a narrower ACL is safer.

1. Copy the ACL definition to the router:

   ```sh
   scp -O router/terraform-acl.json root@router:/usr/share/rpcd/acl.d/terraform.json
   ```

   It grants UCI read/write, file access below `/etc`, service control, and
   the LuCI package-manager helper. `router/terraform-acl-exec.json` adds
   `exec` on `/bin/sh` for `openwrt_exec`, which is equivalent to root; only
   install it if you need that resource.

2. Create the login with a crypt hash instead of a plaintext password:

   ```sh
   hash=$(uhttpd -m 'a long random password')
   uci add rpcd login
   uci set rpcd.@login[-1].username=terraform
   uci set rpcd.@login[-1].password="$hash"
   uci add_list rpcd.@login[-1].read=terraform
   uci add_list rpcd.@login[-1].write=terraform
   uci commit rpcd && service rpcd restart
   ```

3. Configure the provider with `username = "terraform"` and the password in
   `OPENWRT_PASSWORD` or `password_file`.

Once the provider works, manage the login itself with `openwrt_rpcd_login`
(using `password_wo`).

## TLS

uhttpd's self-signed certificate usually has no IP SAN, so it fails
verification when addressed by IP. Either install a proper certificate and
set `ca_cert`, or use `insecure = true` on a network you trust. Avoid plain
`http://`: the password and every secret you manage would travel in the clear.
