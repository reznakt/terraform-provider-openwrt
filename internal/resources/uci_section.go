package resources

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/reznakt/terraform-provider-openwrt/internal/rpc"
	"github.com/reznakt/terraform-provider-openwrt/internal/secrets"
	"github.com/reznakt/terraform-provider-openwrt/internal/ubus"
	"github.com/reznakt/terraform-provider-openwrt/internal/uci"
)

// NewUCISection is the generic escape hatch: any section of any config.
func NewUCISection() resource.Resource { return &uciSectionResource{} }

type uciSectionResource struct{ c *rpc.Client }

var (
	_ resource.ResourceWithImportState    = (*uciSectionResource)(nil)
	_ resource.ResourceWithValidateConfig = (*uciSectionResource)(nil)
)

type uciSectionModel struct {
	ID                 types.String `tfsdk:"id"`
	Config             types.String `tfsdk:"config"`
	Type               types.String `tfsdk:"type"`
	Section            types.String `tfsdk:"section"`
	Options            types.Map    `tfsdk:"options"`
	Lists              types.Map    `tfsdk:"lists"`
	SensitiveOptions   types.Map    `tfsdk:"sensitive_options"`
	SensitiveWO        types.Map    `tfsdk:"sensitive_options_wo"`
	SensitiveWOVersion types.Int64  `tfsdk:"sensitive_options_wo_version"`
}

func (r *uciSectionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_uci_section"
}

func (r *uciSectionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.c = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *uciSectionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	nameV := []validator.String{stringvalidator.RegexMatches(sectionNameRe, "must contain only letters, digits and underscores")}
	desc := "Manages any UCI section as a whole. The resource is authoritative: options on the router that are not declared here are removed. " +
		"Options whose names look like secrets (`key`, `pass`, `psk`, `secret`, `token`, `auth`) belong in `sensitive_options` or `sensitive_options_wo`."
	resp.Schema = schema.Schema{
		Description: desc, MarkdownDescription: desc,
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true, Description: "`<config>.<section>`.",
			},
			"config": schema.StringAttribute{
				Required: true, Description: "Config file name under /etc/config, e.g. `network`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    nameV,
			},
			"type": schema.StringAttribute{
				Required: true, Description: "Section type, e.g. `interface`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{stringvalidator.RegexMatches(sectionTypeRe, "must be a valid UCI section type")},
			},
			"section": schema.StringAttribute{
				Required: true, Description: "Section name. Changing it renames the section in place.",
				Validators: nameV,
			},
			"options": schema.MapAttribute{
				ElementType: types.StringType, Optional: true,
				Description: "Scalar options (`option name 'value'`).",
			},
			"lists": schema.MapAttribute{
				ElementType: types.ListType{ElemType: types.StringType}, Optional: true,
				Description: "List options (`list name 'value'`).",
			},
			"sensitive_options": schema.MapAttribute{
				ElementType: types.StringType, Optional: true, Sensitive: true,
				Description: "Secret scalar options. Stored in state but hidden in plans and output.",
			},
			"sensitive_options_wo": schema.MapAttribute{
				ElementType: types.StringType, Optional: true, Sensitive: true, WriteOnly: true,
				Description: "Secret scalar options that are never stored in state. Sent when `sensitive_options_wo_version` changes. Requires Terraform/OpenTofu >= 1.11.",
				Validators:  []validator.Map{mapvalidator.AlsoRequires(path.MatchRoot("sensitive_options_wo_version"))},
			},
			"sensitive_options_wo_version": schema.Int64Attribute{
				Optional: true, Description: "Bump to re-send `sensitive_options_wo`. Cleared automatically when the router's values drift.",
				Validators: []validator.Int64{int64validator.AlsoRequires(path.MatchRoot("sensitive_options_wo"))},
			},
		},
	}
}

func (r *uciSectionResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m uciSectionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	seen := map[string]string{}
	for _, part := range []struct {
		attr string
		keys []string
	}{
		{"options", mapKeys(m.Options)}, {"lists", mapKeys(m.Lists)},
		{"sensitive_options", mapKeys(m.SensitiveOptions)}, {"sensitive_options_wo", mapKeys(m.SensitiveWO)},
	} {
		for _, k := range part.keys {
			p := path.Root(part.attr).AtMapKey(k)
			if prev, dup := seen[k]; dup {
				resp.Diagnostics.AddAttributeError(p, "Option declared twice", fmt.Sprintf("%q is also in %s.", k, prev))
			}
			seen[k] = part.attr
			if !optionNameRe.MatchString(k) {
				resp.Diagnostics.AddAttributeError(p, "Invalid option name", "UCI option names contain only letters, digits, `_` and `-`.")
			}
			if (part.attr == "options" || part.attr == "lists") && secrets.LooksSecret(k) {
				resp.Diagnostics.AddAttributeWarning(p, "Possible secret in a non-sensitive attribute",
					fmt.Sprintf("%q looks like a secret; move it to sensitive_options (or sensitive_options_wo) to keep it out of plan output.", k))
			}
		}
	}
}

