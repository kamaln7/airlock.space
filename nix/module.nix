{ config, lib, pkgs, ... }:

let
  cfg = config.services.airlock;
in
{
  options.services.airlock = {
    enable = lib.mkEnableOption "the airlock.space SSH APOD server";

    package = lib.mkOption {
      type = lib.types.package;
      default = pkgs.callPackage ./package.nix { };
      defaultText = lib.literalExpression "pkgs.callPackage ./package.nix { }";
      description = "airlock package to run.";
    };

    listenAddress = lib.mkOption {
      type = lib.types.str;
      default = "0.0.0.0";
      description = "Address on which the socket accepts airlock SSH connections.";
    };

    port = lib.mkOption {
      type = lib.types.port;
      default = 22;
      description = "TCP port on which airlock accepts SSH connections.";
    };

    openFirewall = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Whether to allow airlock's TCP port through the NixOS firewall.";
    };

    hostKeyFile = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      example = "/run/secrets/airlock-ssh-host-key";
      description = ''
        Path to airlock's application SSH host key. Passed as a systemd
        credential so DynamicUser does not need access to the secret file.
      '';
    };

    nasaKeyFile = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      example = "/run/secrets/airlock-nasa-api-key";
      description = ''
        Path to a NASA API key. Passed as a systemd credential; the service
        sees it as NASA_API_KEY_PATH.
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    assertions = [
      {
        assertion = cfg.hostKeyFile != null;
        message = "services.airlock.hostKeyFile must be set to preserve airlock's SSH host-key identity.";
      }
    ];

    networking.firewall.allowedTCPPorts = lib.optional cfg.openFirewall cfg.port;

    systemd.sockets.airlock = {
      description = "airlock.space SSH socket";
      wantedBy = [ "sockets.target" ];
      socketConfig = {
        ListenStream = "${cfg.listenAddress}:${toString cfg.port}";
        NoDelay = true;
      };
    };

    systemd.services.airlock = {
      description = "airlock.space SSH APOD server";
      requires = [ "airlock.socket" ];
      after = [ "network.target" ];
      serviceConfig = {
        ExecStart = "${cfg.package}/bin/airlocksshd";
        DynamicUser = true;
        StateDirectory = "airlock";
        WorkingDirectory = "/var/lib/airlock";
        LoadCredential =
          [ "ssh-host-key:${cfg.hostKeyFile}" ]
          ++ lib.optional (cfg.nasaKeyFile != null) "nasa-api-key:${cfg.nasaKeyFile}";
        Environment =
          [ "SSH_HOST_KEY_PATH=%d/ssh-host-key" ]
          ++ lib.optional (cfg.nasaKeyFile != null) "NASA_API_KEY_PATH=%d/nasa-api-key";
        Restart = "always";
        RestartSec = "1s";
      };
    };
  };
}
