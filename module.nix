self:
{ config, lib, pkgs, ... }:

let
  cfg = config.services.gleaner;
  inherit (lib) mkEnableOption mkOption mkIf types;
in {
  options.services.gleaner = {
    enable = mkEnableOption "gleaner — quota-gated cron dispatcher";

    package = mkOption {
      type = types.package;
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
      description = "gleaner package to use.";
    };

    user = mkOption {
      type = types.str;
      description = ''
        User the gleaner service runs as. Must own ~/.codex/sessions
        and have read access to ~/.claude/.credentials.json — those are
        the zero-token quota sources gleaner reads each tick. Triggers
        also exec as this user; pass any per-trigger secrets through
        the `env:` block on each trigger, not the unit's environment.
      '';
      example = "nsimon";
    };

    configFile = mkOption {
      type = types.path;
      description = ''
        Path to the gleaner YAML config. Surface is one key — a list of
        triggers, each with `name`, `when` (quota expression), `run`
        (argv), optional `timeout` and `env`. See README for the
        grammar.
      '';
      example = "./gleaner.config.yaml";
    };

    timer = {
      onUnitActiveSec = mkOption {
        type = types.str;
        default = "10min";
        description = "How often the dispatcher fires.";
      };
      onBootSec = mkOption {
        type = types.str;
        default = "2min";
        description = "Delay after boot before the first tick.";
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
      description = "Gleaner — one-shot tick";
      wantedBy = [ ];
      after = [ "network-online.target" ];
      wants = [ "network-online.target" ];

      serviceConfig = {
        Type = "oneshot";
        User = cfg.user;
        ExecStart = "${cfg.package}/bin/gleaner tick --config ${cfg.configFile}";
        RuntimeDirectory = "gleaner";
        ProtectSystem = "strict";
        ProtectHome = false;
        ProtectKernelTunables = true;
        ProtectKernelModules = true;
        NoNewPrivileges = true;
      };

      # Triggers may exec arbitrary user-configured commands (typically
      # `claude -p …` or `codex run …`); keep the unit's PATH usable.
      path = [ pkgs.bash ];
    };

    systemd.timers.gleaner = {
      description = "Gleaner — tick every ${cfg.timer.onUnitActiveSec}";
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
