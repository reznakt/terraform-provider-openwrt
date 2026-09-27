# shellcheck shell=bash
# Stops the VM started by run-vm and discards its disk.

dir=${OPENWRT_VM_DIR:-${XDG_RUNTIME_DIR:-/tmp}/openwrt-terraform-vm}
if [[ -f $dir/pid ]]; then
	kill "$(<"$dir/pid")" 2>/dev/null || true
	rm -f "$dir/pid" "$dir/disk.img"
fi
