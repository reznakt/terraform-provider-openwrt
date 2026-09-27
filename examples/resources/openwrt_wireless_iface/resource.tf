resource "openwrt_wireless_iface" "home" {
  section    = "home"
  device     = "radio1"
  mode       = "ap"
  network    = ["lan"]
  ssid       = "home"
  encryption = "sae-mixed"

  # Write-only: the key never appears in plan or state.
  key_wo         = var.wifi_key
  key_wo_version = 1
}
