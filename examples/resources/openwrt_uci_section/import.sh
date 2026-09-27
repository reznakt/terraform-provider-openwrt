# <config>.<section>, or <config>.@<type>[<index>] for anonymous sections
tofu import openwrt_uci_section.wg0 network.wg0
tofu import openwrt_uci_section.zone firewall.@zone[0]
