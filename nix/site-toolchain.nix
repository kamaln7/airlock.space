{ lib, stdenv, stdenvNoCC, fetchurl, autoPatchelfHook }:
let
  # Nixpkgs lags the compiler versions used by the browser build.
  releases = {
    aarch64-darwin = {
      tinygoPlatform = "darwin-arm64";
      tinygoHash = "696a37db149c2c37ed0da96cc1f2cca2630fca0c4dd19823a2a481c9b4a6a748";
      binaryenPlatform = "arm64-macos";
      binaryenHash = "ad66da82ac13f163e424b1643f16c6dfcccc98b5966296b43e52d3cab04f84a8";
    };
    aarch64-linux = {
      tinygoPlatform = "linux-arm64";
      tinygoHash = "48cece9e1e886a176caad0918182d90f8348ad3084774e6239f42e5e11e40990";
      binaryenPlatform = "aarch64-linux";
      binaryenHash = "89c07ea56faf38d0fbecf36ca8ec0721756716185f265b568e133d427f299bf8";
    };
    x86_64-linux = {
      tinygoPlatform = "linux-amd64";
      tinygoHash = "ed0f45543a49ff2a99120a5a1b8814a0750e1fda1a66f8e2a0a172576134aaf9";
      binaryenPlatform = "x86_64-linux";
      binaryenHash = "2dc9c7813f5375db93d96ead4b78222fcc3e2677bbb832297af4797782a37489";
    };
  };
  release = releases.${stdenv.hostPlatform.system};
  installRelease = { pname, version, url, sha256, license }: stdenvNoCC.mkDerivation {
    inherit pname version;
    src = fetchurl { inherit url sha256; };
    nativeBuildInputs = lib.optionals stdenv.hostPlatform.isLinux [ autoPatchelfHook ];
    buildInputs = lib.optionals stdenv.hostPlatform.isLinux [ stdenv.cc.cc.lib ];
    dontConfigure = true;
    dontBuild = true;
    # TinyGo ships target libraries that must not be stripped by the host toolchain.
    dontStrip = true;
    installPhase = ''
      runHook preInstall
      mkdir -p "$out"
      cp -R . "$out/"
      runHook postInstall
    '';
    meta = {
      inherit license;
      platforms = builtins.attrNames releases;
    };
  };
in
{
  tinygo = installRelease {
    pname = "tinygo";
    version = "0.44.0";
    url = "https://github.com/tinygo-org/tinygo/releases/download/v0.44.0/tinygo0.44.0.${release.tinygoPlatform}.tar.gz";
    sha256 = release.tinygoHash;
    license = lib.licenses.bsd3;
  };
  binaryen = installRelease {
    pname = "binaryen";
    version = "133";
    url = "https://github.com/WebAssembly/binaryen/releases/download/version_133/binaryen-version_133-${release.binaryenPlatform}.tar.gz";
    sha256 = release.binaryenHash;
    license = lib.licenses.asl20;
  };
}
