package sections

import (
	"regexp"
	"testing"

	"github.com/reznakt/terraform-provider-openwrt/internal/secrets"
)

var attrRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Attribute names the engine adds to every typed resource.
var engineAttrs = map[string]bool{"id": true, "section": true, "extra_options": true, "extra_lists": true}

// Names Terraform reserves in resource blocks.
var reserved = map[string]bool{"count": true, "for_each": true, "provider": true, "lifecycle": true, "depends_on": true, "connection": true, "provisioner": true}

func TestSpecs(t *testing.T) {
	names := map[string]bool{}
	for _, s := range All() {
		if names[s.Name] {
			t.Errorf("duplicate resource %s", s.Name)
		}
		names[s.Name] = true
		if s.Config == "" || s.Type == "" || s.Description == "" {
			t.Errorf("%s: config, type and description are required", s.Name)
		}
		attrs := map[string]bool{}
		if s.TypeFrom != "" {
			attrs[s.TypeFrom] = true
		}
		for _, o := range s.Options {
			a := o.AttrName()
			if !attrRe.MatchString(a) || reserved[a] || engineAttrs[a] {
				t.Errorf("%s.%s: bad attribute name %q", s.Name, o.UCI, a)
			}
			if attrs[a] || attrs[a+"_wo"] {
				t.Errorf("%s: duplicate attribute %s", s.Name, a)
			}
			attrs[a] = true
			if o.Sensitive {
				attrs[a+"_wo"], attrs[a+"_wo_version"] = true, true
				if o.Kind != String {
					t.Errorf("%s.%s: only strings can be sensitive", s.Name, o.UCI)
				}
			}
			if o.Description == "" {
				t.Errorf("%s.%s: missing description", s.Name, o.UCI)
			}
			if len(o.Enum) > 0 && o.Kind != String {
				t.Errorf("%s.%s: enum on non-string", s.Name, o.UCI)
			}
			// Secret-looking options must be deliberately classified.
			if secrets.LooksSecret(o.UCI) && !o.Sensitive && !o.NotSecret {
				t.Errorf("%s.%s: looks like a secret but is neither Sensitive nor NotSecret", s.Name, o.UCI)
			}
		}
	}
}

func TestSnakeCase(t *testing.T) {
	for in, want := range map[string]string{
		"RootPasswordAuth": "root_password_auth", "SSHKeepAlive": "ssh_keep_alive",
		"Port": "port", "ra_flags": "ra_flags", "ra-flags": "ra_flags", "ip6gw": "ip6gw",
	} {
		if got := SnakeCase(in); got != want {
			t.Errorf("SnakeCase(%q) = %q, want %q", in, got, want)
		}
	}
}
