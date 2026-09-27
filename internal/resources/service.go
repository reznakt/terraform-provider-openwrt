package resources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/reznakt/terraform-provider-openwrt/internal/rpc"
	"github.com/reznakt/terraform-provider-openwrt/internal/ubus"
)

// NewService manages an init script's enabled/running state via rc.
func NewService() resource.Resource { return &serviceResource{} }

type serviceResource struct{ c *rpc.Client }

type serviceModel struct {
	ID       types.String `tfsdk:"id"`
	Name     types.String `tfsdk:"name"`
	Enabled  types.Bool   `tfsdk:"enabled"`
	Running  types.Bool   `tfsdk:"running"`
	Triggers types.Map    `tfsdk:"restart_triggers"`
}

func (r *serviceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service"
}

func (r *serviceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.c = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *serviceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	desc := "Controls an init script in /etc/init.d (enable at boot, keep running, restart on change). Destroying the resource leaves the service as it is."
	resp.Schema = schema.Schema{
		Description: desc, MarkdownDescription: desc,
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"name": schema.StringAttribute{
				Required: true, Description: "Init script name, e.g. `dnsmasq`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"enabled":          schema.BoolAttribute{Optional: true, Description: "Start at boot. Unset leaves it alone."},
			"running":          schema.BoolAttribute{Optional: true, Description: "Keep the service running (true) or stopped (false). Unset leaves it alone."},
			"restart_triggers": schema.MapAttribute{ElementType: types.StringType, Optional: true, Description: "Arbitrary values; any change restarts the service."},
		},
	}
}

func (r *serviceResource) converge(ctx context.Context, m *serviceModel, restart bool) error {
	if err := r.act(ctx, m, restart); err != nil {
		return err
	}
	// Init scripts can exit 0 without starting anything (cron without
	// crontabs, a daemon that crashes on startup): check the outcome.
	st, err := r.c.Service(ctx, m.Name.ValueString())
	if err != nil {
		return err
	}
	if !m.Enabled.IsNull() && st.Enabled != m.Enabled.ValueBool() {
		return fmt.Errorf("service %s: enabled is %t after the change", m.Name.ValueString(), st.Enabled)
	}
	if !m.Running.IsNull() && st.Running != m.Running.ValueBool() {
		return fmt.Errorf("service %s: running is %t after the change; check `logread` on the router", m.Name.ValueString(), st.Running)
	}
	return nil
}

func (r *serviceResource) act(ctx context.Context, m *serviceModel, restart bool) error {
	name := m.Name.ValueString()
	st, err := r.c.Service(ctx, name)
	if err != nil {
		return err
	}
	if !m.Enabled.IsNull() && m.Enabled.ValueBool() != st.Enabled {
		action := "disable"
		if m.Enabled.ValueBool() {
			action = "enable"
		}
		if err := r.c.ServiceAction(ctx, name, action); err != nil {
			return err
		}
	}
	switch {
	case !m.Running.IsNull() && !m.Running.ValueBool():
		if st.Running {
			return r.c.ServiceAction(ctx, name, "stop")
		}
	case restart:
		return r.c.ServiceAction(ctx, name, "restart")
	case !m.Running.IsNull() && !st.Running:
		return r.c.ServiceAction(ctx, name, "start")
	}
	return nil
}

func (r *serviceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m serviceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.converge(ctx, &m, false); err != nil {
		errDiag(&resp.Diagnostics, "Configuring service "+m.Name.ValueString(), err)
		return
	}
	m.ID = m.Name
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *serviceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m serviceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	st, err := r.c.Service(ctx, m.Name.ValueString())
	if ubus.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		errDiag(&resp.Diagnostics, "Reading service "+m.Name.ValueString(), err)
		return
	}
	if !m.Enabled.IsNull() {
		m.Enabled = types.BoolValue(st.Enabled)
	}
	if !m.Running.IsNull() {
		m.Running = types.BoolValue(st.Running)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *serviceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state serviceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.converge(ctx, &plan, !plan.Triggers.Equal(state.Triggers)); err != nil {
		errDiag(&resp.Diagnostics, "Configuring service "+plan.Name.ValueString(), err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *serviceResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {}
