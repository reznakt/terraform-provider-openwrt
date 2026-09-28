package resources

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
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
	"github.com/reznakt/terraform-provider-openwrt/internal/sections"
	"github.com/reznakt/terraform-provider-openwrt/internal/ubus"
	"github.com/reznakt/terraform-provider-openwrt/internal/uci"
)

// SectionResources returns one resource constructor per typed section spec.
func SectionResources() []func() resource.Resource {
	var out []func() resource.Resource
	for _, spec := range sections.All() {
		spec := spec
		out = append(out, func() resource.Resource { return &sectionResource{spec: spec} })
	}
	return out
}

// sectionResource is the generic engine behind every typed UCI resource.
//
// Semantics: the resource is authoritative for the options declared in its
// spec (unset in HCL = absent on the router) and for the keys it lists in
// extra_options/extra_lists. Any other option on the router is left alone.
type sectionResource struct {
	spec sections.Spec
	c    *rpc.Client
}

var (
	_ resource.ResourceWithImportState    = (*sectionResource)(nil)
	_ resource.ResourceWithValidateConfig = (*sectionResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*sectionResource)(nil)
)

func (r *sectionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.spec.Name
}

func (r *sectionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.c = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *sectionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	s := r.spec
	desc := s.Description + fmt.Sprintf("\n\nManages a `%s` section in `/etc/config/%s`.", s.Type+typeSuffixHint(s), s.Config)
	if s.Singleton {
		desc += " This section normally exists on a stock router: creating the resource adopts it (renaming an anonymous section to `section`), and destroying the resource only removes it from state."
	}
	attrs := map[string]schema.Attribute{
		"id": schema.StringAttribute{
			Computed:    true,
			Description: "`<config>.<section>`.",
		},
		"section": schema.StringAttribute{
			Required:    true,
			Description: "UCI section name. Changing it renames the section in place.",
			Validators:  []validator.String{stringvalidator.RegexMatches(sectionNameRe, "must contain only letters, digits and underscores")},
		},
		"extra_options": schema.MapAttribute{
			ElementType: types.StringType,
			Optional:    true,
			Description: "Additional scalar options not covered by this resource's attributes. Only listed keys are managed.",
		},
		"extra_lists": schema.MapAttribute{
			ElementType: types.ListType{ElemType: types.StringType},
			Optional:    true,
			Description: "Additional list options not covered by this resource's attributes. Only listed keys are managed.",
		},
	}
	if s.TypeFrom != "" {
		attrs[s.TypeFrom] = schema.StringAttribute{
			Required:      true,
			Description:   s.TypeFromDesc,
			PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			Validators:    []validator.String{stringvalidator.RegexMatches(sectionNameRe, "must be a valid UCI name")},
		}
	}
	for _, o := range s.Options {
		a := o.AttrName()
		d := o.Description + fmt.Sprintf(" (UCI option `%s`.)", o.UCI)
		switch o.Kind {
		case sections.String:
			var v []validator.String
			if len(o.Enum) > 0 {
				v = append(v, stringvalidator.OneOf(o.Enum...))
				d += " One of: `" + strings.Join(o.Enum, "`, `") + "`."
			}
			if o.Sensitive {
				v = append(v, stringvalidator.ConflictsWith(path.MatchRoot(a+"_wo"), path.MatchRoot(a+"_file")))
				attrs[a+"_wo"] = schema.StringAttribute{
					Optional: true, Sensitive: true, WriteOnly: true,
					Description: "Write-only variant of `" + a + "`: never stored in state or plan. Sent again whenever it changes. Requires Terraform/OpenTofu >= 1.11.",
					Validators:  []validator.String{stringvalidator.ConflictsWith(path.MatchRoot(a + "_file"))},
				}
				attrs[a+"_file"] = schema.StringAttribute{
					Optional:    true,
					Description: "Path to a local file holding `" + a + "` (trailing newlines ignored), e.g. a secret decrypted by sops-nix or agenix. Only the path is stored in state; the value is handled like `" + a + "_wo` and sent again whenever the file changes.",
				}
				attrs[a+"_wo_version"] = schema.Int64Attribute{
					Optional: true, Computed: true,
					Description: "Version of the value sent from `" + a + "_wo` or `" + a + "_file`. Leave it unset and the provider bumps it whenever the value changes or the router's value drifts; set it to send only on explicit bumps.",
				}
			}
			attrs[a] = schema.StringAttribute{Required: o.Required, Optional: !o.Required, Sensitive: o.Sensitive, Description: d, Validators: v}
		case sections.Int:
			attrs[a] = schema.Int64Attribute{Required: o.Required, Optional: !o.Required, Description: d}
		case sections.Bool:
			attrs[a] = schema.BoolAttribute{Required: o.Required, Optional: !o.Required, Description: d}
		case sections.List:
			attrs[a] = schema.ListAttribute{
				ElementType: types.StringType, Required: o.Required, Optional: !o.Required, Description: d,
				Validators: []validator.List{listvalidator.SizeAtLeast(1)},
			}
		}
	}
	resp.Schema = schema.Schema{Description: desc, MarkdownDescription: desc, Attributes: attrs}
}

