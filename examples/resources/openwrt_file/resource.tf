resource "openwrt_file" "banner" {
  path    = "/etc/banner"
  content = "Managed by OpenTofu\n"
}

# Secrets: write-only content, only a salted fingerprint is kept. It is
# uploaded again whenever var.tls_key_pem changes.
resource "openwrt_file" "tls_key" {
  path       = "/etc/uhttpd.key"
  mode       = "0600"
  content_wo = var.tls_key_pem
}

# Or upload a local secret file, e.g. one decrypted by sops-nix. Only the
# path is kept in state.
resource "openwrt_file" "wg_key" {
  path             = "/etc/wireguard/wg0.key"
  mode             = "0600"
  sensitive_source = "/run/secrets/openwrt/wg0-key"
}
