resource "openwrt_file" "banner" {
  path    = "/etc/banner"
  content = "Managed by OpenTofu\n"
}

# Secrets: write-only content, only a salted fingerprint is kept.
resource "openwrt_file" "tls_key" {
  path               = "/etc/uhttpd.key"
  mode               = "0600"
  content_wo         = var.tls_key_pem
  content_wo_version = 1
}
