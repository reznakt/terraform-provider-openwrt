// Package ubustest provides an in-memory fake of uhttpd-mod-ubus + rpcd that
// is faithful enough for unit and in-process acceptance tests: sessions,
// per-session staged uci changes, apply/confirm/rollback, file and rc.
package ubustest

import (
	"crypto/md5" //nolint:gosec // mirrors rpcd file.md5
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	stOK               = 0
	stInvalidArgument  = 2
	stMethodNotFound   = 3
	stNotFound         = 4
	stNoData           = 5
	stPermissionDenied = 6
)

// Section is a fake UCI section.
type Section struct {
	Name      string
	Type      string
	Anonymous bool
	Values    map[string]any // string or []string
}

func (s *Section) clone() *Section {
	c := &Section{Name: s.Name, Type: s.Type, Anonymous: s.Anonymous, Values: map[string]any{}}
	for k, v := range s.Values {
		if l, ok := v.([]string); ok {
			v = append([]string(nil), l...)
		}
		c.Values[k] = v
	}
	return c
}

type config []*Section

func (c config) clone() config {
	out := make(config, len(c))
	for i, s := range c {
		out[i] = s.clone()
	}
	return out
}

func (c config) find(name string) (int, *Section) {
	for i, s := range c {
		if s.Name == name {
			return i, s
		}
	}
	return -1, nil
}

// File is a fake file.
type File struct {
	Data []byte
	Mode int
}

// ExecFunc handles file.exec calls.
type ExecFunc func(command string, params []string) (code int, stdout, stderr string)

// Server is the fake router.
type Server struct {
	*httptest.Server

	mu        sync.Mutex
	users     map[string]string
	sessions  map[string]bool
	committed map[string]config
	staged    map[string]map[string]config // session -> config -> working copy
	pending   map[string]config            // pre-apply snapshot while a rollback is pending
	pendingBy string
	anonSeq   int
	sidSeq    int

	Files    map[string]*File
	Services map[string]map[string]bool // name -> {enabled, running}
	Exec     ExecFunc
	Board    map[string]any

	// RollbackOnApply makes the next rollback-protected apply revert itself
	// immediately, as if the new config had cut off access.
	RollbackOnApply atomic.Bool
	// Calls counts calls per "object.method".
	Calls sync.Map
	// Log records every raw request body, for secret-leak assertions.
	Log []string
}

// New starts a fake with one login (username/password).
func New(username, password string) *Server {
	f := &Server{
		users:     map[string]string{username: password},
		sessions:  map[string]bool{},
		committed: map[string]config{},
		staged:    map[string]map[string]config{},
		Files:     map[string]*File{},
		Services:  map[string]map[string]bool{},
		Board: map[string]any{
			"hostname": "OpenWrt", "model": "Fake Router", "board_name": "fake,router",
			"kernel": "6.6.0", "system": "fake", "rootfs_type": "squashfs",
			"release": map[string]any{"distribution": "OpenWrt", "version": "24.10.5", "revision": "r0", "target": "x86/64", "description": "OpenWrt 24.10.5"},
		},
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.handle))
	return f
}

// Endpoint is the /ubus URL of the fake.
func (f *Server) Endpoint() string { return f.URL + "/ubus" }

// AddSection seeds a committed section. An empty name creates an anonymous one.
func (f *Server) AddSection(cfg, typ, name string, values map[string]any) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.committed[cfg]
	n := f.addLocked(&c, typ, name, values)
	f.committed[cfg] = c
	return n
}

// Section returns a copy of a committed section, or nil.
func (f *Server) Section(cfg, name string) *Section {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, s := f.committed[cfg].find(name); s != nil {
		return s.clone()
	}
	return nil
}

// SectionNames returns the committed section names of cfg in order.
func (f *Server) SectionNames(cfg string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var names []string
	for _, s := range f.committed[cfg] {
		names = append(names, s.Name)
	}
	return names
}

// SetOption changes a committed option behind the provider's back (drift).
// A nil value deletes the option.
func (f *Server) SetOption(cfg, section, key string, value any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, s := f.committed[cfg].find(section); s != nil {
		if value == nil {
			delete(s.Values, key)
		} else {
			s.Values[key] = normalize(value)
		}
	}
}

// RawLog returns every request body received so far.
func (f *Server) RawLog() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.Log, "\n")
}

// ExpireSessions drops all sessions, as if rpcd restarted.
func (f *Server) ExpireSessions() {
	f.mu.Lock()
	f.sessions = map[string]bool{}
	f.mu.Unlock()
}

