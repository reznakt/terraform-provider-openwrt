resource "openwrt_system" "system" {
  section  = "system"
  hostname = "OpenWrt"
  timezone = "UTC"
  zonename = "UTC"
  log_size = 64
  ttylogin = false
}

resource "openwrt_system_ntp" "ntp" {
  section = "ntp"
  enabled = true
  server  = [for i in range(4) : "${i}.openwrt.pool.ntp.org"]
}

resource "openwrt_dhcp_pool" "lan" {
  section   = "lan"
  interface = openwrt_network_interface.lan.section
  start     = 100
  limit     = 150
  leasetime = "12h"
}