func typeSuffixHint(s sections.Spec) string {
	if s.TypeFrom != "" {
		return "<" + s.TypeFrom + ">"
	}
	return ""
}

func (r *sectionResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	known := map[string]bool{}
	for _, o := range r.spec.Options {
		known[o.UCI] = true
	}
	var opts map[string]types.String
	var lists map[string]types.List
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("extra_options"), &opts)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("extra_lists"), &lists)...)
	check := func(attr, k string) {
		if known[k] {
			resp.Diagnostics.AddAttributeError(path.Root(attr).AtMapKey(k), "Option has a dedicated attribute",
				fmt.Sprintf("%q is managed by this resource's own attribute; set it there instead.", k))
		}
		if secrets.LooksSecret(k) {
			resp.Diagnostics.AddAttributeWarning(path.Root(attr).AtMapKey(k), "Possible secret in a non-sensitive attribute",
				fmt.Sprintf("%q looks like a secret. Values in %s are stored in state and shown in plans; use openwrt_uci_section's sensitive_options instead.", k, attr))
		}
	}
	for k := range opts {
		check("extra_options", k)
		if _, dup := lists[k]; dup {
			resp.Diagnostics.AddAttributeError(path.Root("extra_lists").AtMapKey(k), "Duplicate option", k+" is also in extra_options.")
		}
	}
	for k := range lists {
		check("extra_lists", k)
	}
	for _, o := range r.spec.Options {
		if !o.Sensitive {
			continue
		}
		a := o.AttrName()
		var ver types.Int64
		var wo, file types.String
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root(a+"_wo_version"), &ver)...)
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root(a+"_wo"), &wo)...)
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root(a+"_file"), &file)...)
		if !ver.IsNull() && wo.IsNull() && file.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root(a+"_wo_version"), "Missing write-only value",
				a+"_wo_version needs "+a+"_wo or "+a+"_file.")
		}
	}
}

// woValue returns the write-only value of o from the configuration, taken
// from <attr>_wo or read from <attr>_file.
func woValue(ctx context.Context, config getter, o sections.Option) (value string, set, known bool, diags diag.Diagnostics) {
	a := o.AttrName()
	var wo, file types.String
	diags.Append(config.GetAttribute(ctx, path.Root(a+"_wo"), &wo)...)
	diags.Append(config.GetAttribute(ctx, path.Root(a+"_file"), &file)...)
	switch {
	case wo.IsUnknown() || file.IsUnknown():
		return "", true, false, diags
	case !wo.IsNull():
		return wo.ValueString(), true, true, diags
	case !file.IsNull():
		v, err := readSecretFile(file.ValueString())
		if err != nil {
			diags.AddAttributeError(path.Root(a+"_file"), "Cannot read secret file", err.Error())
			return "", true, false, diags
		}
		return v, true, true, diags
	}
	return "", false, true, diags
}

func (r *sectionResource) configName() string { return r.spec.Config }

func (r *sectionResource) sectionType(ctx context.Context, g getter, diags *diag.Diagnostics) string {
	if r.spec.TypeFrom == "" {
		return r.spec.Type
	}
	var v types.String
	diags.Append(g.GetAttribute(ctx, path.Root(r.spec.TypeFrom), &v)...)
	return r.spec.Type + v.ValueString()
}

// desired computes what to write. prior is nil on Create.
type desired struct {
	set   uci.Values
	unset []string
	wo    map[string]string // uci option -> write-only value being sent
	// versions settles *_wo_version attributes that were unknown in the plan.
	versions map[string]int64
}

