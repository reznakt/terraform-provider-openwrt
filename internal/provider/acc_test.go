package provider_test

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/reznakt/terraform-provider-openwrt/internal/ubus"
	"github.com/reznakt/terraform-provider-openwrt/internal/uci"
)

// TestAcc* run against a real OpenWrt (the QEMU VM from `nix run .#test-acc`)
// configured through OPENWRT_ENDPOINT/USERNAME/PASSWORD. They create,
// break and roll back configuration, so they refuse to run against anything
// but a loopback address.

func accPreCheck(t *testing.T) {
	t.Helper()
	if os.Getenv("TF_ACC") == "" {
		t.Skip("TF_ACC not set")
	}
	u, err := url.Parse(os.Getenv("OPENWRT_ENDPOINT"))
	if err != nil || u.Host == "" {
		t.Fatal("OPENWRT_ENDPOINT must point at the test VM")
	}
	if ip := net.ParseIP(u.Hostname()); ip == nil || !ip.IsLoopback() {
		t.Fatalf("refusing to run destructive acceptance tests against %s: only loopback (the QEMU VM) is allowed", u.Host)
	}
}

// accProvider relies on the OPENWRT_* environment for the connection.
const accProvider = `
provider "openwrt" {
  apply_timeout = 10
}
`

// accUCI talks to the VM directly to verify results independently of the provider.
func accUCI(t *testing.T) *uci.Client {
	t.Helper()
	endpoint := os.Getenv("OPENWRT_ENDPOINT") + "/ubus"
	c, err := ubus.New(ubus.Config{Endpoint: endpoint, Username: os.Getenv("OPENWRT_USERNAME"), Password: os.Getenv("OPENWRT_PASSWORD")})
	if err != nil {
		t.Fatal(err)
	}
	return uci.NewClient(c)
}

func routerHas(u *uci.Client, cfg, section, option, want string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		s, err := u.Get(context.Background(), cfg, section)
		if err != nil {
			return fmt.Errorf("%s.%s: %w", cfg, section, err)
		}
		if got := s.Options[option]; got != want {
			return fmt.Errorf("%s.%s.%s = %q, want %q", cfg, section, option, got, want)
		}
		return nil
	}
}

func TestAccBoard(t *testing.T) {
	accPreCheck(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{
			Config: accProvider + `data "openwrt_board" "b" {}`,
			Check:  resource.TestMatchResourceAttr("data.openwrt_board.b", "version", regexp.MustCompile(`^\d+\.\d+`)),
		}},
	})
}

func TestAccDHCPHost(t *testing.T) {
	accPreCheck(t)
	u := accUCI(t)
	cfg := func(ip string) string {
		return accProvider + fmt.Sprintf(`
resource "openwrt_dhcp_host" "h" {
  section = "acc_host"
  name    = "acc-host"
  mac     = ["02:00:00:00:00:01", "02:00:00:00:00:02"]
  ip      = %q
}
`, ip)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg("192.168.1.201"), Check: routerHas(u, "dhcp", "acc_host", "ip", "192.168.1.201")},
			{Config: cfg("192.168.1.202"), Check: routerHas(u, "dhcp", "acc_host", "ip", "192.168.1.202")},
			{ResourceName: "openwrt_dhcp_host.h", ImportState: true, ImportStateId: "acc_host", ImportStateVerify: true},
		},
	})
}

func TestAccSystemSingleton(t *testing.T) {
	accPreCheck(t)
	u := accUCI(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{
			Config: accProvider + `
resource "openwrt_system" "s" {
  section  = "main"
  hostname = "acc-router"
  zonename = "UTC"
  timezone = "UTC"
}
`,
			Check: routerHas(u, "system", "main", "hostname", "acc-router"),
		}},
	})
}

