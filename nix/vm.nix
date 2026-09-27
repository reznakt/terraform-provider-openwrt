# Helpers for the OpenWrt acceptance-test VM, each wrapping scripts/<name>.sh.
{
  lib,
  writeShellApplication,
  coreutils,
  curl,
  jq,
  qemu,
  go,
  opentofu,
  vm-image,
  provider,
}:
let
  mk =
    name: runtimeInputs:
    writeShellApplication {
      inherit name;
      text = builtins.readFile ../scripts/${name}.sh;
      runtimeInputs = [ coreutils ] ++ runtimeInputs;
      runtimeEnv = provider.testEnv // {
        OPENWRT_VM_IMAGE = "${vm-image}/openwrt.img";
        OPENWRT_VM_RELEASE = vm-image.release;
      };
      meta.platforms = lib.platforms.linux;
    };
in
rec {
  run-vm = mk "run-vm" [
    qemu
    curl
    jq
  ];
  stop-vm = mk "stop-vm" [ ];
  test-acc = mk "test-acc" [
    go
    opentofu
    run-vm
    stop-vm
  ];
}
