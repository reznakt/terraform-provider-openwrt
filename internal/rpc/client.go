// Package rpc bundles everything resources need to talk to one router: the
// raw ubus client, the serialized uci manager, and typed helpers for the
// non-uci rpcd objects (file, rc, system, package manager).
package rpc

import (
	"context"
	"encoding/base64"
	"fmt"
	"sync"

	"github.com/reznakt/terraform-provider-openwrt/internal/ubus"
	"github.com/reznakt/terraform-provider-openwrt/internal/uci"
)

// Client is what the provider hands to resources as ProviderData.
type Client struct {
	Ubus *ubus.Client
	UCI  *uci.Manager

	pkgOnce sync.Once
	pkgMgr  *PackageManager
	pkgErr  error
}

func New(c *ubus.Client, m *uci.Manager) *Client { return &Client{Ubus: c, UCI: m} }

// Call is a shorthand for Ubus.Call.
func (c *Client) Call(ctx context.Context, object, method string, args, out any) error {
	return c.Ubus.Call(ctx, object, method, args, out)
}

// FileStat is the reply of file.stat.
type FileStat struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Size int64  `json:"size"`
	Mode int64  `json:"mode"`
}

func (c *Client) FileStat(ctx context.Context, path string) (*FileStat, error) {
	var st FileStat
	if err := c.Call(ctx, "file", "stat", map[string]any{"path": path}, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func (c *Client) FileRead(ctx context.Context, path string) ([]byte, error) {
	var out struct {
		Data string `json:"data"`
	}
	if err := c.Call(ctx, "file", "read", map[string]any{"path": path, "base64": true}, &out); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(out.Data)
}

// FileWrite writes data with the given permission bits (e.g. 0o600).
func (c *Client) FileWrite(ctx context.Context, path string, data []byte, mode int64) error {
	return c.Call(ctx, "file", "write", map[string]any{
		"path":   path,
		"data":   base64.StdEncoding.EncodeToString(data),
		"base64": true,
		"mode":   mode,
	}, nil)
}

func (c *Client) FileMD5(ctx context.Context, path string) (string, error) {
	var out struct {
		MD5 string `json:"md5"`
	}
	err := c.Call(ctx, "file", "md5", map[string]any{"path": path}, &out)
	return out.MD5, err
}

func (c *Client) FileRemove(ctx context.Context, path string) error {
	err := c.Call(ctx, "file", "remove", map[string]any{"path": path}, nil)
	if ubus.IsNotFound(err) {
		return nil
	}
	return err
}

// ExecResult is the reply of file.exec.
type ExecResult struct {
	Code   int    `json:"code"`
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
}

// Exec runs command with params via rpcd-mod-file. The login's ACL must
// grant "exec" on the command path.
func (c *Client) Exec(ctx context.Context, command string, params []string, env map[string]string) (*ExecResult, error) {
	args := map[string]any{"command": command}
	if len(params) > 0 {
		args["params"] = params
	}
	if len(env) > 0 {
		args["env"] = env
	}
	var out ExecResult
	if err := c.Call(ctx, "file", "exec", args, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ServiceState is one entry of rc.list.
type ServiceState struct {
	Enabled bool `json:"enabled"`
	Running bool `json:"running"`
}

func (c *Client) Service(ctx context.Context, name string) (*ServiceState, error) {
	var out map[string]ServiceState
	if err := c.Call(ctx, "rc", "list", map[string]any{"name": name}, &out); err != nil {
		return nil, err
	}
	st, ok := out[name]
	if !ok {
		return nil, &ubus.Error{Object: "rc", Method: "list", Status: ubus.StatusNotFound}
	}
	return &st, nil
}

// ServiceAction runs an init action: enable, disable, start, stop, restart, reload.
func (c *Client) ServiceAction(ctx context.Context, name, action string) error {
	if err := c.Call(ctx, "rc", "init", map[string]any{"name": name, "action": action}, nil); err != nil {
		return fmt.Errorf("service %s %s: %w", name, action, err)
	}
	return nil
}
