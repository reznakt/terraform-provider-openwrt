package uci_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/reznakt/terraform-provider-openwrt/internal/ubus"
	"github.com/reznakt/terraform-provider-openwrt/internal/ubustest"
	"github.com/reznakt/terraform-provider-openwrt/internal/uci"
)

func setup(t *testing.T) (*ubustest.Server, *uci.Manager) {
	t.Helper()
	f := ubustest.New("root", "s3cret-pw")
	t.Cleanup(f.Close)
	c, err := ubus.New(ubus.Config{Endpoint: f.Endpoint(), Username: "root", Password: "s3cret-pw"})
	if err != nil {
		t.Fatal(err)
	}
	m := uci.NewManager(c)
	m.Sleep = func(context.Context, time.Duration) error { return nil }
	return f, m
}

func TestLoginFailure(t *testing.T) {
	f := ubustest.New("root", "right")
	defer f.Close()
	c, _ := ubus.New(ubus.Config{Endpoint: f.Endpoint(), Username: "root", Password: "wrong"})
	err := c.Call(context.Background(), "system", "board", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "wrong username or password") {
		t.Fatalf("want login error, got %v", err)
	}
	if strings.Contains(err.Error(), "wrong\"") || strings.Contains(err.Error(), "right") {
		t.Fatalf("error leaks password: %v", err)
	}
}

func TestSessionExpiryRelogin(t *testing.T) {
	f, m := setup(t)
	ctx := context.Background()
	f.AddSection("system", "system", "", map[string]any{"hostname": "a"})
	if _, err := m.UCI.List(ctx, "system", "system"); err != nil {
		t.Fatal(err)
	}
	f.ExpireSessions()
	if _, err := m.UCI.List(ctx, "system", "system"); err != nil {
		t.Fatalf("expected transparent re-login, got %v", err)
	}
	if n := f.CallCount("session.login"); n != 2 {
		t.Fatalf("want 2 logins, got %d", n)
	}
}

func TestGetDecodesListsAndResolve(t *testing.T) {
	f, m := setup(t)
	ctx := context.Background()
	f.AddSection("firewall", "zone", "", map[string]any{"name": "lan", "network": []string{"lan"}})
	anon := f.AddSection("firewall", "zone", "", map[string]any{"name": "wan", "network": []string{"wan", "wan6"}, "masq": "1"})

	name, err := m.UCI.Resolve(ctx, "firewall", "@zone[-1]")
	if err != nil || name != anon {
		t.Fatalf("resolve: %q %v", name, err)
	}
	s, err := m.UCI.Get(ctx, "firewall", name)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Anonymous || s.Type != "zone" || s.Options["masq"] != "1" || len(s.Lists["network"]) != 2 {
		t.Fatalf("bad section %+v", s)
	}
	if _, err := m.UCI.Resolve(ctx, "firewall", "@zone[5]"); !ubus.IsNotFound(err) {
		t.Fatalf("want not found, got %v", err)
	}
}

func TestTransactCommitsWithConfirm(t *testing.T) {
	f, m := setup(t)
	ctx := context.Background()
	err := m.Transact(ctx, []string{"dhcp"}, func(ctx context.Context, u *uci.Client) error {
		return u.Add(ctx, "dhcp", "host", "printer", uci.Values{"mac": "aa:bb:cc:dd:ee:ff", "ip": "192.168.1.5"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if s := f.Section("dhcp", "printer"); s == nil || s.Values["ip"] != "192.168.1.5" {
		t.Fatalf("not committed: %+v", s)
	}
	if f.CallCount("uci.apply") != 1 || f.CallCount("uci.confirm") != 1 {
		t.Fatalf("expected one apply+confirm")
	}
}

func TestTransactNoChangesSkipsApply(t *testing.T) {
	f, m := setup(t)
	f.AddSection("dhcp", "host", "x", map[string]any{"ip": "1"})
	err := m.Transact(context.Background(), []string{"dhcp"}, func(ctx context.Context, u *uci.Client) error {
		return u.Set(ctx, "dhcp", "x", uci.Values{"ip": "1"})
	})
	if err != nil || f.CallCount("uci.apply") != 0 {
		t.Fatalf("err=%v applies=%d", err, f.CallCount("uci.apply"))
	}
}

func TestTransactRollback(t *testing.T) {
	f, m := setup(t)
	f.AddSection("network", "interface", "lan", map[string]any{"ipaddr": "192.168.1.1"})
	f.RollbackOnApply.Store(true)
	err := m.Transact(context.Background(), []string{"network"}, func(ctx context.Context, u *uci.Client) error {
		return u.Set(ctx, "network", "lan", uci.Values{"ipaddr": "10.9.9.9"})
	})
	if !errors.Is(err, uci.ErrRolledBack) {
		t.Fatalf("want ErrRolledBack, got %v", err)
	}
	if got := f.Section("network", "lan").Values["ipaddr"]; got != "192.168.1.1" {
		t.Fatalf("not rolled back: %v", got)
	}
}

func TestTransactStageErrorReverts(t *testing.T) {
	f, m := setup(t)
	boom := errors.New("boom")
	err := m.Transact(context.Background(), []string{"dhcp"}, func(ctx context.Context, u *uci.Client) error {
		if err := u.Add(ctx, "dhcp", "host", "h", nil); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	// A following no-op transaction must not apply the abandoned add.
	if err := m.Transact(context.Background(), []string{"dhcp"}, func(context.Context, *uci.Client) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if f.Section("dhcp", "h") != nil {
		t.Fatal("abandoned change was applied")
	}
}

func TestTransactWithoutRollbackCommits(t *testing.T) {
	f, m := setup(t)
	m.Rollback = false
	err := m.Transact(context.Background(), []string{"system"}, func(ctx context.Context, u *uci.Client) error {
		return u.Add(ctx, "system", "timeserver", "ntp", uci.Values{"server": []string{"a", "b"}})
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.CallCount("uci.apply") != 0 || f.CallCount("uci.commit") != 1 {
		t.Fatal("expected plain commit")
	}
	if s := f.Section("system", "ntp"); s == nil || len(s.Values["server"].([]string)) != 2 {
		t.Fatalf("bad %+v", s)
	}
}
