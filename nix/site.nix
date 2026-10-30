{
  lib,
  buildNpmPackage,
  fetchNpmDeps,
  buildGo126Module,
  nodejs_24,
  go_1_26,
  git,
  callPackage,
}:
let
  src = lib.cleanSource ../.;
  tools = callPackage ./site-toolchain.nix { };
  goModules =
    (buildGo126Module {
      pname = "airlock-site";
      version = "0-unstable";
      inherit src;
      proxyVendor = true;
      vendorHash = "sha256-a7ues4S4bCc/to4/XUNi1um89AmAQe81RwvF17tiy50=";
    }).goModules;
in
buildNpmPackage {
  pname = "airlock-site";
  version = "0-unstable";
  inherit src;
  nodejs = nodejs_24;
  npmRoot = "site";
  npmBuildFlags = [
    "--prefix"
    "site"
  ];
  npmDeps = fetchNpmDeps {
    src = ../site;
    hash = "sha256-HtF7D3QNLjTHQ4Orw7ich2O2gn4TZdXGZRTb2/JTchA=";
  };
  nativeBuildInputs = [
    go_1_26
    git
    tools.tinygo
    tools.binaryen
  ];

  postConfigure = ''
    export GOPATH="$TMPDIR/go"
    export GOCACHE="$TMPDIR/go-cache"
    export GOPROXY="file://${goModules}"
    export GOSUMDB=off
    export GOTOOLCHAIN=local
  '';

  installPhase = ''
    runHook preInstall
    mkdir -p "$out"
    cp -r site/dist/. "$out/"
    runHook postInstall
  '';

  meta = {
    description = "Airlock browser TUI static website";
    homepage = "https://airlock.space";
    license = lib.licenses.mit;
    platforms = [
      "x86_64-linux"
      "aarch64-linux"
      "aarch64-darwin"
    ];
  };
}
