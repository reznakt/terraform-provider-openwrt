resource "openwrt_wireless_iface" "home" {
  section    = "home"
  device     = "radio1"
  mode       = "ap"
  network    = ["lan"]
  ssid       = "home"
  encryption = "sae-mixed"

  # Write-only: the key never appears in plan or state, and is sent again
  # whenever var.wifi_key changes.
  key_wo = var.wifi_key
}

# Or read the key from a local file, e.g. decrypted by sops-nix or agenix.
resource "openwrt_wireless_iface" "guest" {
  section    = "guest"
  device     = "radio1"
  mode       = "ap"
  network    = ["lan"]
  ssid       = "guest"
  encryption = "psk2"
  key_file   = "/run/secrets/openwrt/guest-wifi-key"
}