func (r *sectionResource) desired(ctx context.Context, plan getter, config getter, prior getter) (*desired, diag.Diagnostics) {
	var diags diag.Diagnostics
	d := &desired{set: uci.Values{}, wo: map[string]string{}, versions: map[string]int64{}}
	for _, o := range r.spec.Options {
		a := o.AttrName()
		if o.Sensitive {
			var ver types.Int64
			diags.Append(plan.GetAttribute(ctx, path.Root(a+"_wo_version"), &ver)...)
			if !ver.IsNull() {
				var prev types.Int64
				if prior != nil {
					diags.Append(prior.GetAttribute(ctx, path.Root(a+"_wo_version"), &prev)...)
				}
				if ver.IsUnknown() {
					// The value was unknown at plan time: send it and settle the version now.
					d.versions[a+"_wo_version"] = prev.ValueInt64() + 1
				}
				if prior == nil || ver.IsUnknown() || !prev.Equal(ver) {
					v, set, known, dg := woValue(ctx, config, o)
					diags.Append(dg...)
					if !set || !known {
						if !dg.HasError() {
							diags.AddAttributeError(path.Root(a+"_wo"), "Missing write-only value", a+"_wo or "+a+"_file must be set when "+a+"_wo_version is set.")
						}
						continue
					}
					d.set[o.UCI] = v
					d.wo[o.UCI] = v
				}
				continue
			}
		}
		v, null, dg := readAttr(ctx, plan, a, o.Kind)
		diags.Append(dg...)
		if null {
			d.unset = append(d.unset, o.UCI)
		} else {
			d.set[o.UCI] = v
		}
	}

	var opts, prevOpts map[string]string
	var lists, prevLists map[string][]string
	diags.Append(plan.GetAttribute(ctx, path.Root("extra_options"), &opts)...)
	diags.Append(plan.GetAttribute(ctx, path.Root("extra_lists"), &lists)...)
	for k, v := range opts {
		d.set[k] = v
	}
	for k, v := range lists {
		if len(v) == 0 {
			d.unset = append(d.unset, k)
		} else {
			d.set[k] = v
		}
	}
	if prior != nil {
		diags.Append(prior.GetAttribute(ctx, path.Root("extra_options"), &prevOpts)...)
		diags.Append(prior.GetAttribute(ctx, path.Root("extra_lists"), &prevLists)...)
		for k := range prevOpts {
			if _, ok := d.set[k]; !ok {
				d.unset = append(d.unset, k)
			}
		}
		for k := range prevLists {
			if _, ok := d.set[k]; !ok {
				d.unset = append(d.unset, k)
			}
		}
	}
	return d, diags
}

func setVersions(ctx context.Context, d *desired, st *tfsdk.State, diags *diag.Diagnostics) {
	for a, v := range d.versions {
		diags.Append(st.SetAttribute(ctx, path.Root(a), v)...)
	}
}

// readAttr reads attribute a of the given kind and encodes it for UCI.
func readAttr(ctx context.Context, g getter, a string, kind sections.Kind) (any, bool, diag.Diagnostics) {
	p := path.Root(a)
	switch kind {
	case sections.Int:
		var v types.Int64
		d := g.GetAttribute(ctx, p, &v)
		return strconv.FormatInt(v.ValueInt64(), 10), v.IsNull(), d
	case sections.Bool:
		var v types.Bool
		d := g.GetAttribute(ctx, p, &v)
		if v.ValueBool() {
			return "1", v.IsNull(), d
		}
		return "0", v.IsNull(), d
	case sections.List:
		var v []string
		d := g.GetAttribute(ctx, p, &v)
		return v, v == nil, d
	default:
		var v types.String
		d := g.GetAttribute(ctx, p, &v)
		return v.ValueString(), v.IsNull(), d
	}
}

// stage writes d into the section (which exists by now).
func stage(ctx context.Context, u *uci.Client, config, section string, cur *uci.Section, d *desired) error {
	var del []string
	for _, k := range d.unset {
		if cur == nil || cur.Has(k) {
			del = append(del, k)
		}
	}
	sort.Strings(del)
	if err := u.DeleteOptions(ctx, config, section, del); err != nil {
		return err
	}
	return u.Set(ctx, config, section, d.set)
}

