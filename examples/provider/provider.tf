provider "openwrt" {
  endpoint = "https://192.168.1.1"
  username = "terraform"
  # Password from OPENWRT_PASSWORD or a file managed by sops/agenix:
  password_file = "/run/secrets/openwrt-terraform"

  # Trust the router's certificate instead of disabling verification.
  ca_cert = file("${path.module}/router-ca.pem")

  # Every change is confirmed or rolled back by the router within 30s.
  apply_timeout = 30
}
