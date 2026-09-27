// Package ubus is a minimal client for OpenWrt's ubus JSON-RPC endpoint as
// exposed by uhttpd-mod-ubus (usually https://<router>/ubus).
package ubus

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// NullSession is the anonymous session id used for session.login.
const NullSession = "00000000000000000000000000000000"

// jsonrpcAccessDenied is returned by uhttpd when the session id is unknown or
// expired, before the call ever reaches ubus.
const jsonrpcAccessDenied = -32002

// Config configures a Client.
type Config struct {
	// Endpoint is the full URL of the ubus endpoint, e.g. https://192.168.1.1/ubus.
	Endpoint string
	Username string
	Password string
	// SessionTimeout is the rpcd session timeout requested at login.
	SessionTimeout time.Duration
	// RequestTimeout bounds a single HTTP round trip.
	RequestTimeout     time.Duration
	InsecureSkipVerify bool
	// CACertPEM, when set, replaces the system roots for TLS verification.
	CACertPEM string
	// Log, when set, receives one entry per call with object, method, HTTP
	// status and duration. Call arguments and replies are never passed to it.
	Log func(ctx context.Context, msg string, fields map[string]any)
}

// Client performs authenticated ubus calls. It is safe for concurrent use.
type Client struct {
	cfg       Config
	transport *http.Transport
	http      *http.Client
	nextID    atomic.Int64

	mu  sync.Mutex
	sid string
}

// New creates a client. It does not contact the router; the first Call logs in.
func New(cfg Config) (*Client, error) {
	if cfg.Endpoint == "" {
		return nil, errors.New("ubus: endpoint is required")
	}
	if cfg.SessionTimeout == 0 {
		cfg.SessionTimeout = 5 * time.Minute
	}
	if cfg.RequestTimeout == 0 {
		cfg.RequestTimeout = 30 * time.Second
	}
	tlsCfg := &tls.Config{InsecureSkipVerify: cfg.InsecureSkipVerify} //nolint:gosec // opt-in for self-signed router certs
	if cfg.CACertPEM != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(cfg.CACertPEM)) {
			return nil, errors.New("ubus: ca_cert contains no PEM certificates")
		}
		tlsCfg.RootCAs = pool
	}
	tr := &http.Transport{TLSClientConfig: tlsCfg, MaxIdleConnsPerHost: 4}
	return &Client{
		cfg:       cfg,
		transport: tr,
		http:      &http.Client{Transport: tr, Timeout: cfg.RequestTimeout},
	}, nil
}

type request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type response struct {
	Result []json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// rpcError is a JSON-RPC level error (as opposed to a ubus status code).
type rpcError struct {
	code int
	msg  string
}

func (e *rpcError) Error() string { return fmt.Sprintf("ubus: json-rpc error %d: %s", e.code, e.msg) }

// Call invokes object.method with args and decodes the reply data into out
// (which may be nil). It logs in on first use and re-logs in once when the
// session has expired.
func (c *Client) Call(ctx context.Context, object, method string, args any, out any) error {
	sid, err := c.session(ctx)
	if err != nil {
		return err
	}
	err = c.call(ctx, sid, object, method, args, out)
	var re *rpcError
	if errors.As(err, &re) && re.code == jsonrpcAccessDenied {
		c.invalidate(sid)
		if sid, err = c.session(ctx); err != nil {
			return err
		}
		err = c.call(ctx, sid, object, method, args, out)
	}
	return err
}

// Reconnect drops idle keep-alive connections so the next call opens a fresh
// TCP/TLS connection. Used after a config apply to prove reachability.
func (c *Client) Reconnect() { c.transport.CloseIdleConnections() }

func (c *Client) session(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sid != "" {
		return c.sid, nil
	}
	var out struct {
		Session string `json:"ubus_rpc_session"`
	}
	args := map[string]any{
		"username": c.cfg.Username,
		"password": c.cfg.Password,
		"timeout":  int(c.cfg.SessionTimeout / time.Second),
	}
	if err := c.call(ctx, NullSession, "session", "login", args, &out); err != nil {
		var ue *Error
		if errors.As(err, &ue) && ue.Status == StatusPermissionDenied {
			return "", fmt.Errorf("ubus: login as %q rejected: wrong username or password", c.cfg.Username)
		}
		return "", fmt.Errorf("ubus: login: %w", err)
	}
	if out.Session == "" {
		return "", errors.New("ubus: login returned no session")
	}
	c.sid = out.Session
	return c.sid, nil
}

func (c *Client) invalidate(sid string) {
	c.mu.Lock()
	if c.sid == sid {
		c.sid = ""
	}
	c.mu.Unlock()
}

func (c *Client) call(ctx context.Context, sid, object, method string, args any, out any) error {
	if args == nil {
		args = map[string]any{}
	}
	body, err := json.Marshal(request{
		JSONRPC: "2.0",
		ID:      c.nextID.Add(1),
		Method:  "call",
		Params:  []any{sid, object, method, args},
	})
	if err != nil {
		return fmt.Errorf("ubus: encode %s.%s: %w", object, method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return &Error{Object: object, Method: method, Status: StatusConnectionFailed, cause: err}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return &Error{Object: object, Method: method, Status: StatusConnectionFailed, cause: err}
	}
	if c.cfg.Log != nil {
		c.cfg.Log(ctx, "ubus call", map[string]any{"object": object, "method": method, "http_status": resp.StatusCode, "duration_ms": time.Since(start).Milliseconds()})
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ubus: %s.%s: HTTP %s", object, method, resp.Status)
	}

	var r response
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("ubus: %s.%s: malformed reply: %w", object, method, err)
	}
	if r.Error != nil {
		return &rpcError{code: r.Error.Code, msg: r.Error.Message}
	}
	if len(r.Result) == 0 {
		return fmt.Errorf("ubus: %s.%s: empty result", object, method)
	}
	var status Status
	if err := json.Unmarshal(r.Result[0], &status); err != nil {
		return fmt.Errorf("ubus: %s.%s: malformed status: %w", object, method, err)
	}
	if status != StatusOK {
		return &Error{Object: object, Method: method, Status: status}
	}
	if out != nil && len(r.Result) > 1 {
		if err := json.Unmarshal(r.Result[1], out); err != nil {
			return fmt.Errorf("ubus: %s.%s: decode reply: %w", object, method, err)
		}
	}
	return nil
}
