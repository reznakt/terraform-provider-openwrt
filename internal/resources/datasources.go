package resources

import (
	"context"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/reznakt/terraform-provider-openwrt/internal/rpc"
	"github.com/reznakt/terraform-provider-openwrt/internal/secrets"
	"github.com/reznakt/terraform-provider-openwrt/internal/uci"
)

// baseDS carries the client for all data sources.
type baseDS struct{ c *rpc.Client }

func (b *baseDS) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	b.c = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func dsName(req datasource.MetadataRequest, name string) string {
	return req.ProviderTypeName + "_" + name
}

// ---------------------------------------------------------------- board

func NewBoardDataSource() datasource.DataSource { return &boardDS{} }

type boardDS struct{ baseDS }

type boardModel struct {
	Hostname     types.String `tfsdk:"hostname"`
	Model        types.String `tfsdk:"model"`
	BoardName    types.String `tfsdk:"board_name"`
	Kernel       types.String `tfsdk:"kernel"`
	System       types.String `tfsdk:"system"`
	RootfsType   types.String `tfsdk:"rootfs_type"`
	Distribution types.String `tfsdk:"distribution"`
	Version      types.String `tfsdk:"version"`
	Revision     types.String `tfsdk:"revision"`
	Target       types.String `tfsdk:"target"`
	Description  types.String `tfsdk:"description"`
}

func (d *boardDS) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = dsName(req, "board")
}

func (d *boardDS) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := map[string]schema.Attribute{}
	for _, a := range []string{"hostname", "model", "board_name", "kernel", "system", "rootfs_type", "distribution", "version", "revision", "target", "description"} {
		attrs[a] = schema.StringAttribute{Computed: true}
	}
	resp.Schema = schema.Schema{Description: "Hardware and firmware information (`ubus call system board`).", Attributes: attrs}
}

func (d *boardDS) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	var out struct {
		Hostname   string `json:"hostname"`
		Model      string `json:"model"`
		BoardName  string `json:"board_name"`
		Kernel     string `json:"kernel"`
		System     string `json:"system"`
		RootfsType string `json:"rootfs_type"`
		Release    struct {
			Distribution string `json:"distribution"`
			Version      string `json:"version"`
			Revision     string `json:"revision"`
			Target       string `json:"target"`
			Description  string `json:"description"`
		} `json:"release"`
	}
	if err := d.c.Call(ctx, "system", "board", nil, &out); err != nil {
		errDiag(&resp.Diagnostics, "Reading board info", err)
		return
	}
	s := types.StringValue
	resp.Diagnostics.Append(resp.State.Set(ctx, &boardModel{
		Hostname: s(out.Hostname), Model: s(out.Model), BoardName: s(out.BoardName), Kernel: s(out.Kernel),
		System: s(out.System), RootfsType: s(out.RootfsType), Distribution: s(out.Release.Distribution),
		Version: s(out.Release.Version), Revision: s(out.Release.Revision), Target: s(out.Release.Target),
		Description: s(out.Release.Description),
	})...)
}

// ---------------------------------------------------------------- system info

func NewSystemInfoDataSource() datasource.DataSource { return &sysInfoDS{} }

type sysInfoDS struct{ baseDS }

type sysInfoModel struct {
	Uptime    types.Int64 `tfsdk:"uptime"`
	Localtime types.Int64 `tfsdk:"localtime"`
	Load      types.List  `tfsdk:"load"`
	Memory    types.Map   `tfsdk:"memory"`
	Root      types.Map   `tfsdk:"root"`
	Tmp       types.Map   `tfsdk:"tmp"`
}

func (d *sysInfoDS) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = dsName(req, "system_info")
}

func (d *sysInfoDS) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	nm := func(desc string) schema.MapAttribute {
		return schema.MapAttribute{ElementType: types.Int64Type, Computed: true, Description: desc}
	}
	resp.Schema = schema.Schema{
		Description: "Runtime information (`ubus call system info`).",
		Attributes: map[string]schema.Attribute{
			"uptime":    schema.Int64Attribute{Computed: true, Description: "Uptime in seconds."},
			"localtime": schema.Int64Attribute{Computed: true, Description: "Local time as a Unix timestamp."},
			"load":      schema.ListAttribute{ElementType: types.Int64Type, Computed: true, Description: "Load averages (1/5/15 min, scaled by 65536)."},
			"memory":    nm("Memory counters in bytes."),
			"root":      nm("Root filesystem usage in KiB."),
			"tmp":       nm("/tmp usage in KiB."),
		},
	}
}

