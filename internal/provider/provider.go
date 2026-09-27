// Package provider wires the OpenWrt provider together.
package provider

import (
	"context"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/reznakt/terraform-provider-openwrt/internal/resources"
	"github.com/reznakt/terraform-provider-openwrt/internal/rpc"
	"github.com/reznakt/terraform-provider-openwrt/internal/secrets"
	"github.com/reznakt/terraform-provider-openwrt/internal/ubus"
	"github.com/reznakt/terraform-provider-openwrt/internal/uci"
)

// New returns the provider factory.
func New(version string) func() provider.Provider {
	return func() provider.Provider { return &openwrtProvider{version: version} }
}

type openwrtProvider struct{ version string }

var _ provider.ProviderWithEphemeralResources = (*openwrtProvider)(nil)

type providerModel struct {
	Endpoint       types.String `tfsdk:"endpoint"`
	Username       types.String `tfsdk:"username"`
	Password       types.String `tfsdk:"password"`
	PasswordFile   types.String `tfsdk:"password_file"`
	Insecure       types.Bool   `tfsdk:"insecure"`
	CACert         types.String `tfsdk:"ca_cert"`
	RequestTimeout types.Int64  `tfsdk:"request_timeout"`
	Rollback       types.Bool   `tfsdk:"rollback"`
	ApplyTimeout   types.Int64  `tfsdk:"apply_timeout"`
	ApplyHoldoff   types.Int64  `tfsdk:"apply_holdoff"`
}

func (p *openwrtProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "openwrt"
	resp.Version = p.version
}

func (p *openwrtProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	desc := "Manages OpenWrt routers through the ubus JSON-RPC API served by uhttpd (`uhttpd-mod-ubus` + `rpcd`). " +
		"Every change is applied with `uci apply` rollback protection by default: if a change cuts the provider off, the router reverts it."
	resp.Schema = schema.Schema{
		Description: desc, MarkdownDescription: desc,
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				Optional:    true,
				Description: "Router URL, e.g. `https://192.168.1.1` (`/ubus` is appended when the path is empty). Env: `OPENWRT_ENDPOINT`.",
			},
			"username": schema.StringAttribute{
				Optional:    true,
				Description: "rpcd login name. Defaults to `root`. Env: `OPENWRT_USERNAME`.",
			},
			"password": schema.StringAttribute{
				Optional: true, Sensitive: true,
				Description: "rpcd login password. Env: `OPENWRT_PASSWORD`. Prefer `password_file` or the environment over literals.",
				Validators:  []validator.String{stringvalidator.ConflictsWith(path.MatchRoot("password_file"))},
			},
			"password_file": schema.StringAttribute{
				Optional:    true,
				Description: "Path to a file containing the password (trailing newline ignored), e.g. from sops or agenix. Env: `OPENWRT_PASSWORD_FILE`.",
			},
			"insecure": schema.BoolAttribute{
				Optional:    true,
				Description: "Skip TLS verification (the router's self-signed certificate). Env: `OPENWRT_INSECURE`. Prefer `ca_cert`.",
			},
			"ca_cert": schema.StringAttribute{
				Optional:    true,
				Description: "PEM certificate(s) to trust, e.g. the router's `/etc/uhttpd.crt`. Env: `OPENWRT_CA_CERT`.",
			},
			"request_timeout": schema.Int64Attribute{
				Optional:    true,
				Description: "Timeout of a single API request in seconds. Default 30.",
			},
			"rollback": schema.BoolAttribute{
				Optional:    true,
				Description: "Apply changes with `uci apply` rollback protection. Default true. When false, changes are committed and `reload_config` is called directly.",
			},
			"apply_timeout": schema.Int64Attribute{
				Optional:    true,
				Description: "Seconds the router waits for confirmation before rolling a change back. Default 30.",
			},
			"apply_holdoff": schema.Int64Attribute{
				Optional:    true,
				Description: "Seconds to wait after applying before confirming, so services have reloaded. Default 4.",
			},
		},
	}
}

func envOr(v types.String, env string) string {
	if !v.IsNull() && !v.IsUnknown() {
		return v.ValueString()
	}
	return os.Getenv(env)
}