func (r *sectionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var name string
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("section"), &name)...)
	typ := r.sectionType(ctx, req.Plan, &resp.Diagnostics)
	d, diags := r.desired(ctx, req.Plan, req.Config, nil)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	cfg := r.configName()

	err := r.c.UCI.Transact(ctx, []string{cfg}, func(ctx context.Context, u *uci.Client) error {
		cur, err := u.Get(ctx, cfg, name)
		switch {
		case err == nil && cur.Type != typ:
			return fmt.Errorf("section %s.%s exists with type %q, not %q", cfg, name, cur.Type, typ)
		case err == nil && !r.spec.Singleton:
			return fmt.Errorf("section %s.%s already exists; import it with `terraform import <address> %s`", cfg, name, name)
		case err == nil:
			return stage(ctx, u, cfg, name, cur, d)
		case !ubus.IsNotFound(err):
			return err
		}
		if r.spec.Singleton {
			existing, err := u.List(ctx, cfg, typ)
			if err != nil && !ubus.IsNotFound(err) {
				return err
			}
			if len(existing) > 0 {
				if !existing[0].Anonymous {
					return fmt.Errorf("a %s section already exists as %s.%s; set section = %q to adopt it", typ, cfg, existing[0].Name, existing[0].Name)
				}
				if err := u.Rename(ctx, cfg, existing[0].Name, name); err != nil {
					return err
				}
				return stage(ctx, u, cfg, name, existing[0], d)
			}
		}
		return u.Add(ctx, cfg, typ, name, d.set)
	})
	if err != nil {
		errDiag(&resp.Diagnostics, "Creating "+cfg+"."+name, err)
		return
	}

	resp.State.Raw = req.Plan.Raw.Copy()
	setVersions(ctx, d, &resp.State, &resp.Diagnostics)
	wo, diags := loadWO(ctx, nil)
	resp.Diagnostics.Append(diags...)
	for k, v := range d.wo {
		wo.Hashes[k] = wo.fingerprint(v)
	}
	r.refresh(ctx, name, &resp.State, wo, &resp.Diagnostics)
	resp.Diagnostics.Append(wo.save(ctx, resp.Private)...)
}

func (r *sectionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var name string
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("section"), &name)...)
	wo, diags := loadWO(ctx, req.Private)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !r.refresh(ctx, name, &resp.State, wo, &resp.Diagnostics) {
		resp.State.RemoveResource(ctx)
	}
}

func (r *sectionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var oldName, name string
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("section"), &oldName)...)
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("section"), &name)...)
	d, diags := r.desired(ctx, req.Plan, req.Config, req.State)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	cfg := r.configName()
	err := r.c.UCI.Transact(ctx, []string{cfg}, func(ctx context.Context, u *uci.Client) error {
		if oldName != name {
			if err := u.Rename(ctx, cfg, oldName, name); err != nil {
				return fmt.Errorf("rename %s.%s to %s: %w", cfg, oldName, name, err)
			}
		}
		cur, err := u.Get(ctx, cfg, name)
		if err != nil {
			return err
		}
		return stage(ctx, u, cfg, name, cur, d)
	})
	if err != nil {
		errDiag(&resp.Diagnostics, "Updating "+cfg+"."+name, err)
		return
	}
	wo, diags := loadWO(ctx, req.Private)
	resp.Diagnostics.Append(diags...)
	for k, v := range d.wo {
		wo.Hashes[k] = wo.fingerprint(v)
	}
	// Options no longer managed write-only lose their fingerprint.
	for _, o := range r.spec.Options {
		var ver types.Int64
		if o.Sensitive {
			resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root(o.AttrName()+"_wo_version"), &ver)...)
			if ver.IsNull() {
				delete(wo.Hashes, o.UCI)
			}
		}
	}
	resp.State.Raw = req.Plan.Raw.Copy()
	setVersions(ctx, d, &resp.State, &resp.Diagnostics)
	r.refresh(ctx, name, &resp.State, wo, &resp.Diagnostics)
	resp.Diagnostics.Append(wo.save(ctx, resp.Private)...)
}

func (r *sectionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var name string
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("section"), &name)...)
	cfg := r.configName()
	if r.spec.Singleton {
		resp.Diagnostics.AddWarning("Section left on the router",
			fmt.Sprintf("%s.%s is a built-in section; it was removed from state but not deleted.", cfg, name))
		return
	}
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

func (r *sectionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	cfg := r.configName()
	ref := strings.TrimPrefix(req.ID, cfg+".")
	u := r.c.UCI.UCI
	name, err := u.Resolve(ctx, cfg, ref)
	if err != nil {
		errDiag(&resp.Diagnostics, "Importing "+cfg+"."+ref, err)
		return
	}
	s, err := u.Get(ctx, cfg, name)
	if err != nil {
		errDiag(&resp.Diagnostics, "Importing "+cfg+"."+ref, err)
		return
	}
	if !strings.HasPrefix(s.Type, r.spec.Type) || (r.spec.TypeFrom == "" && s.Type != r.spec.Type) {
		resp.Diagnostics.AddError("Wrong section type", fmt.Sprintf("%s.%s is a %q section, this resource manages %q", cfg, name, s.Type, r.spec.Type+typeSuffixHint(r.spec)))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("section"), name)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), cfg+"."+name)...)
	if r.spec.TypeFrom != "" {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(r.spec.TypeFrom), strings.TrimPrefix(s.Type, r.spec.Type))...)
	}
	if s.Anonymous {
		resp.Diagnostics.AddWarning("Imported an anonymous section",
			fmt.Sprintf("%s is anonymous (%s). Its generated name is not stable: set `section` in your configuration and apply right away so the section gets renamed.", ref, name))
	}
}

