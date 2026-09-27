# The key is write-only: it never lands in state or plan output.
resource "openwrt_wireless_iface" "ap" {
  for_each = toset(["radio0", "radio1"])

  section        = "default_${each.key}"
  device         = each.key
  mode           = "ap"
  network        = [openwrt_network_interface.lan.section]
  ssid           = "OpenWrt"
  encryption     = "psk2"
  key_wo         = var.wifi_key
  key_wo_version = var.wifi_key_version
}
