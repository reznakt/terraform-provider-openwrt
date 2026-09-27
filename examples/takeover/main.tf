# Takes over a router that still runs the stock OpenWrt configuration: the
# existing sections are imported (or adopted, for built-in ones), so the first
# `tofu plan` should show only renames of anonymous sections. Adjust the
# values to your router and review that plan before the first apply.

terraform {
  required_providers {
    openwrt = {
      source = "reznakt/openwrt"
    }
  }

  # The state holds the imported configuration; keep it encrypted.
  encryption {
    key_provider "pbkdf2" "state" {
      passphrase = var.state_passphrase
    }
    method "aes_gcm" "state" {
      keys = key_provider.pbkdf2.state
    }
    state {
      method   = method.aes_gcm.state
      enforced = true
    }
    plan {
      method   = method.aes_gcm.state
      enforced = true
    }
  }
}

provider "openwrt" {
  endpoint = var.endpoint
  username = var.username
  # The password comes from OPENWRT_PASSWORD or OPENWRT_PASSWORD_FILE.

  # uhttpd's self-signed certificate has no IP SAN. Replace with ca_cert
  # once the router has a certificate that matches its address.
  insecure = true
}
