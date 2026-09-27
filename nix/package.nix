{
  lib,
  buildGoModule,
  opentofu,
  stdenv,
}:
let
  version = "0.1.0";

  # How terraform-plugin-testing finds the CLI and addresses the provider.
  testEnv = {
    TF_ACC_TERRAFORM_PATH = lib.getExe opentofu;
    TF_ACC_PROVIDER_NAMESPACE = "reznakt";
    TF_ACC_PROVIDER_HOST = "registry.opentofu.org";
  };

  mirrorDir = "share/terraform/plugins/registry.opentofu.org/reznakt/openwrt/${version}/${stdenv.hostPlatform.go.GOOS}_${stdenv.hostPlatform.go.GOARCH}";
in
buildGoModule {
  pname = "terraform-provider-openwrt";
  inherit version;

  src = lib.fileset.toSource {
    root = ../.;
    fileset = lib.fileset.unions [
      ../go.mod
      ../go.sum
      ../main.go
      ../internal
    ];
  };
  vendorHash = "sha256-kjIpsM2xlz/mlNjveSBpiWRzhoDrjAWpfikCvzGnfA8=";

  subPackages = [ "." ];
  ldflags = [
    "-s"
    "-w"
    "-X main.version=${version}"
  ];

  # Tests run OpenTofu against an in-memory fake router; enabled in `checks`.
  doCheck = false;
  nativeCheckInputs = [ opentofu ];
  env = testEnv;
  preCheck = "export HOME=$TMPDIR";

  # Filesystem-mirror layout, usable from `provider_installation`.
  postInstall = ''
    mkdir -p $out/${mirrorDir}
    ln -s $out/bin/terraform-provider-openwrt $out/${mirrorDir}/terraform-provider-openwrt_v${version}
  '';

  passthru = { inherit testEnv; };

  meta = {
    description = "Terraform/OpenTofu provider for OpenWrt";
    license = lib.licenses.mit;
    mainProgram = "terraform-provider-openwrt";
  };
}
