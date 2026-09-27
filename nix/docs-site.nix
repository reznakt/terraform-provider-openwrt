# HTML site rendered from the generated Markdown in docs/ (GitHub Pages).
{
  lib,
  stdenvNoCC,
  python3,
}:
let
  pythonEnv = python3.withPackages (p: [ p.mkdocs-material ]);
in
stdenvNoCC.mkDerivation {
  name = "terraform-provider-openwrt-docs";

  src = lib.fileset.toSource {
    root = ../.;
    fileset = lib.fileset.unions [
      ../mkdocs.yml
      ../docs
    ];
  };

  nativeBuildInputs = [ pythonEnv ];

  buildPhase = ''
    runHook preBuild
    python -m mkdocs build --strict --site-dir $out
    runHook postBuild
  '';
  dontInstall = true;

  passthru = { inherit pythonEnv; };
}