func TestAccGenericSectionAndOrder(t *testing.T) {
	accPreCheck(t)
	u := accUCI(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{
			Config: accProvider + `
resource "openwrt_firewall_rule" "a" {
  section   = "acc_rule_a"
  name      = "acc-a"
  src       = "wan"
  proto     = ["tcp"]
  dest_port = "2222"
  target    = "REJECT"
}
resource "openwrt_uci_section" "b" {
  config  = "firewall"
  type    = "rule"
  section = "acc_rule_b"
  options = { name = "acc-b", src = "wan", target = "DROP", dest_port = "2223" }
  lists   = { proto = ["tcp", "udp"] }
}
resource "openwrt_uci_order" "o" {
  config   = "firewall"
  sections = [openwrt_uci_section.b.section, openwrt_firewall_rule.a.section]
}
`,
			Check: resource.ComposeTestCheckFunc(
				routerHas(u, "firewall", "acc_rule_b", "target", "DROP"),
				func(*terraform.State) error {
					secs, err := u.List(context.Background(), "firewall", "")
					if err != nil {
						return err
					}
					if secs[0].Name != "acc_rule_b" || secs[1].Name != "acc_rule_a" {
						return fmt.Errorf("unexpected order: %s, %s", secs[0].Name, secs[1].Name)
					}
					return nil
				},
			),
		}},
	})
}

func TestAccWriteOnlySecret(t *testing.T) {
	accPreCheck(t)
	u := accUCI(t)
	const psk = "acc-test-psk-canary"
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{
			Config: accProvider + fmt.Sprintf(`
# The x86 VM has no radios, hence no /etc/config/wireless.
resource "openwrt_exec" "wireless" {
  command = "touch /etc/config/wireless"
}
resource "openwrt_wireless_iface" "w" {
  depends_on     = [openwrt_exec.wireless]
  section        = "acc_wifi"
  device         = "radio0"
  mode           = "ap"
  ssid           = "acc"
  encryption     = "psk2"
  disabled       = true
  key_wo         = %q
  key_wo_version = 1
}
`, psk),
			Check: resource.ComposeTestCheckFunc(routerHas(u, "wireless", "acc_wifi", "key", psk), noSecretInState(psk)),
		}},
	})
}

func TestAccFileAndService(t *testing.T) {
	accPreCheck(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{
			Config: accProvider + `
resource "openwrt_file" "f" {
  path    = "/etc/acc-test.txt"
  mode    = "0600"
  content = "hello from terraform\n"
}
data "openwrt_file" "f" {
  path       = openwrt_file.f.path
  depends_on = [openwrt_file.f]
}
resource "openwrt_service" "ntp" {
  name    = "sysntpd"
  enabled = true
  running = true
}
resource "openwrt_exec" "e" {
  command = "echo exec-ok"
}
`,
			Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr("data.openwrt_file.f", "mode", "0600"),
				resource.TestCheckResourceAttr("data.openwrt_file.f", "size", "21"),
				resource.TestCheckResourceAttr("openwrt_service.ntp", "running", "true"),
				resource.TestCheckResourceAttr("openwrt_exec.e", "stdout", "exec-ok\n"),
			),
		}},
	})
}

// TestAccRollback moves uhttpd off the port the test reaches the VM on. The
// confirm can never get through, so the router must revert on its own and
// the provider must report it. The failed create leaves nothing in state, so
// the harness has nothing to destroy afterwards.
func TestAccRollback(t *testing.T) {
	accPreCheck(t)
	u := accUCI(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{
			Config: accProvider + `
resource "openwrt_uhttpd" "main" {
  section     = "main"
  listen_http = ["0.0.0.0:8081"]
  home        = "/www"
}
`,
			ExpectError: regexp.MustCompile(`rolled back`),
		}},
	})
	s, err := u.Get(context.Background(), "uhttpd", "main")
	if err != nil {
		t.Fatalf("router unreachable after rollback: %v", err)
	}
	if got := s.Lists["listen_http"]; len(got) == 0 || got[0] != "0.0.0.0:80" {
		t.Fatalf("uhttpd not rolled back: listen_http = %v", got)
	}
}

func TestAccPackage(t *testing.T) {
	accPreCheck(t)
	if os.Getenv("OPENWRT_ACC_PACKAGES") == "" {
		t.Skip("set OPENWRT_ACC_PACKAGES=1 to test package installs (needs internet in the VM)")
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{
			Config: accProvider + `
resource "openwrt_package" "p" {
  name = "tcpdump-mini"
}
`,
			Check: resource.TestCheckResourceAttrSet("openwrt_package.p", "version"),
		}},
	})
}