func mapKeys(m types.Map) []string {
	if m.IsNull() || m.IsUnknown() {
		return nil
	}
	keys := make([]string, 0, len(m.Elements()))
	for k := range m.Elements() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// uciDesired is the full desired content of the section.
type uciDesired struct {
	set    uci.Values
	woKeys map[string]bool   // keys owned by sensitive_options_wo
	wo     map[string]string // write-only values being sent now
}

func (r *uciSectionResource) desired(ctx context.Context, plan, config getter, prior getter) (*uciDesired, diag.Diagnostics) {
	var diags diag.Diagnostics
	var opts, sens, woVals map[string]string
	var lists map[string][]string
	diags.Append(plan.GetAttribute(ctx, path.Root("options"), &opts)...)
	diags.Append(plan.GetAttribute(ctx, path.Root("lists"), &lists)...)
	diags.Append(plan.GetAttribute(ctx, path.Root("sensitive_options"), &sens)...)
	diags.Append(config.GetAttribute(ctx, path.Root("sensitive_options_wo"), &woVals)...)

	d := &uciDesired{set: uci.Values{}, woKeys: map[string]bool{}, wo: map[string]string{}}
	for k, v := range opts {
		d.set[k] = v
	}
	for k, v := range sens {
		d.set[k] = v
	}
	for k, v := range lists {
		if len(v) > 0 {
			d.set[k] = v
		}
	}
	var ver, prev types.Int64
	diags.Append(plan.GetAttribute(ctx, path.Root("sensitive_options_wo_version"), &ver)...)
	if prior != nil {
		diags.Append(prior.GetAttribute(ctx, path.Root("sensitive_options_wo_version"), &prev)...)
	}
	send := !ver.IsNull() && (prior == nil || !prev.Equal(ver))
	for k, v := range woVals {
		d.woKeys[k] = true
		if send {
			d.set[k], d.wo[k] = v, v
		}
	}
	return d, diags
}

func (r *uciSectionResource) apply(ctx context.Context, u *uci.Client, cfg, name string, d *uciDesired) error {
	cur, err := u.Get(ctx, cfg, name)
	if err != nil {
		return err
	}
	// Authoritative: whatever is on the router but not being written (and not
	// owned by the write-only map) goes, including lists that became empty.
	var del []string
	drop := func(k string) {
		if _, keep := d.set[k]; !keep && !d.woKeys[k] {
			del = append(del, k)
		}
	}
	for k := range cur.Options {
		drop(k)
	}
	for k := range cur.Lists {
		drop(k)
	}
	sort.Strings(del)
	if err := u.DeleteOptions(ctx, cfg, name, del); err != nil {
		return err
	}
	return u.Set(ctx, cfg, name, d.set)
}

func (r *uciSectionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m uciSectionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	d, diags := r.desired(ctx, req.Plan, req.Config, nil)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	cfg, typ, name := m.Config.ValueString(), m.Type.ValueString(), m.Section.ValueString()
	wo, _ := loadWO(ctx, nil)
	err := r.c.UCI.Transact(ctx, []string{cfg}, func(ctx context.Context, u *uci.Client) error {
		if _, err := u.Get(ctx, cfg, name); err == nil {
			return fmt.Errorf("section %s.%s already exists; import it with `terraform import <address> %s.%s`", cfg, name, cfg, name)
		} else if !ubus.IsNotFound(err) {
			return err
		}
		return u.Add(ctx, cfg, typ, name, d.set)
	})
	if err != nil {
		errDiag(&resp.Diagnostics, "Creating "+cfg+"."+name, err)
		return
	}
	for k, v := range d.wo {
		wo.Hashes[k] = wo.fingerprint(v)
	}
	resp.State.Raw = req.Plan.Raw.Copy()
	r.refresh(ctx, cfg, name, &resp.State, wo, &resp.Diagnostics)
	resp.Diagnostics.Append(wo.save(ctx, resp.Private)...)
}

func (r *uciSectionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m uciSectionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	wo, diags := loadWO(ctx, req.Private)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !r.refresh(ctx, m.Config.ValueString(), m.Section.ValueString(), &resp.State, wo, &resp.Diagnostics) {
		resp.State.RemoveResource(ctx)
	}
}

func (r *uciSectionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state uciSectionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	d, diags := r.desired(ctx, req.Plan, req.Config, req.State)
	resp.Diagnostics.Append(diags...)
	wo, diags := loadWO(ctx, req.Private)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	cfg, name, oldName := plan.Config.ValueString(), plan.Section.ValueString(), state.Section.ValueString()
	// Keys dropped from sensitive_options_wo are no longer protected from deletion.
	for k := range wo.Hashes {
		if !d.woKeys[k] {
			delete(wo.Hashes, k)
		}
	}
	err := r.c.UCI.Transact(ctx, []string{cfg}, func(ctx context.Context, u *uci.Client) error {
		if oldName != name {
			if err := u.Rename(ctx, cfg, oldName, name); err != nil {
				return err
			}
		}
		return r.apply(ctx, u, cfg, name, d)
	})
	if err != nil {
		errDiag(&resp.Diagnostics, "Updating "+cfg+"."+name, err)
		return
	}
	for k, v := range d.wo {
		wo.Hashes[k] = wo.fingerprint(v)
	}
	resp.State.Raw = req.Plan.Raw.Copy()
	r.refresh(ctx, cfg, name, &resp.State, wo, &resp.Diagnostics)
	resp.Diagnostics.Append(wo.save(ctx, resp.Private)...)
}

func (r *uciSectionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m uciSectionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	cfg, name := m.Config.ValueString(), m.Section.ValueString()
	err := r.c.UCI.Transact(ctx, []string{cfg}, func(ctx context.Context, u *uci.Client) error {
		err := u.DeleteSection(ctx, cfg, name)
		if ubus.IsNotFound(err) {
			return nil
		}
		return err
	})
	if err != nil {
		errDiag(&resp.Diagnostics, "Deleting "+cfg+"."+name, err)
	}
}

func (r *uciSectionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	cfg, ref, ok := strings.Cut(req.ID, ".")
	if !ok || cfg == "" || ref == "" {
		resp.Diagnostics.AddError("Invalid import ID", "Expected `<config>.<section>` or `<config>.@<type>[<index>]`, e.g. `network.lan` or `firewall.@zone[0]`.")
		return
	}
	u := r.c.UCI.UCI
	name, err := u.Resolve(ctx, cfg, ref)
	if err != nil {
		errDiag(&resp.Diagnostics, "Importing "+req.ID, err)
		return
	}
	s, err := u.Get(ctx, cfg, name)
	if err != nil {
		errDiag(&resp.Diagnostics, "Importing "+req.ID, err)
		return
	}
	for a, v := range map[string]string{"id": cfg + "." + name, "config": cfg, "type": s.Type, "section": name} {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(a), v)...)
	}
	if s.Anonymous {
		resp.Diagnostics.AddWarning("Imported an anonymous section",
			fmt.Sprintf("%s is anonymous (%s). Its generated name is not stable: set `section` in your configuration and apply right away so the section gets renamed.", req.ID, name))
	}
}