func int64Map(m map[string]int64) types.Map {
	vals := map[string]attr.Value{}
	for k, v := range m {
		vals[k] = types.Int64Value(v)
	}
	return types.MapValueMust(types.Int64Type, vals)
}

func (d *sysInfoDS) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	var out struct {
		Uptime    int64            `json:"uptime"`
		Localtime int64            `json:"localtime"`
		Load      []int64          `json:"load"`
		Memory    map[string]int64 `json:"memory"`
		Root      map[string]int64 `json:"root"`
		Tmp       map[string]int64 `json:"tmp"`
	}
	if err := d.c.Call(ctx, "system", "info", nil, &out); err != nil {
		errDiag(&resp.Diagnostics, "Reading system info", err)
		return
	}
	load := make([]attr.Value, len(out.Load))
	for i, l := range out.Load {
		load[i] = types.Int64Value(l)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &sysInfoModel{
		Uptime: types.Int64Value(out.Uptime), Localtime: types.Int64Value(out.Localtime),
		Load:   types.ListValueMust(types.Int64Type, load),
		Memory: int64Map(out.Memory), Root: int64Map(out.Root), Tmp: int64Map(out.Tmp),
	})...)
}

// ---------------------------------------------------------------- uci section(s)

func NewUCISectionDataSource() datasource.DataSource  { return &uciSectionDS{} }
func NewUCISectionsDataSource() datasource.DataSource { return &uciSectionsDS{} }

type uciSectionDS struct{ baseDS }
type uciSectionsDS struct{ baseDS }

// sectionAttrs are the per-section outputs shared by both UCI data sources.
func sectionAttrs() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"name":      schema.StringAttribute{Computed: true, Description: "Section name (generated for anonymous sections)."},
		"type":      schema.StringAttribute{Computed: true, Description: "Section type."},
		"anonymous": schema.BoolAttribute{Computed: true, Description: "Whether the section is anonymous."},
		"index":     schema.Int64Attribute{Computed: true, Description: "Position in the config file."},
		"options":   schema.MapAttribute{ElementType: types.StringType, Computed: true, Description: "Scalar options, excluding secret-looking ones."},
		"lists":     schema.MapAttribute{ElementType: types.ListType{ElemType: types.StringType}, Computed: true, Description: "List options."},
		"sensitive_options": schema.MapAttribute{
			ElementType: types.StringType, Computed: true, Sensitive: true,
			Description: "Secret-looking options; only populated with `include_sensitive = true`.",
		},
		"redacted_options": schema.ListAttribute{ElementType: types.StringType, Computed: true, Description: "Names of secret-looking options that were withheld."},
	}
}

var sectionObjType = map[string]attr.Type{
	"name": types.StringType, "type": types.StringType, "anonymous": types.BoolType, "index": types.Int64Type,
	"options": types.MapType{ElemType: types.StringType}, "lists": types.MapType{ElemType: types.ListType{ElemType: types.StringType}},
	"sensitive_options": types.MapType{ElemType: types.StringType}, "redacted_options": types.ListType{ElemType: types.StringType},
}

// sectionValues splits a section into public and secret parts.
func sectionValues(s *uci.Section, includeSensitive bool) map[string]attr.Value {
	opts, sens := map[string]attr.Value{}, map[string]attr.Value{}
	var redacted []attr.Value
	keys := make([]string, 0, len(s.Options))
	for k := range s.Options {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if secrets.LooksSecret(k) {
			if includeSensitive {
				sens[k] = types.StringValue(s.Options[k])
			} else {
				redacted = append(redacted, types.StringValue(k))
			}
			continue
		}
		opts[k] = types.StringValue(s.Options[k])
	}
	lists := map[string]attr.Value{}
	for k, items := range s.Lists {
		vals := make([]attr.Value, len(items))
		for i, it := range items {
			vals[i] = types.StringValue(it)
		}
		lists[k] = types.ListValueMust(types.StringType, vals)
	}
	sensMap := types.MapNull(types.StringType)
	if includeSensitive {
		sensMap = types.MapValueMust(types.StringType, sens)
	}
	if redacted == nil {
		redacted = []attr.Value{}
	}
	return map[string]attr.Value{
		"name": types.StringValue(s.Name), "type": types.StringValue(s.Type),
		"anonymous": types.BoolValue(s.Anonymous), "index": types.Int64Value(int64(s.Index)),
		"options":           types.MapValueMust(types.StringType, opts),
		"lists":             types.MapValueMust(types.ListType{ElemType: types.StringType}, lists),
		"sensitive_options": sensMap,
		"redacted_options":  types.ListValueMust(types.StringType, redacted),
	}
}

