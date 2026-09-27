package resources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/reznakt/terraform-provider-openwrt/internal/rpc"
)

// NewExec runs shell commands on the router, like null_resource + remote-exec.
func NewExec() resource.Resource { return &execResource{} }

type execResource struct{ c *rpc.Client }

type execModel struct {
	ID              types.String `tfsdk:"id"`
	Command         types.String `tfsdk:"command"`
	DestroyCommand  types.String `tfsdk:"destroy_command"`
	Environment     types.Map    `tfsdk:"environment"`
	Triggers        types.Map    `tfsdk:"triggers"`
	SensitiveOutput types.Bool   `tfsdk:"sensitive_output"`
	ExitCode        types.Int64  `tfsdk:"exit_code"`
	Stdout          types.String `tfsdk:"stdout"`
	Stderr          types.String `tfsdk:"stderr"`
}

func (r *execResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_exec"
}

func (r *execResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.c = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *execResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	desc := "Runs a command with `/bin/sh -c` on the router when created (and when `command` or `triggers` change), and optionally another on destroy. " +
		"Requires an rpcd ACL granting `exec` on `/bin/sh` (see the bootstrap guide)."
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: desc, MarkdownDescription: desc,
		Attributes: map[string]schema.Attribute{
			"id":              schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"command":         schema.StringAttribute{Required: true, Description: "Shell command to run on create.", PlanModifiers: replace},
			"destroy_command": schema.StringAttribute{Optional: true, Description: "Shell command to run on destroy."},
			"environment": schema.MapAttribute{
				ElementType: types.StringType, Optional: true, Sensitive: true,
				Description:   "Environment variables. Sensitive, so they may carry secrets without showing up in plans.",
				PlanModifiers: []planmodifier.Map{mapplanmodifier.RequiresReplace()},
			},
			"triggers": schema.MapAttribute{
				ElementType: types.StringType, Optional: true,
				Description:   "Arbitrary values; any change re-runs the command.",
				PlanModifiers: []planmodifier.Map{mapplanmodifier.RequiresReplace()},
			},
			"sensitive_output": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "Do not store stdout/stderr in state (use when the command prints secrets).",
			},
			"exit_code": schema.Int64Attribute{Computed: true, Description: "Exit code of the create command."},
			"stdout":    schema.StringAttribute{Computed: true, Description: "Standard output of the create command (null with sensitive_output)."},
			"stderr":    schema.StringAttribute{Computed: true, Description: "Standard error of the create command (null with sensitive_output)."},
		},
	}
}

func (r *execResource) run(ctx context.Context, cmd string, env types.Map) (*rpc.ExecResult, error) {
	var envMap map[string]string
	if !env.IsNull() {
		envMap = map[string]string{}
		for k, v := range env.Elements() {
			if s, ok := v.(types.String); ok {
				envMap[k] = s.ValueString()
			}
		}
	}
	res, err := r.c.Exec(ctx, "/bin/sh", []string{"-c", cmd}, envMap)
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (r *execResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m execModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	res, err := r.run(ctx, m.Command.ValueString(), m.Environment)
	if err != nil {
		errDiag(&resp.Diagnostics, "Running command", err)
		return
	}
	if res.Code != 0 {
		detail := fmt.Sprintf("exit code %d", res.Code)
		if !m.SensitiveOutput.ValueBool() {
			detail += "\n\nstderr:\n" + res.Stderr
		}
		resp.Diagnostics.AddError("Command failed", detail)
		return
	}
	m.ID = types.StringValue(fmt.Sprintf("%x", fnv(m.Command.ValueString())))
	m.ExitCode = types.Int64Value(int64(res.Code))
	m.Stdout, m.Stderr = types.StringNull(), types.StringNull()
	if !m.SensitiveOutput.ValueBool() {
		m.Stdout, m.Stderr = types.StringValue(res.Stdout), types.StringValue(res.Stderr)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *execResource) Read(context.Context, resource.ReadRequest, *resource.ReadResponse) {}

func (r *execResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state execModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	plan.ID, plan.ExitCode = state.ID, state.ExitCode
	plan.Stdout, plan.Stderr = state.Stdout, state.Stderr
	if plan.SensitiveOutput.ValueBool() {
		plan.Stdout, plan.Stderr = types.StringNull(), types.StringNull()
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *execResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m execModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if m.DestroyCommand.IsNull() || m.DestroyCommand.ValueString() == "" {
		return
	}
	res, err := r.run(ctx, m.DestroyCommand.ValueString(), m.Environment)
	if err != nil {
		errDiag(&resp.Diagnostics, "Running destroy command", err)
		return
	}
	if res.Code != 0 {
		resp.Diagnostics.AddError("Destroy command failed", fmt.Sprintf("exit code %d", res.Code))
	}
}

func fnv(s string) uint64 {
	h := uint64(14695981039346656037)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}
