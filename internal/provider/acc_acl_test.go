package provider_test

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/reznakt/terraform-provider-openwrt/internal/ubus"
)

// The VM image has every router/terraform-acl*.json installed. This login
// gets only the base group and the dhcp config, the smallest setup the
// router setup guide describes.
const (
	accRestrictedSection  = "acc_restricted"
	accRestrictedUser     = "acc-restricted"
	accRestrictedPassword = "acc-restricted"
	// openssl passwd -1 -salt acctest acc-restricted
	accRestrictedHash = "$1$acctest$rX34TdUW7zrpeZg46365g."
)

var accRestrictedGroups = []string{"terraform", "terraform-uci-dhcp"}

// accRestrictedLogin adds the login through the admin connection and removes
// it again when the test ends. rpcd reads its logins on every session.login,
// so no restart is needed.
func accRestrictedLogin(t *testing.T) {
	t.Helper()
	c, err := ubus.New(ubus.Config{
		Endpoint: os.Getenv("OPENWRT_ENDPOINT") + "/ubus",
		Username: os.Getenv("OPENWRT_USERNAME"),
		Password: os.Getenv("OPENWRT_PASSWORD"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	call := func(method string, args map[string]any) {
		t.Helper()
		if err := c.Call(ctx, "uci", method, args, nil); err != nil {
			t.Fatalf("uci %s: %v", method, err)
		}
	}
	call("add", map[string]any{"config": "rpcd", "type": "login", "name": accRestrictedSection})
	call("set", map[string]any{"config": "rpcd", "section": accRestrictedSection, "values": map[string]any{
		"username": accRestrictedUser,
		"password": accRestrictedHash,
		"read":     accRestrictedGroups,
		"write":    accRestrictedGroups,
	}})
	call("commit", map[string]any{"config": "rpcd"})
	t.Cleanup(func() {
		_ = c.Call(ctx, "uci", "delete", map[string]any{"config": "rpcd", "section": accRestrictedSection}, nil)
		_ = c.Call(ctx, "uci", "commit", map[string]any{"config": "rpcd"}, nil)
	})
}

// TestAccRestrictedLogin checks that the base ACL plus one config file is
// enough to manage that config, and that it grants nothing beyond it.
func TestAccRestrictedLogin(t *testing.T) {
	accPreCheck(t)
	accRestrictedLogin(t)
	u := accUCI(t)

	provider := fmt.Sprintf(`
provider "openwrt" {
  username      = %q
  password      = %q
  apply_timeout = 10
}
`, accRestrictedUser, accRestrictedPassword)
	host := `
resource "openwrt_dhcp_host" "h" {
  section = "acc_restricted_host"
  name    = "acc-restricted"
  mac     = ["02:00:00:00:00:03"]
  ip      = "192.168.1.203"
}
`
	denied := regexp.MustCompile(`(?i)permission denied|access denied`)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: provider + host + `
data "openwrt_board" "b" {}
`,
				Check: routerHas(u, "dhcp", "acc_restricted_host", "ip", "192.168.1.203"),
			},
			{
				// Without terraform-uci-rpcd the login cannot add another login.
				Config: provider + host + `
resource "openwrt_uci_section" "login" {
  config  = "rpcd"
  type    = "login"
  section = "acc_escalate"
  options = { username = "acc-escalate" }
}
`,
				ExpectError: denied,
			},
			{
				// Without terraform-file nothing below /etc is writable.
				Config: provider + host + `
resource "openwrt_file" "f" {
  path    = "/etc/acc-restricted.txt"
  content = "should not be written\n"
}
`,
				ExpectError: denied,
			},
			{
				// Without terraform-service init scripts are out of reach.
				Config: provider + host + `
resource "openwrt_service" "s" {
  name    = "sysntpd"
  enabled = true
  running = true
}
`,
				ExpectError: denied,
			},
		},
	})
}