func (d *uciSectionDS) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = dsName(req, "uci_section")
}

func (d *uciSectionDS) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := sectionAttrs()
	attrs["config"] = schema.StringAttribute{Required: true, Description: "Config file name."}
	attrs["section"] = schema.StringAttribute{Required: true, Description: "Section name or `@type[index]`."}
	attrs["include_sensitive"] = schema.BoolAttribute{Optional: true, Description: "Return secret-looking options in `sensitive_options`."}
	resp.Schema = schema.Schema{Description: "Reads one UCI section. Secret-looking options are withheld unless `include_sensitive` is set.", Attributes: attrs}
}

func (d *uciSectionDS) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg, ref types.String
	var inc types.Bool
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, pathRoot("config"), &cfg)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, pathRoot("section"), &ref)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, pathRoot("include_sensitive"), &inc)...)
	u := d.c.UCI.UCI
	name, err := u.Resolve(ctx, cfg.ValueString(), ref.ValueString())
	if err != nil {
		errDiag(&resp.Diagnostics, "Reading "+cfg.ValueString()+"."+ref.ValueString(), err)
		return
	}
	s, err := u.Get(ctx, cfg.ValueString(), name)
	if err != nil {
		errDiag(&resp.Diagnostics, "Reading "+cfg.ValueString()+"."+ref.ValueString(), err)
		return
	}
	for k, v := range sectionValues(s, inc.ValueBool()) {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, pathRoot(k), v)...)
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, pathRoot("config"), cfg)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, pathRoot("section"), ref)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, pathRoot("include_sensitive"), inc)...)
}

func (d *uciSectionsDS) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = dsName(req, "uci_sections")
}

func (d *uciSectionsDS) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the sections of a UCI config in file order. Secret-looking options are withheld unless `include_sensitive` is set.",
		Attributes: map[string]schema.Attribute{
			"config":            schema.StringAttribute{Required: true, Description: "Config file name."},
			"type":              schema.StringAttribute{Optional: true, Description: "Only sections of this type."},
			"include_sensitive": schema.BoolAttribute{Optional: true, Description: "Return secret-looking options in `sensitive_options`."},
			"sections": schema.ListNestedAttribute{
				Computed: true, Description: "The sections.",
				NestedObject: schema.NestedAttributeObject{Attributes: sectionAttrs()},
			},
		},
	}
}

func (d *uciSectionsDS) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg, typ types.String
	var inc types.Bool
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, pathRoot("config"), &cfg)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, pathRoot("type"), &typ)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, pathRoot("include_sensitive"), &inc)...)
	secs, err := d.c.UCI.UCI.List(ctx, cfg.ValueString(), typ.ValueString())
	if err != nil {
		errDiag(&resp.Diagnostics, "Reading "+cfg.ValueString(), err)
		return
	}
	objs := make([]attr.Value, len(secs))
	for i, s := range secs {
		objs[i] = types.ObjectValueMust(sectionObjType, sectionValues(s, inc.ValueBool()))
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, pathRoot("config"), cfg)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, pathRoot("type"), typ)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, pathRoot("include_sensitive"), inc)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, pathRoot("sections"), types.ListValueMust(types.ObjectType{AttrTypes: sectionObjType}, objs))...)
}

// ---------------------------------------------------------------- network interface

func NewNetworkInterfaceDataSource() datasource.DataSource { return &netIfaceDS{} }

type netIfaceDS struct{ baseDS }

func (d *netIfaceDS) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = dsName(req, "network_interface")
}

