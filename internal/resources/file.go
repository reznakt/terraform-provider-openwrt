package resources

import (
	"context"
	"crypto/md5" //nolint:gosec // rpcd only offers md5; used for drift detection, not security
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/reznakt/terraform-provider-openwrt/internal/rpc"
	"github.com/reznakt/terraform-provider-openwrt/internal/ubus"
)

// NewFile manages a file on the router.
func NewFile() resource.Resource { return &fileResource{} }

type fileResource struct{ c *rpc.Client }

var _ resource.ResourceWithModifyPlan = (*fileResource)(nil)

type fileModel struct {
	ID            types.String `tfsdk:"id"`
	Path          types.String `tfsdk:"path"`
	Mode          types.String `tfsdk:"mode"`
	Content       types.String `tfsdk:"content"`
	ContentBase64 types.String `tfsdk:"content_base64"`
	Source        types.String `tfsdk:"source"`
	SensitiveSrc  types.String `tfsdk:"sensitive_source"`
	ContentWO     types.String `tfsdk:"content_wo"`
	ContentWOVer  types.Int64  `tfsdk:"content_wo_version"`
	MD5           types.String `tfsdk:"md5"`
}

var contentAttrs = []string{"content", "content_base64", "source", "sensitive_source", "content_wo"}

// secret reports whether the content is kept out of state (content_wo or
// sensitive_source), with only a salted fingerprint in private state.
func (m *fileModel) secret() bool { return !m.ContentWO.IsNull() || !m.SensitiveSrc.IsNull() }

func (r *fileResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_file"
}

func (r *fileResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.c = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *fileResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	exactlyOne := func(self string) []validator.String {
		var others []path.Expression
		for _, a := range contentAttrs {
			if a != self {
				others = append(others, path.MatchRoot(a))
			}
		}
		return []validator.String{stringvalidator.ExactlyOneOf(others...)}
	}
	desc := "Manages a file on the router through rpcd-mod-file. Exactly one of `content`, `content_base64`, `source`, `sensitive_source` or `content_wo` is required. " +
		"Drift is detected by comparing the router's MD5 with the expected one, so file bodies are only kept in state when you use `content`/`content_base64`. " +
		"With `content_wo` or `sensitive_source` not even the MD5 is stored; a salted fingerprint in private state detects drift instead. " +
		"Requires an rpcd ACL granting read/write on the path (see the bootstrap guide)."
	resp.Schema = schema.Schema{
		Description: desc, MarkdownDescription: desc,
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"path": schema.StringAttribute{
				Required: true, Description: "Absolute path on the router.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{stringvalidator.RegexMatches(absPathRe, "must be an absolute path")},
			},
			"mode": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("0644"),
				Description: "Octal permission bits. Default `0644`; use `0600` for secrets.",
				Validators:  []validator.String{stringvalidator.RegexMatches(modeRe, "must be an octal mode like 0644")},
			},
			"content":        schema.StringAttribute{Optional: true, Sensitive: true, Description: "UTF-8 content (stored in state, hidden in plans).", Validators: exactlyOne("content")},
			"content_base64": schema.StringAttribute{Optional: true, Sensitive: true, Description: "Base64 content for binary files (stored in state, hidden in plans).", Validators: exactlyOne("content_base64")},
			"source":         schema.StringAttribute{Optional: true, Description: "Local file to upload. Only its MD5 is kept in state; use `sensitive_source` for secrets.", Validators: exactlyOne("source")},
			"sensitive_source": schema.StringAttribute{
				Optional:    true,
				Description: "Local secret file to upload, e.g. one decrypted by sops-nix or agenix. Only the path is kept in state, not even the MD5; the file is uploaded again whenever it changes.",
				Validators:  exactlyOne("sensitive_source"),
			},
			"content_wo": schema.StringAttribute{
				Optional: true, Sensitive: true, WriteOnly: true,
				Description: "Write-only content, never stored in state or plan. Uploaded again whenever it changes. Requires Terraform/OpenTofu >= 1.11.",
				Validators:  exactlyOne("content_wo"),
			},
			"content_wo_version": schema.Int64Attribute{
				Optional: true, Computed: true,
				Description: "Version of the content sent from `content_wo` or `sensitive_source`. Leave it unset and the provider bumps it whenever the content changes or the file on the router drifts; set it to upload only on explicit bumps.",
				Validators: []validator.Int64{int64validator.Any(
					int64validator.AlsoRequires(path.MatchRoot("content_wo")),
					int64validator.AlsoRequires(path.MatchRoot("sensitive_source")),
				)},
			},
			"md5": schema.StringAttribute{Computed: true, Description: "MD5 of the file on the router (null with `content_wo` or `sensitive_source`, so no fingerprint of a secret ends up in state)."},
		},
	}
}

