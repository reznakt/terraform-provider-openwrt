// Package resources implements the provider's resources and data sources.
package resources

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/reznakt/terraform-provider-openwrt/internal/rpc"
	"github.com/reznakt/terraform-provider-openwrt/internal/uci"
)

// getter is satisfied by tfsdk.Plan, tfsdk.State and tfsdk.Config.
type getter interface {
	GetAttribute(ctx context.Context, p path.Path, target any) diag.Diagnostics
}

// privateGetter/privateSetter are satisfied by the framework's private state.
type privateGetter interface {
	GetKey(ctx context.Context, key string) ([]byte, diag.Diagnostics)
}

type privateSetter interface {
	SetKey(ctx context.Context, key string, value []byte) diag.Diagnostics
}

// clientFrom extracts the provider's client in Configure.
func clientFrom(data any, diags *diag.Diagnostics) *rpc.Client {
	if data == nil {
		return nil // provider not configured yet (validation phase)
	}
	c, ok := data.(*rpc.Client)
	if !ok {
		diags.AddError("Unexpected provider data", fmt.Sprintf("expected *rpc.Client, got %T", data))
		return nil
	}
	return c
}

// errDiag turns an error into a diagnostic. Errors from this provider never
// carry option values, only config/section/option names.
func errDiag(diags *diag.Diagnostics, summary string, err error) {
	detail := err.Error()
	if errors.Is(err, uci.ErrRolledBack) {
		summary += ": change rolled back"
	}
	diags.AddError(summary, detail)
}

// woKey is the private-state key holding write-only secret fingerprints.
const woKey = "wo"

// woState stores salted SHA-256 fingerprints of write-only values as they
// were sent to the router, so drift is detected without storing secrets.
type woState struct {
	Salt   string            `json:"salt"`
	Hashes map[string]string `json:"h"`
}

func loadWO(ctx context.Context, p privateGetter) (*woState, diag.Diagnostics) {
	st := &woState{Hashes: map[string]string{}}
	if p == nil {
		return st, nil
	}
	raw, diags := p.GetKey(ctx, woKey)
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, st); err != nil {
			diags.AddWarning("Ignoring corrupt private state", err.Error())
		}
	}
	if st.Hashes == nil {
		st.Hashes = map[string]string{}
	}
	return st, diags
}

func (w *woState) save(ctx context.Context, p privateSetter) diag.Diagnostics {
	raw, _ := json.Marshal(w)
	return p.SetKey(ctx, woKey, raw)
}

func (w *woState) fingerprint(value string) string {
	if w.Salt == "" {
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		w.Salt = base64.StdEncoding.EncodeToString(b)
	}
	h := sha256.Sum256([]byte(w.Salt + "\x00" + value))
	return hex.EncodeToString(h[:])
}

// differs reports whether any value is not what was last sent under its key.
func (w *woState) differs(values map[string]string) bool {
	for k, v := range values {
		if h, ok := w.Hashes[k]; !ok || w.fingerprint(v) != h {
			return true
		}
	}
	return false
}

// planWOVersion decides the planned *_wo_version. An explicit version is kept
// as is, with a warning when the value changed but the version did not. A
// version left out of the configuration is computed: the prior one while the
// value still matches what was last sent, the next one otherwise, so a
// rotated secret is sent without bumping anything by hand. set is false when
// no write-only value is configured, known is false while it is unknown.
func planWOVersion(configured, prior types.Int64, set, known, changed bool, attr string, diags *diag.Diagnostics) types.Int64 {
	fresh := prior.IsNull() || prior.IsUnknown()
	if !configured.IsNull() {
		if set && known && changed && !fresh && configured.Equal(prior) {
			diags.AddAttributeWarning(path.Root(attr), "Write-only value changed but its version did not",
				"The new value will not be sent. Bump "+attr+", or remove it and let the provider track changes.")
		}
		return configured
	}
	switch {
	case !set:
		return types.Int64Null()
	case !known:
		return types.Int64Unknown()
	case fresh || changed:
		return types.Int64Value(prior.ValueInt64() + 1)
	}
	return prior
}

// readSecretFile reads a secret from a local file, e.g. one decrypted by
// sops-nix or agenix. Trailing newlines are dropped, as tools like
// `wg genkey` add one.
func readSecretFile(name string) (string, error) {
	b, err := os.ReadFile(name)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\r\n"), nil
}

// parseBool accepts every spelling UCI consumers accept.
func parseBool(s string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on", "enabled":
		return true, true
	case "0", "false", "no", "off", "disabled":
		return false, true
	}
	return false, false
}

var (
	sectionNameRe = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
	sectionTypeRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	optionNameRe  = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

var (
	absPathRe = regexp.MustCompile(`^/[^\x00]*$`)
	modeRe    = regexp.MustCompile(`^0?[0-7]{3,4}$`)
)

func pathRoot(name string) path.Path { return path.Root(name) }

// planID sets id = "<config>.<section>" in the plan, so renames show the new
// id up front instead of an inconsistent result after apply.
func planID(ctx context.Context, config string, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var section types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("section"), &section)...)
	if config == "" {
		var c types.String
		resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("config"), &c)...)
		if c.IsUnknown() {
			return
		}
		config = c.ValueString()
	}
	if section.IsUnknown() || section.IsNull() {
		return
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("id"), config+"."+section.ValueString())...)
}
