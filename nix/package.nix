{ lib, buildGoModule }:

buildGoModule {
  pname = "airlock.space";
  version = "0-unstable";
  src = ../.;

  vendorHash = "sha256-0hI56m3BQF6npsszVpoBF0TYQ6AyhF1brHgWLnKPz84=";

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
