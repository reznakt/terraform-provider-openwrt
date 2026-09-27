// Package sections declares typed UCI section resources as data. Each Spec
// becomes one openwrt_<Name> resource via the generic engine in
// internal/resources, so adding a section type means adding a table entry.
package sections

import (
	"regexp"
	"strings"
)

// Kind is the Terraform type of an option.
type Kind int

const (
	String Kind = iota
	Int
	Bool
	List
)

// Option describes one UCI option of a section type.
type Option struct {
	// UCI is the option name on the router.
	UCI string
	// Attr overrides the Terraform attribute name (default: snake_case of UCI).
	Attr        string
	Kind        Kind
	Required    bool
	Description string
	// Enum restricts String values.
	Enum []string
	// Sensitive marks secrets: the attribute is Sensitive and gets a
	// write-only <attr>_wo + <attr>_wo_version pair. Only for String.
	Sensitive bool
	// NotSecret overrides the secret-name heuristic for this option.
	NotSecret bool
}

// AttrName is the Terraform attribute name of the option.
func (o Option) AttrName() string {
	if o.Attr != "" {
		return o.Attr
	}
	return SnakeCase(o.UCI)
}

// Spec describes one section type managed as its own resource.
type Spec struct {
	// Name is the resource suffix: openwrt_<Name>.
	Name        string
	Config      string
	Type        string
	Description string
	// Singleton sections exist exactly once on a stock router (system,
	// firewall defaults, dnsmasq...). Create adopts the existing section
	// (renaming it if anonymous) and Delete only forgets it.
	Singleton bool
	// TypeFrom names a required, replace-forcing attribute whose value is
	// appended to Type (WireGuard peers are of type "wireguard_<iface>").
	TypeFrom     string
	TypeFromDesc string
	Options      []Option
}

var (
	camel   = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	acronym = regexp.MustCompile(`([A-Z]+)([A-Z][a-z])`)
)

// SnakeCase converts UCI names like "RootPasswordAuth", "SSHKeepAlive" or "ra-flags".
func SnakeCase(s string) string {
	s = acronym.ReplaceAllString(s, "${1}_${2}")
	s = camel.ReplaceAllString(s, "${1}_${2}")
	return strings.ToLower(strings.ReplaceAll(s, "-", "_"))
}

// All returns every typed section spec.
func All() []Spec {
	var all []Spec
	for _, group := range [][]Spec{network, wireless, firewall, dhcp, system, services} {
		all = append(all, group...)
	}
	return all
}

// Shorthands keep the tables readable.
func str(uci, desc string) Option  { return Option{UCI: uci, Kind: String, Description: desc} }
func num(uci, desc string) Option  { return Option{UCI: uci, Kind: Int, Description: desc} }
func flag(uci, desc string) Option { return Option{UCI: uci, Kind: Bool, Description: desc} }
func list(uci, desc string) Option { return Option{UCI: uci, Kind: List, Description: desc} }
func secret(uci, desc string) Option {
	return Option{UCI: uci, Kind: String, Sensitive: true, Description: desc}
}
func enum(uci, desc string, values ...string) Option {
	return Option{UCI: uci, Kind: String, Enum: values, Description: desc}
}
func required(o Option) Option { o.Required = true; return o }

// notSecret marks an option whose name looks secret but whose value is not
// (e.g. uhttpd's "key" is a file path).
func notSecret(o Option) Option { o.NotSecret = true; return o }
