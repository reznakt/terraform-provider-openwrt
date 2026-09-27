package rpc

import (
	"reflect"
	"testing"
)

func TestParseInstalledOpkgStatus(t *testing.T) {
	status := `Package: libc
Version: 1.2.5-r4
Depends: libgcc1
Status: install user installed
Architecture: x86_64

Package: tcpdump-mini
Version: 4.99.5-r1
Status: install user installed
`
	want := map[string]string{"libc": "1.2.5-r4", "tcpdump-mini": "4.99.5-r1"}
	if got := ParseInstalled(status); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestParseInstalledApk(t *testing.T) {
	list := `tcpdump-mini-4.99.5-r1 x86_64 {tcpdump} (BSD-3-Clause) [installed]
luci-app-sqm-1.0-r2 noarch {luci-app-sqm} (Apache-2.0) [installed]
`
	want := map[string]string{"tcpdump-mini": "4.99.5-r1", "luci-app-sqm": "1.0-r2"}
	if got := ParseInstalled(list); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}
