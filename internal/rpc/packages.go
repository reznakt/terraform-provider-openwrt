package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/reznakt/terraform-provider-openwrt/internal/ubus"
)

// PackageManager drives opkg or apk through LuCI's helper script, which the
// stock luci-app-package-manager ACL already allows executing. The helper
// always exits 0 and reports the real outcome as JSON on stdout.
type PackageManager struct {
	Command string
}

// helpers, newest first: package-manager-call (LuCI >= 24.10, opkg or apk)
// and opkg-call (older LuCI).
var helpers = []string{"/usr/libexec/package-manager-call", "/usr/libexec/opkg-call"}

// Packages detects the helper once per provider instance.
func (c *Client) Packages(ctx context.Context) (*PackageManager, error) {
	c.pkgOnce.Do(func() {
		for _, h := range helpers {
			_, err := c.FileStat(ctx, h)
			if err == nil {
				c.pkgMgr = &PackageManager{Command: h}
				return
			}
			if !ubus.IsNotFound(err) && !ubus.HasStatus(err, ubus.StatusPermissionDenied) {
				c.pkgErr = err
				return
			}
		}
		c.pkgErr = errors.New("no package manager helper found; install luci-app-package-manager (it provides /usr/libexec/package-manager-call)")
	})
	return c.pkgMgr, c.pkgErr
}

// helperResult is what the helper prints for install/remove/update.
type helperResult struct {
	Code   int    `json:"code"`
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
}

func (c *Client) pkgAction(ctx context.Context, args ...string) error {
	pm, err := c.Packages(ctx)
	if err != nil {
		return err
	}
	res, err := c.Exec(ctx, pm.Command, args, nil)
	if err != nil {
		return err
	}
	var out helperResult
	if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil {
		return fmt.Errorf("%s %s: unexpected output: %s", pm.Command, args[0], strings.TrimSpace(res.Stdout+res.Stderr))
	}
	if out.Code != 0 {
		return fmt.Errorf("%s %s exited with %d: %s", pm.Command, strings.Join(args, " "), out.Code, strings.TrimSpace(out.Stderr+"\n"+out.Stdout))
	}
	return nil
}

// Installed returns installed package names mapped to versions.
func (c *Client) Installed(ctx context.Context) (map[string]string, error) {
	pm, err := c.Packages(ctx)
	if err != nil {
		return nil, err
	}
	res, err := c.Exec(ctx, pm.Command, []string{"list-installed"}, nil)
	if err != nil {
		return nil, err
	}
	return ParseInstalled(res.Stdout), nil
}

// apk list --full: "name-1.2.3-r4 arch {origin} (license) [installed]".
var apkLine = regexp.MustCompile(`^(\S+?)-(\d\S*-r\d+)\s`)

// ParseInstalled understands the helper's list-installed output for both
// managers: the opkg status file ("Package:"/"Version:" stanzas) and
// `apk list --full` lines.
func ParseInstalled(out string) map[string]string {
	pkgs := map[string]string{}
	var name string
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		if v, ok := strings.CutPrefix(line, "Package: "); ok {
			name = strings.TrimSpace(v)
			continue
		}
		if v, ok := strings.CutPrefix(line, "Version: "); ok && name != "" {
			pkgs[name] = strings.TrimSpace(v)
			name = ""
			continue
		}
		if m := apkLine.FindStringSubmatch(line); m != nil {
			pkgs[m[1]] = m[2]
		}
	}
	return pkgs
}

// UpdateLists refreshes package feeds.
func (c *Client) UpdateLists(ctx context.Context) error { return c.pkgAction(ctx, "update") }

func (c *Client) Install(ctx context.Context, names ...string) error {
	return c.pkgAction(ctx, append([]string{"install"}, names...)...)
}

func (c *Client) Remove(ctx context.Context, names ...string) error {
	return c.pkgAction(ctx, append([]string{"remove"}, names...)...)
}
