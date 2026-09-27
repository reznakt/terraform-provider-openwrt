{
  description = "Terraform/OpenTofu provider for OpenWrt (ubus JSON-RPC)";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      forAllSystems =
        f:
        nixpkgs.lib.genAttrs [ "x86_64-linux" "aarch64-linux" ] (
          system: f nixpkgs.legacyPackages.${system} self.packages.${system}
        );
    in
    {
      packages = forAllSystems (
        pkgs: own:
        {
          default = pkgs.callPackage ./nix/package.nix { };
          vm-image = pkgs.callPackage ./nix/vm-image.nix { };
          docs-site = pkgs.callPackage ./nix/docs-site.nix { };
          treefmt = pkgs.callPackage ./nix/treefmt.nix { };
        }
        // pkgs.callPackages ./nix/vm.nix {
          inherit (own) vm-image;
          provider = own.default;
        }
      );

      formatter = forAllSystems (_: own: own.treefmt);

      checks = forAllSystems (_: own: { provider = own.default.overrideAttrs { doCheck = true; }; });

      devShells = forAllSystems (
        pkgs: own: {
          default = pkgs.mkShell {
            inputsFrom = [ own.default ];
            packages = [
              pkgs.just
              own.treefmt
              pkgs.shellcheck
              pkgs.actionlint
              pkgs.gopls
              pkgs.golangci-lint
              pkgs.terraform-plugin-docs
              own.docs-site.pythonEnv
              pkgs.opentofu
              own.run-vm
              own.stop-vm
              own.test-acc
            ];
            env = own.default.testEnv;
          };
        }
      );
    };
}