// refresh reads the section into state. It returns false when the section
// no longer exists.
func (r *sectionResource) refresh(ctx context.Context, name string, st *tfsdk.State, wo *woState, diags *diag.Diagnostics) bool {
	cfg := r.configName()
	s, err := r.c.UCI.UCI.Get(ctx, cfg, name)
	if ubus.IsNotFound(err) {
		return false
	}
	if err != nil {
		errDiag(diags, "Reading "+cfg+"."+name, err)
		return true
	}
	set := func(a string, v any) { diags.Append(st.SetAttribute(ctx, path.Root(a), v)...) }
	set("id", cfg+"."+s.Name)
	set("section", s.Name)
	if r.spec.TypeFrom != "" {
		set(r.spec.TypeFrom, strings.TrimPrefix(s.Type, r.spec.Type))
	}

	for _, o := range r.spec.Options {
		a := o.AttrName()
		if o.Sensitive {
			if h, ok := wo.Hashes[o.UCI]; ok {
				// Managed write-only: keep the value out of state; if the router's
				// value no longer matches what we sent, clear the version so the
				// next plan re-sends it.
				v, present := s.Options[o.UCI]
				if !present || wo.fingerprint(v) != h {
					set(a+"_wo_version", types.Int64Null())
				}
				set(a, types.StringNull())
				continue
			}
		}
		val, err := decode(s, o)
		if err != nil {
			diags.AddAttributeError(path.Root(a), "Unexpected value on the router", err.Error())
			continue
		}
		set(a, val)
	}

	var opts map[string]string
	var lists map[string][]string
	diags.Append(st.GetAttribute(ctx, path.Root("extra_options"), &opts)...)
	diags.Append(st.GetAttribute(ctx, path.Root("extra_lists"), &lists)...)
	if opts != nil {
		next := map[string]string{}
		for k := range opts {
			if v, ok := s.Options[k]; ok {
				next[k] = v
			}
		}
		set("extra_options", next)
	}
	if lists != nil {
		next := map[string][]string{}
		for k := range lists {
			if v, ok := s.Lists[k]; ok {
				next[k] = v
			}
		}
		set("extra_lists", next)
	}
	return true
}

// decode converts a router value into the attribute's framework value.
func decode(s *uci.Section, o sections.Option) (attr.Value, error) {
	raw, isScalar := s.Options[o.UCI]
	items, isList := s.Lists[o.UCI]
	if isList && !isScalar {
		raw = strings.Join(items, " ")
	}
	present := isScalar || isList
	switch o.Kind {
	case sections.Int:
		if !present {
			return types.Int64Null(), nil
		}
		n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("option %s is %q, not an integer; manage it with openwrt_uci_section instead", o.UCI, raw)
		}
		return types.Int64Value(n), nil
	case sections.Bool:
		if !present {
			return types.BoolNull(), nil
		}
		b, ok := parseBool(raw)
		if !ok {
			return nil, fmt.Errorf("option %s is %q, not a boolean", o.UCI, raw)
		}
		return types.BoolValue(b), nil
	case sections.List:
		if !present {
			return types.ListNull(types.StringType), nil
		}
		if !isList {
			// Legacy space-separated option, e.g. `option network 'lan wan'`.
			items = strings.Fields(raw)
		}
		vals := make([]attr.Value, len(items))
		for i, it := range items {
			vals[i] = types.StringValue(it)
		}
		return types.ListValueMust(types.StringType, vals), nil
	default:
		if !present {
			return types.StringNull(), nil
		}
		return types.StringValue(raw), nil
	}
}

func (r *sectionResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	planID(ctx, r.spec.Config, req, resp)
	if req.Plan.Raw.IsNull() {
		return
	}
	wo, diags := loadWO(ctx, req.Private)
	resp.Diagnostics.Append(diags...)
	for _, o := range r.spec.Options {
		if !o.Sensitive {
			continue
		}
		a := o.AttrName() + "_wo_version"
		configured, prior := types.Int64Null(), types.Int64Null()
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root(a), &configured)...)
		if !req.State.Raw.IsNull() {
			resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root(a), &prior)...)
		}
		v, set, known, dg := woValue(ctx, req.Config, o)
		resp.Diagnostics.Append(dg...)
		changed := set && known && wo.differs(map[string]string{o.UCI: v})
		ver := planWOVersion(configured, prior, set, known, changed, a, &resp.Diagnostics)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root(a), ver)...)
	}
}
