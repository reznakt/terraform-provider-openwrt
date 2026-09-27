resource "openwrt_network_device" "br_lan" {
  section = "br_lan"
  name    = "br-lan"
  type    = "bridge"
  ports   = ["lan1", "lan2", "lan3", "lan4"]
}

resource "openwrt_network_interface" "lan" {
  section   = "lan"
  proto     = "static"
  device    = openwrt_network_device.br_lan.name
  ipaddr    = "192.168.1.1"
  netmask   = "255.255.255.0"
  ip6assign = 60
}

resource "openwrt_network_interface" "wan" {
  section = "wan"
  proto   = "dhcp"
  device  = "wan"
}

resource "openwrt_network_interface" "wan6" {
  section = "wan6"
  proto   = "dhcpv6"
  device  = "wan"
}