func (d *netIfaceDS) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	sl := func(desc string) schema.ListAttribute {
		return schema.ListAttribute{ElementType: types.StringType, Computed: true, Description: desc}
	}
	resp.Schema = schema.Schema{
		Description: "Runtime status of a logical interface (netifd).",
		Attributes: map[string]schema.Attribute{
			"name":           schema.StringAttribute{Required: true, Description: "Logical interface, e.g. `wan`."},
			"up":             schema.BoolAttribute{Computed: true},
			"pending":        schema.BoolAttribute{Computed: true},
			"available":      schema.BoolAttribute{Computed: true},
			"uptime":         schema.Int64Attribute{Computed: true, Description: "Seconds since the interface came up."},
			"proto":          schema.StringAttribute{Computed: true},
			"device":         schema.StringAttribute{Computed: true},
			"l3_device":      schema.StringAttribute{Computed: true},
			"ipv4_addresses": sl("IPv4 addresses in CIDR notation."),
			"ipv6_addresses": sl("IPv6 addresses in CIDR notation."),
			"ipv6_prefixes":  sl("Delegated IPv6 prefixes in CIDR notation."),
			"dns_servers":    sl("DNS servers."),
		},
	}
}

type addrMask struct {
	Address string `json:"address"`
	Mask    int    `json:"mask"`
}

func cidrs(in []addrMask) types.List {
	vals := make([]attr.Value, len(in))
	for i, a := range in {
		vals[i] = types.StringValue(fmt.Sprintf("%s/%d", a.Address, a.Mask))
	}
	return types.ListValueMust(types.StringType, vals)
}

func strList(in []string) types.List {
	vals := make([]attr.Value, len(in))
	for i, s := range in {
		vals[i] = types.StringValue(s)
	}
	return types.ListValueMust(types.StringType, vals)
}

func (d *netIfaceDS) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var name types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, pathRoot("name"), &name)...)
	var out struct {
		Interface []struct {
			Interface string     `json:"interface"`
			Up        bool       `json:"up"`
			Pending   bool       `json:"pending"`
			Available bool       `json:"available"`
			Uptime    int64      `json:"uptime"`
			Proto     string     `json:"proto"`
			Device    string     `json:"device"`
			L3Device  string     `json:"l3_device"`
			IPv4      []addrMask `json:"ipv4-address"`
			IPv6      []addrMask `json:"ipv6-address"`
			Prefix    []addrMask `json:"ipv6-prefix"`
			DNS       []string   `json:"dns-server"`
		} `json:"interface"`
	}
	if err := d.c.Call(ctx, "network.interface", "dump", nil, &out); err != nil {
		errDiag(&resp.Diagnostics, "Reading interfaces", err)
		return
	}
	for _, i := range out.Interface {
		if i.Interface != name.ValueString() {
			continue
		}
		for k, v := range map[string]attr.Value{
			"up": types.BoolValue(i.Up), "pending": types.BoolValue(i.Pending), "available": types.BoolValue(i.Available),
			"uptime": types.Int64Value(i.Uptime), "proto": types.StringValue(i.Proto), "device": types.StringValue(i.Device),
			"l3_device": types.StringValue(i.L3Device), "ipv4_addresses": cidrs(i.IPv4), "ipv6_addresses": cidrs(i.IPv6),
			"ipv6_prefixes": cidrs(i.Prefix), "dns_servers": strList(i.DNS),
		} {
			resp.Diagnostics.Append(resp.State.SetAttribute(ctx, pathRoot(k), v)...)
		}
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, pathRoot("name"), name)...)
		return
	}
	resp.Diagnostics.AddError("Interface not found", fmt.Sprintf("netifd has no interface %q", name.ValueString()))
}

// ---------------------------------------------------------------- wireless status

func NewWirelessStatusDataSource() datasource.DataSource { return &wifiDS{} }

type wifiDS struct{ baseDS }

var wifiIfaceType = map[string]attr.Type{"section": types.StringType, "ifname": types.StringType, "ssid": types.StringType, "mode": types.StringType, "network": types.ListType{ElemType: types.StringType}}
var radioType = map[string]attr.Type{"name": types.StringType, "up": types.BoolType, "pending": types.BoolType, "disabled": types.BoolType, "interfaces": types.ListType{ElemType: types.ObjectType{AttrTypes: wifiIfaceType}}}

