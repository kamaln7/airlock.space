{
  description = "airlock.space: NASA APOD over SSH";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { nixpkgs, ... }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "aarch64-darwin"
      ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
    in
    {
      packages = forAllSystems (
        system:
        let
          pkgs = import nixpkgs { inherit system; };
        in
        {
          site = pkgs.callPackage ./nix/site.nix { };
        }
        // pkgs.lib.optionalAttrs pkgs.stdenv.hostPlatform.isLinux {
          default = pkgs.callPackage ./nix/package.nix { };
        }
      );

      devShells = forAllSystems (
        system:
        let
          pkgs = import nixpkgs { inherit system; };
          tools = pkgs.callPackage ./nix/site-toolchain.nix { };
        in
        {
          default = pkgs.mkShellNoCC {
            packages = [
              pkgs.go_1_26
              pkgs.nodejs_24
              pkgs.git
              pkgs.gnumake
              tools.tinygo
              tools.binaryen
            ];
          };
        }
      );

      nixosModules.default = import ./nix/module.nix;
    };
}
