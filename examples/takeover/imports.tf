# Adopt what already exists. Anonymous sections are addressed as
# @type[index] and get renamed to the `section` of their resource on apply.
# Built-in singletons (system, firewall defaults, ntp) need no import:
# creating them adopts the existing section.

import {
  to = openwrt_network_device.br_lan
  id = "@device[0]"
}

import {
  to = openwrt_network_interface.lan
  id = "lan"
}

import {
  to = openwrt_network_interface.wan
  id = "wan"
}

import {
  to = openwrt_network_interface.wan6
  id = "wan6"
}

import {
  for_each = toset(["radio0", "radio1"])
  to       = openwrt_wireless_iface.ap[each.key]
  id       = "default_${each.key}"
}

import {
  to = openwrt_firewall_zone.lan
  id = "@zone[0]"
}

import {
  to = openwrt_firewall_zone.wan
  id = "@zone[1]"
}

import {
  to = openwrt_firewall_forwarding.lan_wan
  id = "@forwarding[0]"
}

import {
  for_each = { for i, r in local.wan_rules : r.key => i }
  to       = openwrt_firewall_rule.wan[each.key]
  id       = "@rule[${each.value}]"
}

import {
  to = openwrt_dhcp_pool.lan
  id = "lan"
}
