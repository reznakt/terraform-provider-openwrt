package uci

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/reznakt/terraform-provider-openwrt/internal/ubus"
)

// Manager serializes config changes. rpcd supports only one pending
// rollback-protected apply at a time and Terraform runs resources in
// parallel, so every mutation goes through Transact under one lock.
type Manager struct {
	c   Caller
	UCI *Client

	// Rollback enables `uci apply` with automatic rollback: the router
	// reverts the change unless it is confirmed over a fresh connection
	// within Timeout.
	Rollback bool
	Timeout  time.Duration
	// Holdoff is how long to wait after apply before trying to confirm, so
	// the reload has actually taken effect (LuCI uses 4s).
	Holdoff time.Duration
	// Grace is how long past Timeout to keep waiting for the router to come
	// back after a rollback (services restart with the old config).
	Grace time.Duration
	// Sleep is replaceable in tests.
	Sleep func(context.Context, time.Duration) error

	mu sync.Mutex
}

func NewManager(c Caller) *Manager {
	return &Manager{
		c:        c,
		UCI:      NewClient(c),
		Rollback: true,
		Timeout:  30 * time.Second,
		Holdoff:  4 * time.Second,
		Grace:    60 * time.Second,
		Sleep:    sleepCtx,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ErrRolledBack means the router reverted a change because it could not be
// confirmed in time, most likely because the change cut off access.
var ErrRolledBack = errors.New("the router rolled the change back because it could not be confirmed in time; the new configuration probably broke access to the router (address, firewall, uhttpd or rpcd settings)")

// Transact stages changes via stage, then commits and reloads the touched
// configs. On any staging error the staged changes are reverted.
func (m *Manager) Transact(ctx context.Context, configs []string, stage func(ctx context.Context, u *Client) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	revert := func() {
		for _, cfg := range configs {
			_ = m.UCI.Revert(context.WithoutCancel(ctx), cfg)
		}
	}
	// Leftovers from an interrupted run would otherwise be applied too.
	revert()
	if err := stage(ctx, m.UCI); err != nil {
		revert()
		return err
	}
	changed, err := m.UCI.HasChanges(ctx, configs)
	if err != nil {
		revert()
		return err
	}
	if !changed {
		return nil
	}
	if !m.Rollback {
		return m.commit(ctx, configs)
	}
	return m.applyConfirm(ctx)
}

func (m *Manager) commit(ctx context.Context, configs []string) error {
	for _, cfg := range configs {
		if err := m.c.Call(ctx, "uci", "commit", map[string]any{"config": cfg}, nil); err != nil {
			return fmt.Errorf("commit %s: %w", cfg, err)
		}
	}
	// reload_config is what `uci commit && reload_config` does on the shell.
	if err := m.c.Call(ctx, "uci", "reload_config", nil, nil); err != nil && !ubus.HasStatus(err, ubus.StatusMethodNotFound) {
		return fmt.Errorf("reload_config: %w", err)
	}
	return nil
}

func (m *Manager) applyConfirm(ctx context.Context) error {
	secs := int(m.Timeout / time.Second)
	if secs < 5 {
		secs = 5
	}
	err := m.c.Call(ctx, "uci", "apply", map[string]any{"rollback": true, "timeout": secs}, nil)
	if err != nil {
		return fmt.Errorf("apply: %w", err)
	}
	if err := m.Sleep(ctx, m.Holdoff); err != nil {
		return err
	}

	// Confirming over a new connection is the reachability check: if the new
	// config broke access, confirm never gets through and rpcd rolls back
	// when its timer fires. rpcd has the final word either way (confirm
	// succeeds, or answers NO_DATA once it has reverted), so keep trying until
	// the router is reachable again rather than guessing from a local clock.
	deadline := time.Now().Add(time.Duration(secs)*time.Second + m.Grace)
	for {
		m.c.Reconnect()
		err = m.c.Call(ctx, "uci", "confirm", nil, nil)
		switch {
		case err == nil:
			return nil
		case ubus.HasStatus(err, ubus.StatusNoData):
			// No pending apply any more: the timer already fired.
			return ErrRolledBack
		case ctx.Err() != nil:
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return &UnreachableError{After: time.Duration(secs)*time.Second + m.Grace, Last: err}
		}
		if err := m.Sleep(ctx, time.Second); err != nil {
			return err
		}
	}
}

// UnreachableError means the router neither confirmed the change nor came
// back after its rollback timer should have fired.
type UnreachableError struct {
	After time.Duration
	Last  error
}

func (e *UnreachableError) Error() string {
	return fmt.Sprintf("the router has been unreachable for %s after applying; it should have rolled the change back by now, check it manually (last error: %v)", e.After, e.Last)
}