func (f *Server) CallCount(key string) int64 {
	v, ok := f.Calls.Load(key)
	if !ok {
		return 0
	}
	return v.(*atomic.Int64).Load()
}

func (f *Server) addLocked(c *config, typ, name string, values map[string]any) string {
	s := &Section{Name: name, Type: typ, Values: map[string]any{}}
	if name == "" {
		f.anonSeq++
		s.Name = fmt.Sprintf("cfg%02x%04x", len(*c), f.anonSeq)
		s.Anonymous = true
	}
	for k, v := range values {
		s.Values[k] = normalize(v)
	}
	*c = append(*c, s)
	return s.Name
}

func normalize(v any) any {
	switch t := v.(type) {
	case []any:
		out := make([]string, len(t))
		for i, x := range t {
			out[i] = fmt.Sprint(x)
		}
		return out
	case []string:
		return append([]string(nil), t...)
	default:
		return fmt.Sprint(t)
	}
}

type rpcReq struct {
	ID     json.RawMessage   `json:"id"`
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
}

func (f *Server) handle(w http.ResponseWriter, r *http.Request) {
	var req rpcReq
	body := new(strings.Builder)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&req); err != nil || req.Method != "call" || len(req.Params) != 4 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	raw, _ := json.Marshal(req)
	body.Write(raw)

	var sid, obj, method string
	_ = json.Unmarshal(req.Params[0], &sid)
	_ = json.Unmarshal(req.Params[1], &obj)
	_ = json.Unmarshal(req.Params[2], &method)
	var args map[string]json.RawMessage
	_ = json.Unmarshal(req.Params[3], &args)

	key := obj + "." + method
	v, _ := f.Calls.LoadOrStore(key, new(atomic.Int64))
	v.(*atomic.Int64).Add(1)

	f.mu.Lock()
	f.Log = append(f.Log, body.String())
	if (obj != "session" || method != "login") && !f.sessions[sid] {
		f.mu.Unlock()
		writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32002, "message": "Access denied"}})
		return
	}
	status, data := f.dispatch(sid, obj, method, args)
	f.mu.Unlock()

	result := []any{status}
	if data != nil {
		result = append(result, data)
	}
	writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func str(args map[string]json.RawMessage, k string) string {
	var s string
	_ = json.Unmarshal(args[k], &s)
	return s
}

func (f *Server) dispatch(sid, obj, method string, args map[string]json.RawMessage) (int, any) {
	switch obj {
	case "session":
		if method == "login" {
			if pw, ok := f.users[str(args, "username")]; !ok || pw != str(args, "password") {
				return stPermissionDenied, nil
			}
			f.sidSeq++
			id := fmt.Sprintf("%032x", f.sidSeq)
			f.sessions[id] = true
			return stOK, map[string]any{"ubus_rpc_session": id, "timeout": 300}
		}
	case "uci":
		return f.uci(sid, method, args)
	case "file":
		return f.file(method, args)
	case "rc":
		return f.rc(method, args)
	case "network.interface":
		if method == "dump" {
			return stOK, map[string]any{"interface": []any{map[string]any{
				"interface": "lan", "up": true, "pending": false, "available": true, "uptime": 42,
				"proto": "static", "device": "br-lan", "l3_device": "br-lan",
				"ipv4-address": []any{map[string]any{"address": "192.168.1.1", "mask": 24}},
				"ipv6-address": []any{}, "ipv6-prefix": []any{}, "dns-server": []any{},
			}}}
		}
	case "network.wireless":
		if method == "status" {
			return stOK, map[string]any{"radio0": map[string]any{
				"up": true, "pending": false, "disabled": false,
				"interfaces": []any{map[string]any{"section": "default_radio0", "ifname": "phy0-ap0",
					"config": map[string]any{"ssid": "OpenWrt", "mode": "ap", "network": []string{"lan"}, "key": "fake-psk-never-exported"}}},
			}}
		}
	case "luci-rpc":
		if method == "getDHCPLeases" {
			return stOK, map[string]any{
				"dhcp_leases":  []any{map[string]any{"hostname": "laptop", "ipaddr": "192.168.1.100", "macaddr": "aa:bb:cc:00:00:01", "expires": 3600}},
				"dhcp6_leases": []any{},
			}
		}
	case "system":
		switch method {
		case "board":
			return stOK, f.Board
		case "info":
			return stOK, map[string]any{"uptime": 1234, "localtime": 1700000000, "load": []int{0, 0, 0},
				"memory": map[string]any{"total": 256 << 20, "free": 128 << 20}}
		}
	}
	return stMethodNotFound, nil
}

