{
  description = "airlock.space: NASA APOD over SSH";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { nixpkgs, ... }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
    in
    {
      packages = forAllSystems (system: {
        default = (import nixpkgs { inherit system; }).callPackage ./nix/package.nix { };
      });

      nixosModules.default = import ./nix/module.nix;
    };
}
