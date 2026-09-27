# shellcheck shell=bash
# Boots a fresh OpenWrt VM for acceptance tests. The LAN side (eth0, the
# router's static 192.168.1.1) is reachable on 127.0.0.1:$OPENWRT_VM_PORT;
# the WAN side (eth1) gets DHCP and internet access from QEMU.
#
# Provided by nix/scripts.nix: OPENWRT_VM_IMAGE, OPENWRT_VM_RELEASE.

dir=${OPENWRT_VM_DIR:-${XDG_RUNTIME_DIR:-/tmp}/openwrt-terraform-vm}
port=${OPENWRT_VM_PORT:-18080}
mkdir -p "$dir"

if [[ -f $dir/pid ]] && kill -0 "$(<"$dir/pid")" 2>/dev/null; then
	echo "VM already running on http://127.0.0.1:$port"
	exit 0
fi

# The daemonized QEMU would otherwise keep our stdout open forever.
# Every boot starts from a pristine router.
install -m644 "$OPENWRT_VM_IMAGE" "$dir/disk.img"

accel=()
[[ -w /dev/kvm ]] && accel=(-enable-kvm -cpu host)

# QEMU's user network is moved onto 192.168.1.0/24 so that the port
# forward can target the router's default LAN address.
lan="user,id=lan,net=192.168.1.0/24,host=192.168.1.2,dhcpstart=192.168.1.100"
lan+=",hostfwd=tcp:127.0.0.1:$port-192.168.1.1:80"

qemu-system-x86_64 "${accel[@]}" -m 256 -smp 2 \
	-drive "file=$dir/disk.img,format=raw,if=virtio" \
	-netdev "$lan" -device virtio-net-pci,netdev=lan \
	-netdev user,id=wan -device virtio-net-pci,netdev=wan \
	-display none -serial "file:$dir/console.log" \
	-daemonize -pidfile "$dir/pid" \
	</dev/null >"$dir/qemu.log" 2>&1 || {
	cat "$dir/qemu.log" >&2
	exit 1
}

login='{"jsonrpc":"2.0","id":1,"method":"call","params":["00000000000000000000000000000000","session","login",{"username":"root","password":""}]}'
echo "Waiting for rpcd on http://127.0.0.1:$port/ubus ..."
for _ in {1..180}; do
	if curl -sf -m 2 -d "$login" "http://127.0.0.1:$port/ubus" | jq -e '.result[1].ubus_rpc_session' >/dev/null 2>&1; then
		echo "OpenWrt $OPENWRT_VM_RELEASE is up: export OPENWRT_ENDPOINT=http://127.0.0.1:$port OPENWRT_PASSWORD="
		exit 0
	fi
	sleep 1
done
echo "VM did not come up; see $dir/console.log" >&2
exit 1
