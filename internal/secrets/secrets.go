// Package secrets decides which UCI option names hold secrets, so the
// provider can keep them out of plain attributes, plan output and logs.
package secrets

import (
	"regexp"
	"strings"
)

// Pattern matches option names that probably hold a secret.
var Pattern = regexp.MustCompile(`(?i)(key|pass|psk|secret|token|auth)`)

// harmless lists option names that match Pattern but are not secrets.
var harmless = map[string]bool{
	"public_key":            true,
	"key_type":              true,
	"keylength":             true,
	"auth_server":           true,
	"auth_port":             true,
	"acct_server":           true,
	"auth_cache":            true,
	"rsn_preauth":           true,
	"ft_psk_generate_local": true,
	"sae_pwe":               true,
	"keepalive":             true,
	"PasswordAuth":          true,
	"RootPasswordAuth":      true,
	"authoritative":         true,
	"MaxAuthTries":          true,
}

// LooksSecret reports whether option name should be treated as secret.
func LooksSecret(name string) bool {
	if harmless[name] || strings.HasSuffix(name, "_file") || strings.HasSuffix(name, "_path") {
		return false
	}
	return Pattern.MatchString(name)
}

// IsHarmless reports whether name is explicitly allow-listed.
func IsHarmless(name string) bool { return harmless[name] }

// LogFieldKeys are structured log fields that must always be masked.
var LogFieldKeys = []string{"password", "ubus_rpc_session", "data", "key", "sae_password", "private_key", "preshared_key", "content"}
