# treefmt with every formatter treefmt.toml refers to; used by `nix fmt`.
{
  writeShellApplication,
  treefmt,
  go,
  nixfmt,
  shfmt,
  opentofu,
}:
writeShellApplication {
  name = "treefmt";
  runtimeInputs = [
    treefmt
    go
    nixfmt
    shfmt
    opentofu
  ];
  text = ''exec treefmt "$@"'';
}
