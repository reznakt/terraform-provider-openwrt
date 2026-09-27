# Read a secret generated on the router without persisting it anywhere.
ephemeral "openwrt_file" "wg_key" {
  path = "/etc/wireguard/wg0.key"
}
