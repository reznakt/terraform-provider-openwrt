package provider_test

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/reznakt/terraform-provider-openwrt/internal/provider"
	"github.com/reznakt/terraform-provider-openwrt/internal/ubustest"
)

// These tests drive a real OpenTofu/Terraform binary (TF_ACC_TERRAFORM_PATH)
// against the in-memory fake router, so they run without TF_ACC or a VM.

const (
	testUser = "root"
	testPass = "fake-provider-password"
)

func factories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"openwrt": providerserver.NewProtocol6WithError(provider.New("test")()),
	}
}

func newFake(t *testing.T) *ubustest.Server {
	t.Helper()
	f := ubustest.New(testUser, testPass)
	t.Cleanup(f.Close)
	return f
}

func providerBlock(f *ubustest.Server) string {
	return fmt.Sprintf(`
provider "openwrt" {
  endpoint      = %q
  username      = %q
  password      = %q
  apply_holdoff = 0
}
`, f.Endpoint(), testUser, testPass)
}

// noSecretInState fails if value appears anywhere in state.
func noSecretInState(value string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		for addr, rs := range s.RootModule().Resources {
			for k, v := range rs.Primary.Attributes {
				if strings.Contains(v, value) {
					return fmt.Errorf("secret found in state at %s.%s", addr, k)
				}
			}
		}
		return nil
	}
}

func fakeHas(f *ubustest.Server, cfg, section, key string, want any) resource.TestCheckFunc {
	return func(*terraform.State) error {
		s := f.Section(cfg, section)
		if s == nil {
			return fmt.Errorf("%s.%s does not exist on the router", cfg, section)
		}
		got := s.Values[key]
		if fmt.Sprint(got) != fmt.Sprint(want) {
			return fmt.Errorf("%s.%s.%s = %v, want %v", cfg, section, key, got, want)
		}
		return nil
	}
}

func fakeMissing(f *ubustest.Server, cfg, section string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if f.Section(cfg, section) != nil {
			return fmt.Errorf("%s.%s still exists", cfg, section)
		}
		return nil
	}
}

func TestDHCPHostLifecycle(t *testing.T) {
	f := newFake(t)
	cfg := func(ip string) string {
		return providerBlock(f) + fmt.Sprintf(`
resource "openwrt_dhcp_host" "printer" {
  section = "printer"
  name    = "printer"
  mac     = ["aa:bb:cc:dd:ee:ff"]
  ip      = %q
  dns     = true
}
`, ip)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		CheckDestroy:             fakeMissing(f, "dhcp", "printer"),
		Steps: []resource.TestStep{
			{
				Config: cfg("192.168.1.5"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("openwrt_dhcp_host.printer", "id", "dhcp.printer"),
					fakeHas(f, "dhcp", "printer", "ip", "192.168.1.5"),
					fakeHas(f, "dhcp", "printer", "dns", "1"),
					fakeHas(f, "dhcp", "printer", "mac", []string{"aa:bb:cc:dd:ee:ff"}),
				),
			},
			{
				Config: cfg("192.168.1.6"),
				Check:  fakeHas(f, "dhcp", "printer", "ip", "192.168.1.6"),
			},
			{
				ResourceName:      "openwrt_dhcp_host.printer",
				ImportState:       true,
				ImportStateId:     "printer",
				ImportStateVerify: true,
			},
			{
				// Drift on a managed option is detected and corrected.
				PreConfig: func() { f.SetOption("dhcp", "printer", "ip", "10.0.0.1") },
				Config:    cfg("192.168.1.6"),
				Check:     fakeHas(f, "dhcp", "printer", "ip", "192.168.1.6"),
			},
		},
	})
}

func TestSingletonAdoptsAnonymousSection(t *testing.T) {
	f := newFake(t)
	anon := f.AddSection("system", "system", "", map[string]any{"hostname": "OpenWrt", "ttylogin": "0", "log_size": "64"})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		// Destroying a singleton only forgets it.
		CheckDestroy: func(*terraform.State) error {
			if f.Section("system", "main") == nil {
				return fmt.Errorf("singleton was deleted")
			}
			return nil
		},
		Steps: []resource.TestStep{{
			Config: providerBlock(f) + `
resource "openwrt_system" "this" {
  section  = "main"
  hostname = "router1"
  timezone = "CET-1CEST,M3.5.0,M10.5.0/3"
  log_size = 64
}
`,
			Check: resource.ComposeTestCheckFunc(
				fakeMissing(f, "system", anon),
				fakeHas(f, "system", "main", "hostname", "router1"),
				// ttylogin is declared by the resource but unset in HCL: removed.
				fakeHas(f, "system", "main", "ttylogin", nil),
			),
		}},
	})
}

