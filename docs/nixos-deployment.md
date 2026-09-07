# NixOS deployment

Flake outputs:

- `packages.<system>.default` — `airlocksshd`
- `nixosModules.default` — socket-activated `DynamicUser` service

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
    listenAddress = "0.0.0.0";
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

`nasaKeyFile` is optional. The module passes it to the service as the systemd credential `nasa-api-key`. `NASA_API_KEY` or `NASAKEY` in the environment still win; otherwise the NASA client falls back to `DEMO_KEY`.

The module starts `airlock.socket` on the configured address and port and launches `airlock.service` on demand. `openFirewall = true` opens that TCP port in the NixOS firewall.