// desiredBytes returns the content to write from config; wo is true for content_wo.
func desiredBytes(m *fileModel) ([]byte, bool, error) {
	switch {
	case !m.Content.IsNull():
		return []byte(m.Content.ValueString()), false, nil
	case !m.ContentBase64.IsNull():
		b, err := base64.StdEncoding.DecodeString(m.ContentBase64.ValueString())
		return b, false, err
	case !m.Source.IsNull():
		b, err := os.ReadFile(m.Source.ValueString())
		return b, false, err
	case !m.SensitiveSrc.IsNull():
		b, err := os.ReadFile(m.SensitiveSrc.ValueString())
		return b, true, err
	case !m.ContentWO.IsNull():
		return []byte(m.ContentWO.ValueString()), true, nil
	}
	return nil, false, fmt.Errorf("no content given")
}

func md5hex(b []byte) string {
	s := md5.Sum(b) //nolint:gosec
	return hex.EncodeToString(s[:])
}

func parseMode(s string) int64 {
	n, _ := strconv.ParseInt(s, 8, 64)
	return n
}

// ModifyPlan puts the expected MD5 into the plan so content drift on the
// router, or a changed `source` file, shows up as a diff. Secret content
// shows up as a new content_wo_version instead.
func (r *fileResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var cfg, plan fileModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	prior := types.Int64Null()
	if !req.State.Raw.IsNull() {
		resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("content_wo_version"), &prior)...)
	}
	secret := cfg.secret()
	known := !cfg.ContentWO.IsUnknown() && !cfg.SensitiveSrc.IsUnknown()
	changed := false
	if secret && known {
		b, _, err := desiredBytes(&cfg)
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("sensitive_source"), "Cannot read sensitive_source", err.Error())
			return
		}
		wo, diags := loadWO(ctx, req.Private)
		resp.Diagnostics.Append(diags...)
		changed = wo.differs(map[string]string{"md5": md5hex(b)})
	}
	ver := planWOVersion(cfg.ContentWOVer, prior, secret, known, changed, "content_wo_version", &resp.Diagnostics)
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("content_wo_version"), ver)...)

	if cfg.Content.IsUnknown() || cfg.ContentBase64.IsUnknown() || cfg.Source.IsUnknown() {
		return
	}
	if secret {
		plan.MD5 = types.StringNull()
	} else if b, _, err := desiredBytes(&cfg); err == nil {
		plan.MD5 = types.StringValue(md5hex(b))
	} else if !cfg.Source.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("source"), "Cannot read source", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("md5"), plan.MD5)...)
}

func (r *fileResource) write(ctx context.Context, cfg *fileModel, plan *fileModel, send bool) (string, error) {
	path := plan.Path.ValueString()
	if !send {
		return "", r.chmodIfNeeded(ctx, plan)
	}
	b, _, err := desiredBytes(cfg)
	if err != nil {
		return "", err
	}
	if err := r.c.FileWrite(ctx, path, b, parseMode(plan.Mode.ValueString())); err != nil {
		return "", err
	}
	return md5hex(b), nil
}