// refresh reads the section and sorts every option into the right attribute.
func (r *uciSectionResource) refresh(ctx context.Context, cfg, name string, st *tfsdk.State, wo *woState, diags *diag.Diagnostics) bool {
	s, err := r.c.UCI.UCI.Get(ctx, cfg, name)
	if ubus.IsNotFound(err) {
		return false
	}
	if err != nil {
		errDiag(diags, "Reading "+cfg+"."+name, err)
		return true
	}
	var prior uciSectionModel
	diags.Append(st.Get(ctx, &prior)...)
	priorOpts, priorSens := keySet(prior.Options), keySet(prior.SensitiveOptions)

	opts, sens := map[string]string{}, map[string]string{}
	drift := false
	for k, v := range s.Options {
		switch {
		case wo.Hashes[k] != "":
			if wo.fingerprint(v) != wo.Hashes[k] {
				drift = true
			}
		case priorOpts[k]:
			opts[k] = v
		case priorSens[k] || secrets.LooksSecret(k):
			sens[k] = v
		default:
			opts[k] = v
		}
	}
	for k := range wo.Hashes {
		if _, ok := s.Options[k]; !ok {
			drift = true
		}
	}

	set := func(a string, v any) { diags.Append(st.SetAttribute(ctx, path.Root(a), v)...) }
	set("id", cfg+"."+s.Name)
	set("config", cfg)
	set("type", s.Type)
	set("section", s.Name)
	setMap(ctx, st, "options", opts, prior.Options.IsNull(), diags)
	setMap(ctx, st, "sensitive_options", sens, prior.SensitiveOptions.IsNull(), diags)
	if len(s.Lists) == 0 && prior.Lists.IsNull() {
		set("lists", types.MapNull(types.ListType{ElemType: types.StringType}))
	} else {
		set("lists", s.Lists)
	}
	if drift {
		set("sensitive_options_wo_version", types.Int64Null())
	}
	return true
}

func keySet(m types.Map) map[string]bool {
	out := map[string]bool{}
	for _, k := range mapKeys(m) {
		out[k] = true
	}
	return out
}

// setMap keeps an unset attribute null instead of turning it into {}.
func setMap(ctx context.Context, st *tfsdk.State, attr string, m map[string]string, priorNull bool, diags *diag.Diagnostics) {
	if len(m) == 0 && priorNull {
		diags.Append(st.SetAttribute(ctx, path.Root(attr), types.MapNull(types.StringType))...)
		return
	}
	diags.Append(st.SetAttribute(ctx, path.Root(attr), m)...)
}

func (r *uciSectionResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	planID(ctx, "", req, resp)
}
