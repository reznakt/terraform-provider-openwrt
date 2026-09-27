package resources

import (
	"context"
	"sync"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/reznakt/terraform-provider-openwrt/internal/rpc"
)

// NewPackage installs a package with opkg/apk.
func NewPackage() resource.Resource { return &packageResource{} }

// pkgMu serializes package operations: opkg/apk hold a global lock.
var pkgMu sync.Mutex

type packageResource struct{ c *rpc.Client }

type packageModel struct {
	ID            types.String `tfsdk:"id"`
	Name          types.String `tfsdk:"name"`
	Version       types.String `tfsdk:"version"`
	UpdateLists   types.Bool   `tfsdk:"update_lists"`
	KeepOnDestroy types.Bool   `tfsdk:"keep_on_destroy"`
}

func (r *packageResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_package"
}

func (r *packageResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.c = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *packageResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	desc := "Installs a package with opkg (OpenWrt <= 24.10) or apk (>= 25.x), detected automatically. " +
		"Uses LuCI's package-manager helper, which the stock `luci-app-package-manager` ACL already allows. " +
		"Note that packages installed this way are lost on sysupgrade unless kept by attended sysupgrade."
	resp.Schema = schema.Schema{
		Description: desc, MarkdownDescription: desc,
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"name": schema.StringAttribute{
				Required: true, Description: "Package name, e.g. `luci-app-sqm`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"version": schema.StringAttribute{Computed: true, Description: "Installed version.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"update_lists": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				Description: "Refresh package lists before installing. Default true.",
			},
			"keep_on_destroy": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "Leave the package installed when the resource is destroyed.",
			},
		},
	}
}

func (r *packageResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m packageModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := m.Name.ValueString()
	pkgMu.Lock()
	defer pkgMu.Unlock()
	installed, err := r.c.Installed(ctx)
	if err != nil {
		errDiag(&resp.Diagnostics, "Listing packages", err)
		return
	}
	if _, ok := installed[name]; !ok {
		if m.UpdateLists.ValueBool() {
			if err := r.c.UpdateLists(ctx); err != nil {
				errDiag(&resp.Diagnostics, "Updating package lists", err)
				return
			}
		}
		if err := r.c.Install(ctx, name); err != nil {
			errDiag(&resp.Diagnostics, "Installing "+name, err)
			return
		}
		if installed, err = r.c.Installed(ctx); err != nil {
			errDiag(&resp.Diagnostics, "Listing packages", err)
			return
		}
	}
	ver, ok := installed[name]
	if !ok {
		resp.Diagnostics.AddError("Package not installed", name+" is still missing after installation; check the package name and feeds.")
		return
	}
	m.ID, m.Version = m.Name, types.StringValue(ver)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *packageResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m packageModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	installed, err := r.c.Installed(ctx)
	if err != nil {
		errDiag(&resp.Diagnostics, "Listing packages", err)
		return
	}
	ver, ok := installed[m.Name.ValueString()]
	if !ok {
		resp.State.RemoveResource(ctx)
		return
	}
	m.Version = types.StringValue(ver)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *packageResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state packageModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	plan.ID, plan.Version = state.ID, state.Version
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *packageResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m packageModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if m.KeepOnDestroy.ValueBool() {
		return
	}
	pkgMu.Lock()
	defer pkgMu.Unlock()
	if err := r.c.Remove(ctx, m.Name.ValueString()); err != nil {
		errDiag(&resp.Diagnostics, "Removing "+m.Name.ValueString(), err)
	}
}