// view returns the session's working copy of cfg (creating it lazily).
func (f *Server) view(sid, cfg string) config {
	if st, ok := f.staged[sid][cfg]; ok {
		return st
	}
	return f.committed[cfg]
}

func (f *Server) stage(sid, cfg string) *config {
	if f.staged[sid] == nil {
		f.staged[sid] = map[string]config{}
	}
	if _, ok := f.staged[sid][cfg]; !ok {
		f.staged[sid][cfg] = f.committed[cfg].clone()
	}
	c := f.staged[sid][cfg]
	return &c
}

func (f *Server) save(sid, cfg string, c *config) { f.staged[sid][cfg] = *c }

func sectionJSON(s *Section, idx int) map[string]any {
	m := map[string]any{".name": s.Name, ".type": s.Type, ".anonymous": s.Anonymous, ".index": idx}
	for k, v := range s.Values {
		m[k] = v
	}
	return m
}

func (f *Server) uci(sid, method string, args map[string]json.RawMessage) (int, any) {
	cfg := str(args, "config")
	section := str(args, "section")
	switch method {
	case "get":
		c := f.view(sid, cfg)
		if c == nil {
			return stNotFound, nil
		}
		if section != "" {
			i, s := c.find(section)
			if s == nil {
				return stOK, nil // real rpcd: status 0, no data
			}
			if opt := str(args, "option"); opt != "" {
				v, ok := s.Values[opt]
				if !ok {
					return stNotFound, nil
				}
				return stOK, map[string]any{"value": v}
			}
			return stOK, map[string]any{"values": sectionJSON(s, i)}
		}
		typ := str(args, "type")
		out := map[string]any{}
		for i, s := range c {
			if typ == "" || s.Type == typ {
				out[s.Name] = sectionJSON(s, i)
			}
		}
		return stOK, map[string]any{"values": out}

	case "add":
		c := f.stage(sid, cfg)
		name := str(args, "name")
		if name != "" {
			if _, s := c.find(name); s != nil {
				return stInvalidArgument, nil
			}
		}
		var values map[string]any
		_ = json.Unmarshal(args["values"], &values)
		n := f.addLocked(c, str(args, "type"), name, values)
		f.save(sid, cfg, c)
		return stOK, map[string]any{"section": n}

	case "set":
		c := f.stage(sid, cfg)
		_, s := c.find(section)
		if s == nil {
			return stNotFound, nil
		}
		var values map[string]any
		_ = json.Unmarshal(args["values"], &values)
		for k, v := range values {
			s.Values[k] = normalize(v)
		}
		f.save(sid, cfg, c)
		return stOK, nil

	case "delete":
		c := f.stage(sid, cfg)
		i, s := c.find(section)
		if s == nil {
			return stNotFound, nil
		}
		var opts []string
		_ = json.Unmarshal(args["options"], &opts)
		if o := str(args, "option"); o != "" {
			opts = append(opts, o)
		}
		if len(opts) == 0 {
			*c = append((*c)[:i], (*c)[i+1:]...)
		}
		for _, o := range opts {
			if _, ok := s.Values[o]; !ok {
				return stNotFound, nil // real rpcd rejects deleting missing options
			}
			delete(s.Values, o)
		}
		f.save(sid, cfg, c)
		return stOK, nil

	case "rename":
		c := f.stage(sid, cfg)
		_, s := c.find(section)
		if s == nil {
			return stNotFound, nil
		}
		s.Name = str(args, "name")
		s.Anonymous = false
		f.save(sid, cfg, c)
		return stOK, nil

	case "order":
		c := f.stage(sid, cfg)
		var names []string
		_ = json.Unmarshal(args["sections"], &names)
		var head, tail config
		for _, n := range names {
			if _, s := c.find(n); s != nil {
				head = append(head, s)
			}
		}
		for _, s := range *c {
			found := false
			for _, h := range head {
				found = found || h == s
			}
			if !found {
				tail = append(tail, s)
			}
		}
		*c = append(head, tail...)
		f.save(sid, cfg, c)
		return stOK, nil

	case "changes":
		st, ok := f.staged[sid][cfg]
		if !ok || equalConfig(st, f.committed[cfg]) {
			return stOK, map[string]any{"changes": []any{}}
		}
		return stOK, map[string]any{"changes": []any{[]string{"set", "x"}}}

	case "revert":
		delete(f.staged[sid], cfg)
		return stOK, nil

	case "commit":
		if st, ok := f.staged[sid][cfg]; ok {
			f.committed[cfg] = st
			delete(f.staged[sid], cfg)
		}
		return stOK, nil

	case "reload_config":
		return stOK, nil

	case "apply":
		if f.pending != nil {
			return 9, nil
		}
		var rollback bool
		_ = json.Unmarshal(args["rollback"], &rollback)
		snapshot := map[string]config{}
		for name, st := range f.staged[sid] {
			snapshot[name] = f.committed[name]
			f.committed[name] = st
		}
		f.staged[sid] = nil
		if rollback {
			f.pending = snapshot
			f.pendingBy = sid
			if f.RollbackOnApply.Swap(false) {
				f.rollbackLocked()
			}
		}
		return stOK, nil

	case "confirm":
		if f.pending == nil {
			return stNoData, nil
		}
		if f.pendingBy != sid {
			return stPermissionDenied, nil
		}
		f.pending = nil
		return stOK, nil

	case "rollback":
		if f.pending == nil {
			return stNoData, nil
		}
		f.rollbackLocked()
		return stOK, nil
	}
	return stMethodNotFound, nil
}

