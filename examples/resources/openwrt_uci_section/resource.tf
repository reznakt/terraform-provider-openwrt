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

  # Never stored in state; sent again whenever a value changes.
  sensitive_options_wo = {
    private_key = var.wg_private_key
  }
}

# Secrets can also come from local files, e.g. decrypted by sops-nix.
resource "openwrt_uci_section" "wan_ppp" {
  config  = "network"
  type    = "interface"
  section = "wan"

  options = {
    proto    = "pppoe"
    device   = "eth1"
    username = "fake-isp-user"
  }
  sensitive_options_files = {
    password = "/run/secrets/openwrt/pppoe-password"
  }
}