func (d *wifiDS) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = dsName(req, "wireless_status")
}

func (d *wifiDS) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Runtime status of radios and their networks (`ubus call network.wireless status`). Only non-secret fields are exposed; keys are never read into state.",
		Attributes: map[string]schema.Attribute{
			"radios": schema.ListNestedAttribute{
				Computed: true,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"name":     schema.StringAttribute{Computed: true},
					"up":       schema.BoolAttribute{Computed: true},
					"pending":  schema.BoolAttribute{Computed: true},
					"disabled": schema.BoolAttribute{Computed: true},
					"interfaces": schema.ListNestedAttribute{
						Computed: true,
						NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
							"section": schema.StringAttribute{Computed: true},
							"ifname":  schema.StringAttribute{Computed: true},
							"ssid":    schema.StringAttribute{Computed: true},
							"mode":    schema.StringAttribute{Computed: true},
							"network": schema.ListAttribute{ElementType: types.StringType, Computed: true},
						}},
					},
				}},
			},
		},
	}
}

func (d *wifiDS) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	// Decode only whitelisted fields: the config blobs in this reply contain keys.
	var out map[string]struct {
		Up         bool `json:"up"`
		Pending    bool `json:"pending"`
		Disabled   bool `json:"disabled"`
		Interfaces []struct {
			Section string `json:"section"`
			Ifname  string `json:"ifname"`
			Config  struct {
				SSID    string   `json:"ssid"`
				Mode    string   `json:"mode"`
				Network []string `json:"network"`
			} `json:"config"`
		} `json:"interfaces"`
	}
	if err := d.c.Call(ctx, "network.wireless", "status", nil, &out); err != nil {
		errDiag(&resp.Diagnostics, "Reading wireless status", err)
		return
	}
	names := make([]string, 0, len(out))
	for n := range out {
		names = append(names, n)
	}
	sort.Strings(names)
	radios := make([]attr.Value, 0, len(names))
	for _, n := range names {
		r := out[n]
		ifaces := make([]attr.Value, len(r.Interfaces))
		for i, it := range r.Interfaces {
			ifaces[i] = types.ObjectValueMust(wifiIfaceType, map[string]attr.Value{
				"section": types.StringValue(it.Section), "ifname": types.StringValue(it.Ifname),
				"ssid": types.StringValue(it.Config.SSID), "mode": types.StringValue(it.Config.Mode),
				"network": strList(it.Config.Network),
			})
		}
		radios = append(radios, types.ObjectValueMust(radioType, map[string]attr.Value{
			"name": types.StringValue(n), "up": types.BoolValue(r.Up), "pending": types.BoolValue(r.Pending),
			"disabled":   types.BoolValue(r.Disabled),
			"interfaces": types.ListValueMust(types.ObjectType{AttrTypes: wifiIfaceType}, ifaces),
		}))
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, pathRoot("radios"), types.ListValueMust(types.ObjectType{AttrTypes: radioType}, radios))...)
}

// ---------------------------------------------------------------- packages

func NewPackagesDataSource() datasource.DataSource { return &packagesDS{} }

type packagesDS struct{ baseDS }

func (d *packagesDS) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = dsName(req, "packages")
}

func (d *packagesDS) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Installed packages.",
		Attributes: map[string]schema.Attribute{
			"helper":   schema.StringAttribute{Computed: true, Description: "Path of the LuCI package-manager helper in use."},
			"packages": schema.MapAttribute{ElementType: types.StringType, Computed: true, Description: "Package name to version."},
		},
	}
}

func (d *packagesDS) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	pm, err := d.c.Packages(ctx)
	if err != nil {
		errDiag(&resp.Diagnostics, "Detecting package manager", err)
		return
	}
	pkgs, err := d.c.Installed(ctx)
	if err != nil {
		errDiag(&resp.Diagnostics, "Listing packages", err)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, pathRoot("helper"), pm.Command)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, pathRoot("packages"), pkgs)...)
}

// ---------------------------------------------------------------- dhcp leases

func NewDHCPLeasesDataSource() datasource.DataSource { return &leasesDS{} }

type leasesDS struct{ baseDS }

var leaseType = map[string]attr.Type{"hostname": types.StringType, "address": types.StringType, "mac": types.StringType, "duid": types.StringType, "expires": types.Int64Type}

