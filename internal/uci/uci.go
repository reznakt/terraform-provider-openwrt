// Package uci wraps the rpcd "uci" ubus object and serializes config applies
// with rollback protection.
package uci

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/reznakt/terraform-provider-openwrt/internal/ubus"
)

// Caller is the subset of ubus.Client used here; tests substitute a fake.
type Caller interface {
	Call(ctx context.Context, object, method string, args any, out any) error
	Reconnect()
}

// Section is one UCI section with its options split into scalars and lists.
type Section struct {
	Config    string
	Name      string
	Type      string
	Anonymous bool
	Index     int
	Options   map[string]string
	Lists     map[string][]string
}

// Has reports whether the section has option name as scalar or list.
func (s *Section) Has(name string) bool {
	_, ok := s.Options[name]
	_, okl := s.Lists[name]
	return ok || okl
}

// Client issues uci calls. All staged changes live in the caller's rpcd
// session until committed by Manager.Transact.
type Client struct{ c Caller }

func NewClient(c Caller) *Client { return &Client{c: c} }

func decodeSection(config string, raw map[string]json.RawMessage) (*Section, error) {
	s := &Section{Config: config, Options: map[string]string{}, Lists: map[string][]string{}}
	for k, v := range raw {
		switch k {
		case ".name":
			_ = json.Unmarshal(v, &s.Name)
		case ".type":
			_ = json.Unmarshal(v, &s.Type)
		case ".anonymous":
			_ = json.Unmarshal(v, &s.Anonymous)
		case ".index":
			_ = json.Unmarshal(v, &s.Index)
		default:
			if strings.HasPrefix(k, ".") {
				continue
			}
			if len(v) > 0 && v[0] == '[' {
				var items []string
				if err := json.Unmarshal(v, &items); err != nil {
					return nil, fmt.Errorf("uci: %s option %s: %w", config, k, err)
				}
				s.Lists[k] = items
				continue
			}
			var str string
			if err := json.Unmarshal(v, &str); err != nil {
				return nil, fmt.Errorf("uci: %s option %s: %w", config, k, err)
			}
			s.Options[k] = str
		}
	}
	return s, nil
}

// Get returns one section. Missing sections yield an error for which
// ubus.IsNotFound is true.
func (u *Client) Get(ctx context.Context, config, section string) (*Section, error) {
	var out struct {
		Values map[string]json.RawMessage `json:"values"`
	}
	if err := u.c.Call(ctx, "uci", "get", map[string]any{"config": config, "section": section}, &out); err != nil {
		return nil, err
	}
	// rpcd answers a missing section with status 0 and no data.
	if len(out.Values) == 0 {
		return nil, &ubus.Error{Object: "uci", Method: "get", Status: ubus.StatusNotFound}
	}
	return decodeSection(config, out.Values)
}

// List returns all sections of a config (optionally only of one type), in file order.
func (u *Client) List(ctx context.Context, config, typ string) ([]*Section, error) {
	args := map[string]any{"config": config}
	if typ != "" {
		args["type"] = typ
	}
	var out struct {
		Values map[string]map[string]json.RawMessage `json:"values"`
	}
	if err := u.c.Call(ctx, "uci", "get", args, &out); err != nil {
		return nil, err
	}
	secs := make([]*Section, 0, len(out.Values))
	for _, raw := range out.Values {
		s, err := decodeSection(config, raw)
		if err != nil {
			return nil, err
		}
		secs = append(secs, s)
	}
	sort.Slice(secs, func(i, j int) bool { return secs[i].Index < secs[j].Index })
	return secs, nil
}

var extendedRef = regexp.MustCompile(`^@([A-Za-z0-9_-]+)\[(-?\d+)\]$`)

// Resolve turns a section reference into a real section name. It accepts a
// plain name or the extended "@type[index]" syntax (negative index counts
// from the end), which is how anonymous sections are imported.
func (u *Client) Resolve(ctx context.Context, config, ref string) (string, error) {
	m := extendedRef.FindStringSubmatch(ref)
	if m == nil {
		return ref, nil
	}
	secs, err := u.List(ctx, config, m[1])
	if err != nil {
		return "", err
	}
	idx, _ := strconv.Atoi(m[2])
	if idx < 0 {
		idx += len(secs)
	}
	if idx < 0 || idx >= len(secs) {
		return "", &ubus.Error{Object: "uci", Method: "get", Status: ubus.StatusNotFound}
	}
	return secs[idx].Name, nil
}

// Values is the payload of Add/Set: each value is a string or []string.
type Values map[string]any

// Add creates a named section of the given type.
func (u *Client) Add(ctx context.Context, config, typ, name string, values Values) error {
	args := map[string]any{"config": config, "type": typ, "name": name}
	if len(values) > 0 {
		args["values"] = values
	}
	err := u.c.Call(ctx, "uci", "add", args, nil)
	if ubus.IsNotFound(err) {
		return fmt.Errorf("/etc/config/%s does not exist; install the package that provides it or create an empty file (e.g. with openwrt_file): %w", config, err)
	}
	return err
}

// Set merges values into an existing section. Lists are replaced wholesale.
func (u *Client) Set(ctx context.Context, config, section string, values Values) error {
	if len(values) == 0 {
		return nil
	}
	return u.c.Call(ctx, "uci", "set", map[string]any{"config": config, "section": section, "values": values}, nil)
}

// DeleteOptions removes options (scalar or list) from a section.
func (u *Client) DeleteOptions(ctx context.Context, config, section string, options []string) error {
	if len(options) == 0 {
		return nil
	}
	return u.c.Call(ctx, "uci", "delete", map[string]any{"config": config, "section": section, "options": options}, nil)
}

// DeleteSection removes a whole section.
func (u *Client) DeleteSection(ctx context.Context, config, section string) error {
	return u.c.Call(ctx, "uci", "delete", map[string]any{"config": config, "section": section}, nil)
}

// Rename renames a section (this is also how anonymous sections get a name).
func (u *Client) Rename(ctx context.Context, config, section, name string) error {
	return u.c.Call(ctx, "uci", "rename", map[string]any{"config": config, "section": section, "name": name}, nil)
}

// Order moves the given sections to the front of the config in that order.
func (u *Client) Order(ctx context.Context, config string, sections []string) error {
	return u.c.Call(ctx, "uci", "order", map[string]any{"config": config, "sections": sections}, nil)
}

// Revert drops staged changes of one config in the current session.
func (u *Client) Revert(ctx context.Context, config string) error {
	err := u.c.Call(ctx, "uci", "revert", map[string]any{"config": config}, nil)
	if ubus.IsNotFound(err) {
		return nil
	}
	return err
}

// HasChanges reports whether the session has staged changes for any of configs.
func (u *Client) HasChanges(ctx context.Context, configs []string) (bool, error) {
	for _, cfg := range configs {
		var out struct {
			Changes []json.RawMessage `json:"changes"`
		}
		err := u.c.Call(ctx, "uci", "changes", map[string]any{"config": cfg}, &out)
		if ubus.IsNotFound(err) || ubus.HasStatus(err, ubus.StatusNoData) {
			continue
		}
		if err != nil {
			return false, err
		}
		if len(out.Changes) > 0 {
			return true, nil
		}
	}
	return false, nil
}