func TestWirelessKeyWriteOnly(t *testing.T) {
	f := newFake(t)
	const psk = "correct-horse-battery-staple"
	cfg := func(version int) string {
		return providerBlock(f) + fmt.Sprintf(`
resource "openwrt_wireless_iface" "home" {
  section            = "home"
  device             = "radio0"
  mode               = "ap"
  network            = ["lan"]
  ssid               = "home"
  encryption         = "sae-mixed"
  key_wo             = %q
  key_wo_version     = %d
}
`, psk, version)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: cfg(1),
				Check: resource.ComposeTestCheckFunc(
					fakeHas(f, "wireless", "home", "key", psk),
					noSecretInState(psk),
					resource.TestCheckNoResourceAttr("openwrt_wireless_iface.home", "key"),
				),
			},
			{
				// Someone changes the key on the router: the plan must notice.
				PreConfig:          func() { f.SetOption("wireless", "home", "key", "tampered") },
				Config:             cfg(1),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: cfg(1),
				Check:  resource.ComposeTestCheckFunc(fakeHas(f, "wireless", "home", "key", psk), noSecretInState(psk)),
			},
		},
	})
}

func TestWirelessKeySensitiveInState(t *testing.T) {
	f := newFake(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{
			Config: providerBlock(f) + `
resource "openwrt_wireless_iface" "guest" {
  section    = "guest"
  device     = "radio0"
  mode       = "ap"
  ssid       = "guest"
  encryption = "psk2"
  key        = "guest-password-123"
  isolate    = true
}
`,
			Check: fakeHas(f, "wireless", "guest", "key", "guest-password-123"),
		}},
	})
}

