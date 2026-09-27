package resources

import (
	"context"
	"encoding/base64"

	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/reznakt/terraform-provider-openwrt/internal/rpc"
)

// NewFileEphemeral reads a file without ever persisting it: useful for
// secrets that live on the router, like a WireGuard private key.
func NewFileEphemeral() ephemeral.EphemeralResource { return &fileEphemeral{} }

type fileEphemeral struct{ c *rpc.Client }

var _ ephemeral.EphemeralResourceWithConfigure = (*fileEphemeral)(nil)

func (e *fileEphemeral) Metadata(_ context.Context, req ephemeral.MetadataRequest, resp *ephemeral.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_file"
}

func (e *fileEphemeral) Configure(_ context.Context, req ephemeral.ConfigureRequest, resp *ephemeral.ConfigureResponse) {
	e.c = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (e *fileEphemeral) Schema(_ context.Context, _ ephemeral.SchemaRequest, resp *ephemeral.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads a file from the router without storing it in plan or state (Terraform/OpenTofu >= 1.10). Feed it into write-only attributes of other resources.",
		Attributes: map[string]schema.Attribute{
			"path":           schema.StringAttribute{Required: true},
			"content":        schema.StringAttribute{Computed: true, Sensitive: true},
			"content_base64": schema.StringAttribute{Computed: true, Sensitive: true},
		},
	}
}

func (e *fileEphemeral) Open(ctx context.Context, req ephemeral.OpenRequest, resp *ephemeral.OpenResponse) {
	var p types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("path"), &p)...)
	b, err := e.c.FileRead(ctx, p.ValueString())
	if err != nil {
		errDiag(&resp.Diagnostics, "Reading "+p.ValueString(), err)
		return
	}
	resp.Diagnostics.Append(resp.Result.SetAttribute(ctx, path.Root("path"), p)...)
	resp.Diagnostics.Append(resp.Result.SetAttribute(ctx, path.Root("content"), string(b))...)
	resp.Diagnostics.Append(resp.Result.SetAttribute(ctx, path.Root("content_base64"), base64.StdEncoding.EncodeToString(b))...)
}
