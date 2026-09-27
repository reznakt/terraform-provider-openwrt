# Anything without a typed resource, e.g. a WireGuard interface.
resource "openwrt_uci_section" "wg0" {
  config  = "network"
  type    = "interface"
  section = "wg0"

  options = {
    proto       = "wireguard"
    listen_port = "51820"
  }
  lists = {
    addresses = ["10.7.0.1/24"]
  }

  # Never stored in state; bump the version to rotate.
  sensitive_options_wo = {
    private_key = var.wg_private_key
  }
  sensitive_options_wo_version = 1
}
