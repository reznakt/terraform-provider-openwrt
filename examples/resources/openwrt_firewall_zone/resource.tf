resource "openwrt_firewall_zone" "iot" {
  section = "zone_iot"
  name    = "iot"
  network = ["iot"]
  input   = "REJECT"
  output  = "ACCEPT"
  forward = "REJECT"
}