func (d *leasesDS) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = dsName(req, "dhcp_leases")
}

func (d *leasesDS) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	lease := schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
		"hostname": schema.StringAttribute{Computed: true},
		"address":  schema.StringAttribute{Computed: true},
		"mac":      schema.StringAttribute{Computed: true},
		"duid":     schema.StringAttribute{Computed: true},
		"expires":  schema.Int64Attribute{Computed: true, Description: "Seconds until expiry (-1 = static)."},
	}}
	resp.Schema = schema.Schema{
		Description: "Active DHCPv4/DHCPv6 leases (via `luci-rpc getDHCPLeases`).",
		Attributes: map[string]schema.Attribute{
			"ipv4": schema.ListNestedAttribute{Computed: true, NestedObject: lease},
			"ipv6": schema.ListNestedAttribute{Computed: true, NestedObject: lease},
		},
	}
}

func (d *leasesDS) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	type lease struct {
		Hostname string   `json:"hostname"`
		IPAddr   string   `json:"ipaddr"`
		IP6Addr  string   `json:"ip6addr"`
		IP6Addrs []string `json:"ip6addrs"`
		MAC      string   `json:"macaddr"`
		DUID     string   `json:"duid"`
		Expires  int64    `json:"expires"`
	}
	var out struct {
		V4 []lease `json:"dhcp_leases"`
		V6 []lease `json:"dhcp6_leases"`
	}
	if err := d.c.Call(ctx, "luci-rpc", "getDHCPLeases", nil, &out); err != nil {
		errDiag(&resp.Diagnostics, "Reading DHCP leases", err)
		return
	}
	conv := func(in []lease) types.List {
		vals := make([]attr.Value, len(in))
		for i, l := range in {
			addr := l.IPAddr
			if addr == "" {
				addr = l.IP6Addr
			}
			if addr == "" && len(l.IP6Addrs) > 0 {
				addr = strings.Join(l.IP6Addrs, " ")
			}
			vals[i] = types.ObjectValueMust(leaseType, map[string]attr.Value{
				"hostname": types.StringValue(l.Hostname), "address": types.StringValue(addr),
				"mac": types.StringValue(l.MAC), "duid": types.StringValue(l.DUID), "expires": types.Int64Value(l.Expires),
			})
		}
		return types.ListValueMust(types.ObjectType{AttrTypes: leaseType}, vals)
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, pathRoot("ipv4"), conv(out.V4))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, pathRoot("ipv6"), conv(out.V6))...)
}

// ---------------------------------------------------------------- file

func NewFileDataSource() datasource.DataSource { return &fileDS{} }

type fileDS struct{ baseDS }

func (d *fileDS) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = dsName(req, "file")
}

func (d *fileDS) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads a file from the router. Content is always sensitive; use the `openwrt_file` ephemeral resource to keep it out of state entirely.",
		Attributes: map[string]schema.Attribute{
			"path":           schema.StringAttribute{Required: true},
			"content":        schema.StringAttribute{Computed: true, Sensitive: true},
			"content_base64": schema.StringAttribute{Computed: true, Sensitive: true},
			"md5":            schema.StringAttribute{Computed: true},
			"mode":           schema.StringAttribute{Computed: true},
			"size":           schema.Int64Attribute{Computed: true},
		},
	}
}

func (d *fileDS) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var p types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, pathRoot("path"), &p)...)
	st, err := d.c.FileStat(ctx, p.ValueString())
	if err != nil {
		errDiag(&resp.Diagnostics, "Reading "+p.ValueString(), err)
		return
	}
	b, err := d.c.FileRead(ctx, p.ValueString())
	if err != nil {
		errDiag(&resp.Diagnostics, "Reading "+p.ValueString(), err)
		return
	}
	for k, v := range map[string]attr.Value{
		"path": p, "content": types.StringValue(string(b)), "content_base64": types.StringValue(base64.StdEncoding.EncodeToString(b)),
		"md5": types.StringValue(md5hex(b)), "mode": types.StringValue(fmt.Sprintf("%04o", st.Mode&0o7777)), "size": types.Int64Value(st.Size),
	} {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, pathRoot(k), v)...)
	}
}
