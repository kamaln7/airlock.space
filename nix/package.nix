{ lib, buildGo127Module }:

buildGo127Module {
  pname = "airlock.space";
  version = "0-unstable";
  src = ../.;

  vendorHash = "sha256-oTxb451uYeZZFDdduQi3Y/1EnvOpoHTywoN6uJBU5S0=";

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
