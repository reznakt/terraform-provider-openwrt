# Official OpenWrt x86-64 image with the provider's rpcd ACLs baked in.
# The rootfs is edited offline with debugfs, so no root or VM is needed.
{
  stdenvNoCC,
  fetchurl,
  gzip,
  util-linux,
  jq,
  e2fsprogs,
}:
let
  # Current stable release.
  release = "24.10.5";
in
stdenvNoCC.mkDerivation {
  pname = "openwrt-x86-64-terraform-test";
  version = release;

  src = fetchurl {
    url = "https://downloads.openwrt.org/releases/${release}/targets/x86/64/openwrt-${release}-x86-64-generic-ext4-combined.img.gz";
    hash = "sha256:5b854a7a2909a0b21887ba9be773b0964eb28fef81d6b8399d32a0f270a5d8ff";
  };
  dontUnpack = true;

  nativeBuildInputs = [
    gzip
    util-linux
    jq
    e2fsprogs
  ];

  acls = [
    ../router/terraform-acl.json
    ../router/terraform-acl-exec.json
  ];

  buildPhase = ''
    runHook preBuild
    bash ${./inject-acls.sh} $src disk.img $acls
    runHook postBuild
  '';

  installPhase = ''
    install -Dm444 disk.img $out/openwrt.img
  '';

  passthru = { inherit release; };
}
