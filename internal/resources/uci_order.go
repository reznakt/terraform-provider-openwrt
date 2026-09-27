package resources

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/reznakt/terraform-provider-openwrt/internal/rpc"
	"github.com/reznakt/terraform-provider-openwrt/internal/uci"
)

// NewUCIOrder manages the relative order of sections, which matters for
// firewall rules and redirects.
func NewUCIOrder() resource.Resource { return &uciOrderResource{} }

type uciOrderResource struct{ c *rpc.Client }

type uciOrderModel struct {
	ID       types.String `tfsdk:"id"`
	Config   types.String `tfsdk:"config"`
	Sections []string     `tfsdk:"sections"`
}

func (r *uciOrderResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_uci_order"
}

func (r *uciOrderResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.c = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *uciOrderResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	desc := "Keeps the listed sections of a config in the given order (they are moved to the top of the file). " +
		"Use it for firewall rules and redirects, which are evaluated in file order. Destroying it leaves the order as is."
	resp.Schema = schema.Schema{
		Description: desc, MarkdownDescription: desc,
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"config": schema.StringAttribute{
				Required: true, Description: "Config file name, e.g. `firewall`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"sections": schema.ListAttribute{
				ElementType: types.StringType, Required: true,
				Description: "Section names in the desired order, e.g. `[openwrt_firewall_rule.a.section, ...]`.",
				Validators:  []validator.List{listvalidator.SizeAtLeast(1), listvalidator.UniqueValues()},
			},
		},
	}
}

func (r *uciOrderResource) write(ctx context.Context, m *uciOrderModel) error {
	cfg := m.Config.ValueString()
	return r.c.UCI.Transact(ctx, []string{cfg}, func(ctx context.Context, u *uci.Client) error {
		return u.Order(ctx, cfg, m.Sections)
	})
}

func (r *uciOrderResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m uciOrderModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.write(ctx, &m); err != nil {
		errDiag(&resp.Diagnostics, "Ordering "+m.Config.ValueString(), err)
		return
	}
	m.ID = types.StringValue(m.Config.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *uciOrderResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m uciOrderModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	secs, err := r.c.UCI.UCI.List(ctx, m.Config.ValueString(), "")
	if err != nil {
		errDiag(&resp.Diagnostics, "Reading "+m.Config.ValueString(), err)
		return
	}
	// Report the managed sections in their actual relative order; sections
	// that disappeared are dropped so the plan re-adds nothing bogus.
	managed := map[string]bool{}
	for _, s := range m.Sections {
		managed[s] = true
	}
	actual := []string{}
	for _, s := range secs {
		if managed[s.Name] {
			actual = append(actual, s.Name)
		}
	}
	m.Sections = actual
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *uciOrderResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m uciOrderModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.write(ctx, &m); err != nil {
		errDiag(&resp.Diagnostics, "Ordering "+m.Config.ValueString(), err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *uciOrderResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}
