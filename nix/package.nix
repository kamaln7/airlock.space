{ lib, buildGoModule }:

buildGoModule {
  pname = "airlock.space";
  version = "0-unstable";
  src = ../.;

  vendorHash = "sha256-oHyuhC5sKKd/Sc3Ro8EefoDC8Un0PffU1lx+SCMAjXI=";

  subPackages = [ "cmd/airlocksshd" ];
  env.CGO_ENABLED = 0;

  ldflags = [ "-s" "-w" ];

  meta = {
    description = "NASA Astronomy Picture of the Day served as a TUI over SSH";
    homepage = "https://airlock.space";
    license = lib.licenses.mit;
    mainProgram = "airlocksshd";
  };
}
