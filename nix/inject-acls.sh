#!/usr/bin/env bash
# Usage: inject-acls.sh <image.img.gz> <out.img> <acl.json>...
# Copies rpcd ACL files into /usr/share/rpcd/acl.d of an OpenWrt ext4
# combined image without mounting it.
set -euo pipefail

src=$1 out=$2
shift 2

# OpenWrt images carry trailing padding that makes gzip exit with 2 (warning).
gzip -dc "$src" >"$out" || [ $? -eq 2 ]

# Partition 2 is the root filesystem.
read -r start size < <(sfdisk -J "$out" | jq -r '.partitiontable.partitions[1] | "\(.start) \(.size)"')
dd if="$out" of=root.ext4 bs=512 skip="$start" count="$size" status=none

cmds=("cd /usr/share/rpcd/acl.d")
for acl in "$@"; do
	name=$(basename "$acl" | sed 's/^[a-z0-9]*-//')  # strip the store hash prefix
	install -m644 "$acl" "$name"
	cmds+=("write $name $name")
done
printf '%s\n' "${cmds[@]}" | debugfs -w root.ext4 -f - >/dev/null

e2fsck -fy root.ext4 >/dev/null || [ $? -le 1 ]
dd if=root.ext4 of="$out" bs=512 seek="$start" conv=notrunc status=none
