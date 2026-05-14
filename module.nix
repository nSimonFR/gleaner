self:
{ config, lib, pkgs, ... }:

let
  cfg = config.services.gleaner;
  inherit (lib) mkEnableOption mkOption mkIf types;
in {
  options.services.gleaner = {
    enable = mkEnableOption "gleaner — quota-aware Linear ticket picker for Cyrus";

    package = mkOption {
      type = types.package;
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
      description = "gleaner package to use.";
    };

    user = mkOption {
      type = types.str;
      description = ''
        User the gleaner service runs as. Must own ~/.claude/projects
        and ~/.codex/sessions and have read access to ~/.claude/.credentials.json.
        No default — gleaner is always run as a real human user on this
        host (it reads that user's quota journals); creating a synthetic
        "gleaner" user would mean no journals to read.
      '';
      example = "nsimon";
    };

    configFile = mkOption {
      type = types.path;
      description = "Path to the gleaner YAML config.";
      example = "./gleaner.config.yaml";
    };

    timer = {
      onUnitActiveSec = mkOption {
        type = types.str;
        default = "10min";
        description = "How often to run a picker tick.";
      };
      onBootSec = mkOption {
        type = types.str;
        default = "2min";
        description = "Delay after boot before the first evaluation.";
      };
      persistent = mkOption {
        type = types.bool;
        default = true;
        description = "systemd Persistent= — catch up missed ticks after reboots.";
      };
    };
  };

  config = mkIf cfg.enable {
    systemd.services.gleaner = {
      description = "Gleaner — one-shot picker tick";
      wantedBy = [ ]; # triggered by .timer, not at boot
      after = [ "network-online.target" ];
      wants = [ "network-online.target" ];

      serviceConfig = {
        Type = "oneshot";
        User = cfg.user;
        ExecStart = "${cfg.package}/bin/gleaner tick --config ${cfg.configFile}";
        RuntimeDirectory = "gleaner";
        StateDirectory = "gleaner";
        # Soft hardening — gleaner needs read access to user's HOME for quota journals.
        ProtectSystem = "strict";
        ProtectHome = false;
        ProtectKernelTunables = true;
        ProtectKernelModules = true;
        NoNewPrivileges = true;
      };
    };

    systemd.timers.gleaner = {
      description = "Gleaner — run a picker tick every ${cfg.timer.onUnitActiveSec}";
      wantedBy = [ "timers.target" ];
      timerConfig = {
        OnBootSec = cfg.timer.onBootSec;
        OnUnitActiveSec = cfg.timer.onUnitActiveSec;
        Persistent = cfg.timer.persistent;
        Unit = "gleaner.service";
      };
    };
  };
}