// chmodIfNeeded rewrites the file only when the mode alone changed; rpcd has
// no chmod, so the current content is read back and written with the new mode.
func (r *fileResource) chmodIfNeeded(ctx context.Context, plan *fileModel) error {
	st, err := r.c.FileStat(ctx, plan.Path.ValueString())
	if err != nil {
		return err
	}
	if st.Mode&0o7777 == parseMode(plan.Mode.ValueString()) {
		return nil
	}
	b, err := r.c.FileRead(ctx, plan.Path.ValueString())
	if err != nil {
		return err
	}
	return r.c.FileWrite(ctx, plan.Path.ValueString(), b, parseMode(plan.Mode.ValueString()))
}

func (r *fileResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var cfg, plan fileModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	sum, err := r.write(ctx, &cfg, &plan, true)
	if err != nil {
		errDiag(&resp.Diagnostics, "Writing "+plan.Path.ValueString(), err)
		return
	}
	wo, _ := loadWO(ctx, nil)
	if cfg.secret() {
		wo.Hashes["md5"] = wo.fingerprint(sum)
		plan.MD5 = types.StringNull()
		if plan.ContentWOVer.IsUnknown() {
			plan.ContentWOVer = types.Int64Value(1)
		}
	} else {
		plan.MD5 = types.StringValue(sum)
	}
	plan.ID = plan.Path
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(wo.save(ctx, resp.Private)...)
}

func (r *fileResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m fileModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	wo, diags := loadWO(ctx, req.Private)
	resp.Diagnostics.Append(diags...)
	st, err := r.c.FileStat(ctx, m.Path.ValueString())
	if ubus.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		errDiag(&resp.Diagnostics, "Reading "+m.Path.ValueString(), err)
		return
	}
	sum, err := r.c.FileMD5(ctx, m.Path.ValueString())
	if err != nil {
		errDiag(&resp.Diagnostics, "Hashing "+m.Path.ValueString(), err)
		return
	}
	m.Mode = types.StringValue(fmt.Sprintf("%04o", st.Mode&0o7777))
	if h, ok := wo.Hashes["md5"]; ok {
		if wo.fingerprint(sum) != h {
			m.ContentWOVer = types.Int64Null()
		}
		m.MD5 = types.StringNull()
	} else {
		m.MD5 = types.StringValue(sum)
		// Keep inline content in sync so the diff is meaningful; a changed
		// md5 alone already forces an update.
		if !m.Content.IsNull() && md5hex([]byte(m.Content.ValueString())) != sum {
			if b, err := r.c.FileRead(ctx, m.Path.ValueString()); err == nil {
				m.Content = types.StringValue(string(b))
			}
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *fileResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var cfg, plan, state fileModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	isWO := cfg.secret()
	wasWO := state.MD5.IsNull()
	send := wasWO || !plan.MD5.Equal(state.MD5)
	if isWO {
		send = !wasWO || plan.ContentWOVer.IsUnknown() || !plan.ContentWOVer.Equal(state.ContentWOVer)
		if plan.ContentWOVer.IsUnknown() {
			// The content was unknown at plan time; settle the version now.
			plan.ContentWOVer = types.Int64Value(state.ContentWOVer.ValueInt64() + 1)
		}
	}
	sum, err := r.write(ctx, &cfg, &plan, send)
	if err != nil {
		errDiag(&resp.Diagnostics, "Writing "+plan.Path.ValueString(), err)
		return
	}
	wo, _ := loadWO(ctx, req.Private)
	switch {
	case isWO && send:
		wo.Hashes["md5"] = wo.fingerprint(sum)
		plan.MD5 = types.StringNull()
	case isWO:
		plan.MD5 = types.StringNull()
	default:
		delete(wo.Hashes, "md5")
		if send {
			plan.MD5 = types.StringValue(sum)
		} else {
			plan.MD5 = state.MD5
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(wo.save(ctx, resp.Private)...)
}

func (r *fileResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m fileModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if err := r.c.FileRemove(ctx, m.Path.ValueString()); err != nil {
		errDiag(&resp.Diagnostics, "Removing "+m.Path.ValueString(), err)
	}
}