func TestGenericSectionAuthoritative(t *testing.T) {
	f := newFake(t)
	cfg := providerBlock(f) + `
resource "openwrt_uci_section" "wg" {
  config  = "network"
  type    = "interface"
  section = "wg0"
  options = { proto = "wireguard", listen_port = "51820" }
  lists   = { addresses = ["10.7.0.1/24"] }
  sensitive_options = { private_key = "fake-wg-private-key=" }
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		CheckDestroy:             fakeMissing(f, "network", "wg0"),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeTestCheckFunc(
					fakeHas(f, "network", "wg0", "private_key", "fake-wg-private-key="),
					fakeHas(f, "network", "wg0", "addresses", []string{"10.7.0.1/24"}),
				),
			},
			{
				// An option added out of band is removed again.
				PreConfig: func() { f.SetOption("network", "wg0", "mtu", "1280") },
				Config:    cfg,
				Check:     fakeHas(f, "network", "wg0", "mtu", nil),
			},
			{
				// Import sorts the secret into sensitive_options, not options.
				ResourceName:      "openwrt_uci_section.wg",
				ImportState:       true,
				ImportStateId:     "network.wg0",
				ImportStateVerify: true,
			},
		},
	})
}

func TestRenameInPlace(t *testing.T) {
	f := newFake(t)
	cfg := func(name string) string {
		return providerBlock(f) + fmt.Sprintf(`
resource "openwrt_firewall_zone" "iot" {
  section = %q
  name    = "iot"
  network = ["iot"]
  input   = "REJECT"
  output  = "ACCEPT"
  forward = "REJECT"
}
`, name)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg("zone_iot"), Check: fakeHas(f, "firewall", "zone_iot", "input", "REJECT")},
			{Config: cfg("iot_zone"), Check: resource.ComposeTestCheckFunc(fakeMissing(f, "firewall", "zone_iot"), fakeHas(f, "firewall", "iot_zone", "name", "iot"))},
		},
	})
}

func TestImportAnonymousAndRename(t *testing.T) {
	f := newFake(t)
	f.AddSection("firewall", "zone", "", map[string]any{"name": "lan", "network": []string{"lan"}, "input": "ACCEPT", "output": "ACCEPT", "forward": "ACCEPT"})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: providerBlock(f) + `
import {
  to = openwrt_firewall_zone.lan
  id = "@zone[0]"
}
resource "openwrt_firewall_zone" "lan" {
  section = "lan_zone"
  name    = "lan"
  network = ["lan"]
  input   = "ACCEPT"
  output  = "ACCEPT"
  forward = "ACCEPT"
}
`,
				Check: fakeHas(f, "firewall", "lan_zone", "name", "lan"),
			},
		},
	})
}

func TestRollbackSurfacesError(t *testing.T) {
	f := newFake(t)
	f.AddSection("network", "interface", "lan", map[string]any{"proto": "static", "ipaddr": "192.168.1.1"})
	f.RollbackOnApply.Store(true)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{
			Config: providerBlock(f) + `
resource "openwrt_uci_section" "lan" {
  config  = "network"
  type    = "interface"
  section = "lan2"
  options = { proto = "static", ipaddr = "10.9.9.9" }
}
`,
			ExpectError: regexp.MustCompile(`rolled back`),
		}},
	})
	if f.Section("network", "lan2") != nil {
		t.Fatal("rolled back section exists")
	}
}

func TestFileWriteOnlyAndDrift(t *testing.T) {
	f := newFake(t)
	const secret = "-----BEGIN FAKE KEY-----"
	cfg := providerBlock(f) + fmt.Sprintf(`
resource "openwrt_file" "key" {
  path               = "/etc/test.key"
  mode               = "0600"
  content_wo         = %q
  content_wo_version = 1
}
resource "openwrt_file" "motd" {
  path    = "/etc/banner.extra"
  content = "hello\n"
}
`, secret)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeTestCheckFunc(
					noSecretInState(secret),
					resource.TestCheckNoResourceAttr("openwrt_file.key", "md5"),
					resource.TestCheckResourceAttr("openwrt_file.key", "mode", "0600"),
					func(*terraform.State) error {
						if got := string(f.Files["/etc/test.key"].Data); got != secret {
							return fmt.Errorf("key file = %q", got)
						}
						return nil
					},
				),
			},
			{
				PreConfig: func() {
					f.Files["/etc/test.key"].Data = []byte("tampered")
					f.Files["/etc/banner.extra"].Data = []byte("tampered")
				},
				Config:             cfg,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: cfg,
				Check: func(*terraform.State) error {
					if string(f.Files["/etc/test.key"].Data) != secret || string(f.Files["/etc/banner.extra"].Data) != "hello\n" {
						return fmt.Errorf("drift not corrected")
					}
					return nil
				},
			},
		},
	})
}

func TestUCIOrder(t *testing.T) {
	f := newFake(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{
			Config: providerBlock(f) + `
resource "openwrt_firewall_rule" "a" {
  section = "rule_a"
  name    = "a"
  src     = "wan"
  target  = "ACCEPT"
}
resource "openwrt_firewall_rule" "b" {
  section = "rule_b"
  name    = "b"
  src     = "wan"
  target  = "DROP"
}
resource "openwrt_uci_order" "rules" {
  config   = "firewall"
  sections = [openwrt_firewall_rule.b.section, openwrt_firewall_rule.a.section]
}
`,
			Check: func(*terraform.State) error {
				names := f.SectionNames("firewall")
				if len(names) < 2 || names[0] != "rule_b" || names[1] != "rule_a" {
					return fmt.Errorf("order = %v", names)
				}
				return nil
			},
		}},
	})
}

func TestDataSourcesRedactSecrets(t *testing.T) {
	f := newFake(t)
	f.AddSection("wireless", "wifi-iface", "default_radio0", map[string]any{"ssid": "OpenWrt", "key": "fake-psk-never-exported", "mode": "ap"})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{
			Config: providerBlock(f) + `
data "openwrt_board" "b" {}
data "openwrt_uci_section" "wifi" {
  config  = "wireless"
  section = "@wifi-iface[0]"
}
data "openwrt_wireless_status" "w" {}
data "openwrt_network_interface" "lan" { name = "lan" }
data "openwrt_dhcp_leases" "l" {}
`,
			Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr("data.openwrt_board.b", "version", "24.10.5"),
				resource.TestCheckResourceAttr("data.openwrt_uci_section.wifi", "options.ssid", "OpenWrt"),
				resource.TestCheckNoResourceAttr("data.openwrt_uci_section.wifi", "options.key"),
				resource.TestCheckResourceAttr("data.openwrt_uci_section.wifi", "redacted_options.0", "key"),
				resource.TestCheckResourceAttr("data.openwrt_wireless_status.w", "radios.0.interfaces.0.ssid", "OpenWrt"),
				resource.TestCheckResourceAttr("data.openwrt_network_interface.lan", "ipv4_addresses.0", "192.168.1.1/24"),
				resource.TestCheckResourceAttr("data.openwrt_dhcp_leases.l", "ipv4.0.hostname", "laptop"),
				noSecretInState("fake-psk-never-exported"),
			),
		}},
	})
}

func TestProviderPasswordNeverSentOutsideLogin(t *testing.T) {
	f := newFake(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{
			Config: providerBlock(f) + `data "openwrt_board" "b" {}`,
			Check:  noSecretInState(testPass),
		}},
	})
	for _, line := range strings.Split(f.RawLog(), "\n") {
		if strings.Contains(line, testPass) && !strings.Contains(line, `"login"`) {
			t.Fatalf("password sent outside session.login: %s", line)
		}
	}
}

func TestSecretsNeverLogged(t *testing.T) {
	f := newFake(t)
	logPath := t.TempDir() + "/provider.log"
	t.Setenv("TF_LOG", "TRACE")
	t.Setenv("TF_ACC_LOG_PATH", logPath)
	const psk = "log-leak-canary-psk"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{
			Config: providerBlock(f) + fmt.Sprintf(`
resource "openwrt_wireless_iface" "x" {
  section    = "x"
  device     = "radio0"
  mode       = "ap"
  key        = %q
}
resource "openwrt_uci_section" "y" {
  config            = "network"
  type              = "interface"
  section           = "y"
  sensitive_options = { password = %q }
}
`, psk, psk),
		}},
	})
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "ubus call") {
		t.Fatal("no provider ubus log lines: logging is not wired up, the test proves nothing")
	}
	for i, line := range strings.Split(string(raw), "\n") {
		// The test harness echoes the HCL it writes (lines prefixed "  | ");
		// that is test scaffolding, not provider output.
		if strings.HasPrefix(line, "  | ") {
			continue
		}
		for _, canary := range []string{psk, testPass} {
			if strings.Contains(line, canary) {
				t.Errorf("%q found in TRACE log line %d", canary, i+1)
			}
		}
	}
}