func (p *openwrtProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var m providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if m.Endpoint.IsUnknown() || m.Password.IsUnknown() {
		// Values from resources not created yet; resources will fail clearly.
		return
	}

	endpoint := envOr(m.Endpoint, "OPENWRT_ENDPOINT")
	if endpoint == "" {
		resp.Diagnostics.AddAttributeError(path.Root("endpoint"), "Missing endpoint", "Set `endpoint` or OPENWRT_ENDPOINT, e.g. https://192.168.1.1.")
		return
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		resp.Diagnostics.AddAttributeError(path.Root("endpoint"), "Invalid endpoint", "Expected an http(s) URL such as https://192.168.1.1.")
		return
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/ubus"
	}
	if u.Scheme == "http" {
		resp.Diagnostics.AddWarning("Unencrypted endpoint", "The rpcd password and every secret you manage travel in cleartext over http. Use https.")
	}

	username := envOr(m.Username, "OPENWRT_USERNAME")
	if username == "" {
		username = "root"
	}
	password := envOr(m.Password, "OPENWRT_PASSWORD")
	if pf := envOr(m.PasswordFile, "OPENWRT_PASSWORD_FILE"); pf != "" && password == "" {
		raw, err := os.ReadFile(pf)
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("password_file"), "Cannot read password file", err.Error())
			return
		}
		password = strings.TrimRight(string(raw), "\r\n")
	}

	insecure := m.Insecure.ValueBool()
	if m.Insecure.IsNull() {
		insecure, _ = strconv.ParseBool(os.Getenv("OPENWRT_INSECURE"))
	}

	seconds := func(v types.Int64, def int64) time.Duration {
		if v.IsNull() || v.ValueInt64() <= 0 {
			return time.Duration(def) * time.Second
		}
		return time.Duration(v.ValueInt64()) * time.Second
	}

	client, err := ubus.New(ubus.Config{
		Endpoint:           u.String(),
		Username:           username,
		Password:           password,
		RequestTimeout:     seconds(m.RequestTimeout, 30),
		InsecureSkipVerify: insecure,
		CACertPEM:          envOr(m.CACert, "OPENWRT_CA_CERT"),
		Log: func(ctx context.Context, msg string, fields map[string]any) {
			tflog.Debug(maskedContext(ctx, password), msg, fields)
		},
	})
	if err != nil {
		resp.Diagnostics.AddError("Cannot create ubus client", err.Error())
		return
	}

	mgr := uci.NewManager(client)
	mgr.Rollback = m.Rollback.IsNull() || m.Rollback.ValueBool()
	mgr.Timeout = seconds(m.ApplyTimeout, 30)
	mgr.Holdoff = seconds(m.ApplyHoldoff, 4)
	if !m.ApplyHoldoff.IsNull() && m.ApplyHoldoff.ValueInt64() == 0 {
		mgr.Holdoff = 0
	}

	c := rpc.New(client, mgr)
	resp.ResourceData = c
	resp.DataSourceData = c
	resp.EphemeralResourceData = c
}

// maskedContext applies the provider-wide log masking: secret field keys and
// the password value itself never reach the log sink.
func maskedContext(ctx context.Context, password string) context.Context {
	ctx = tflog.MaskFieldValuesWithFieldKeys(ctx, secrets.LogFieldKeys...)
	ctx = tflog.MaskAllFieldValuesRegexes(ctx, secrets.Pattern)
	if password != "" {
		ctx = tflog.MaskAllFieldValuesStrings(ctx, password)
		ctx = tflog.MaskMessageStrings(ctx, password)
	}
	return ctx
}

func (p *openwrtProvider) Resources(_ context.Context) []func() resource.Resource {
	return append([]func() resource.Resource{
		resources.NewUCISection,
		resources.NewUCIOrder,
		resources.NewPackage,
		resources.NewFile,
		resources.NewExec,
		resources.NewService,
	}, resources.SectionResources()...)
}

func (p *openwrtProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		resources.NewBoardDataSource,
		resources.NewSystemInfoDataSource,
		resources.NewUCISectionDataSource,
		resources.NewUCISectionsDataSource,
		resources.NewNetworkInterfaceDataSource,
		resources.NewWirelessStatusDataSource,
		resources.NewPackagesDataSource,
		resources.NewDHCPLeasesDataSource,
		resources.NewFileDataSource,
	}
}

func (p *openwrtProvider) EphemeralResources(_ context.Context) []func() ephemeral.EphemeralResource {
	return []func() ephemeral.EphemeralResource{resources.NewFileEphemeral}
}