func (f *Server) rollbackLocked() {
	for name, c := range f.pending {
		f.committed[name] = c
	}
	f.pending = nil
}

func equalConfig(a, b config) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

func (f *Server) file(method string, args map[string]json.RawMessage) (int, any) {
	path := str(args, "path")
	var b64 bool
	_ = json.Unmarshal(args["base64"], &b64)
	switch method {
	case "read":
		fl, ok := f.Files[path]
		if !ok {
			return stNotFound, nil
		}
		if b64 {
			return stOK, map[string]any{"data": base64.StdEncoding.EncodeToString(fl.Data)}
		}
		return stOK, map[string]any{"data": string(fl.Data)}
	case "write":
		data := []byte(str(args, "data"))
		if b64 {
			d, err := base64.StdEncoding.DecodeString(string(data))
			if err != nil {
				return stInvalidArgument, nil
			}
			data = d
		}
		mode := 0o644
		if m, ok := args["mode"]; ok {
			_ = json.Unmarshal(m, &mode)
		}
		var appendMode bool
		_ = json.Unmarshal(args["append"], &appendMode)
		if old, ok := f.Files[path]; ok && appendMode {
			data = append(old.Data, data...)
		}
		f.Files[path] = &File{Data: data, Mode: mode}
		return stOK, nil
	case "stat":
		fl, ok := f.Files[path]
		if !ok {
			return stNotFound, nil
		}
		return stOK, map[string]any{"path": path, "type": "file", "size": len(fl.Data), "mode": fl.Mode | 0o100000}
	case "md5":
		fl, ok := f.Files[path]
		if !ok {
			return stNotFound, nil
		}
		sum := md5.Sum(fl.Data) //nolint:gosec
		return stOK, map[string]any{"md5": hex.EncodeToString(sum[:])}
	case "remove":
		if _, ok := f.Files[path]; !ok {
			return stNotFound, nil
		}
		delete(f.Files, path)
		return stOK, nil
	case "list":
		var entries []map[string]any
		for p := range f.Files {
			if strings.HasPrefix(p, strings.TrimSuffix(path, "/")+"/") {
				entries = append(entries, map[string]any{"name": p[len(strings.TrimSuffix(path, "/"))+1:], "type": "file"})
			}
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i]["name"].(string) < entries[j]["name"].(string) })
		return stOK, map[string]any{"entries": entries}
	case "exec":
		if f.Exec == nil {
			return stPermissionDenied, nil
		}
		var params []string
		_ = json.Unmarshal(args["params"], &params)
		code, stdout, stderr := f.Exec(str(args, "command"), params)
		return stOK, map[string]any{"code": code, "stdout": stdout, "stderr": stderr}
	}
	return stMethodNotFound, nil
}

func (f *Server) rc(method string, args map[string]json.RawMessage) (int, any) {
	name := str(args, "name")
	switch method {
	case "list":
		out := map[string]any{}
		for n, st := range f.Services {
			if name == "" || n == name {
				out[n] = map[string]any{"start": 50, "stop": 50, "enabled": st["enabled"], "running": st["running"]}
			}
		}
		return stOK, out
	case "init":
		st, ok := f.Services[name]
		if !ok {
			return stNotFound, nil
		}
		switch str(args, "action") {
		case "enable":
			st["enabled"] = true
		case "disable":
			st["enabled"] = false
		case "start", "restart", "reload":
			st["running"] = true
		case "stop":
			st["running"] = false
		default:
			return stInvalidArgument, nil
		}
		return stOK, nil
	}
	return stMethodNotFound, nil
}
