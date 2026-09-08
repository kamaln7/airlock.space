# NixOS deployment

Flake outputs:

- `packages.<system>.default` — `airlocksshd`
- `nixosModules.default` — socket-activated `DynamicUser` service

`vendorHash` in `nix/package.nix` must match `go.sum`. After `go mod tidy`:

```sh
make update-vendor-hash   # rewrite the hash
make ci                   # check it (host nix, or Colima if nix is not on PATH)
make hooks                # once per clone: include .gitconfig named hook
```

The pre-commit hook is a Git 2.36+ named hook (`hook.vendor-hash`) in `.gitconfig`. `make hooks` sets `include.path`; Git will not enable it on clone by itself. `./nix/vendor-hash.sh` uses host `nix` if present, otherwise the Colima VM (repo is virtiofs-mounted at the same path).

## Flake input

```nix
{
  inputs.airlock.url = "github:kamaln7/airlock.space";
}
```

## Configuration

```nix
{ inputs, config, pkgs, ... }:

{
  imports = [ inputs.airlock.nixosModules.default ];

  services.airlock = {
    enable = true;
    package = inputs.airlock.packages.${pkgs.stdenv.hostPlatform.system}.default;
    port = 22;
    openFirewall = true;
    hostKeyFile = "/run/secrets/airlock-ssh-host-key";
    nasaKeyFile = "/run/secrets/airlock-nasa-api-key";
  };
}
```

airlock binds port 22. Move OpenSSH off it:

```nix
services.openssh.ports = [ 2222 ];
```

`hostKeyFile` is required. It is airlock's application SSH host key, not the machine's OpenSSH key. Keep the same key across deploys so returning clients do not see a host-key changed warning.

`nasaKeyFile` is optional. Without it the NASA client falls back to `DEMO_KEY`. `NASA_API_KEY` in the environment still wins.

The module starts `airlock.socket` on the configured address and port and launches `airlock.service` on demand. `openFirewall = true` opens that TCP port in the NixOS firewall.
