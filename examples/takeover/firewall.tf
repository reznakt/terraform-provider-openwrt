resource "openwrt_firewall_defaults" "defaults" {
  section   = "defaults"
  input     = "REJECT"
  output    = "ACCEPT"
  forward   = "REJECT"
  syn_flood = true
}

resource "openwrt_firewall_zone" "lan" {
  section = "zone_lan"
  name    = "lan"
  network = [openwrt_network_interface.lan.section]
  input   = "ACCEPT"
  output  = "ACCEPT"
  forward = "ACCEPT"
}

resource "openwrt_firewall_zone" "wan" {
  section = "zone_wan"
  name    = "wan"
  network = [openwrt_network_interface.wan.section, openwrt_network_interface.wan6.section]
  input   = "REJECT"
  output  = "ACCEPT"
  forward = "REJECT"
  masq    = true
  mtu_fix = true
}

resource "openwrt_firewall_forwarding" "lan_wan" {
  section = "fwd_lan_wan"
  src     = openwrt_firewall_zone.lan.name
  dest    = openwrt_firewall_zone.wan.name
}

# Rules are evaluated in file order; keep them in an ordered list.
locals {
  wan_rules = [
    { key = "allow_dhcp_renew", name = "Allow-DHCP-Renew", family = "ipv4", proto = ["udp"], dest_port = "68" },
    { key = "allow_ping", name = "Allow-Ping", family = "ipv4", proto = ["icmp"], icmp_type = ["echo-request"] },
    { key = "allow_dhcpv6", name = "Allow-DHCPv6", family = "ipv6", proto = ["udp"], dest_port = "546" },
  ]
}

resource "openwrt_firewall_rule" "wan" {
  for_each = { for r in local.wan_rules : r.key => r }

  section   = each.key
  name      = each.value.name
  src       = openwrt_firewall_zone.wan.name
  target    = "ACCEPT"
  family    = each.value.family
  proto     = each.value.proto
  dest_port = try(each.value.dest_port, null)
  icmp_type = try(each.value.icmp_type, null)
}

resource "openwrt_uci_order" "wan_rules" {
  config   = "firewall"
  sections = [for r in local.wan_rules : openwrt_firewall_rule.wan[r.key].section]
}
