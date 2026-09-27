resource "openwrt_dhcp_host" "nas" {
  section = "nas"
  name    = "nas"
  mac     = ["aa:bb:cc:dd:ee:ff"]
  ip      = "192.168.1.10"
  dns     = true
}
