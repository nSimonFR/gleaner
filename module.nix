self:
{ config, lib, pkgs, ... }:

let
  cfg = config.services.gleaner;
  inherit (lib) mkEnableOption mkOption mkIf types;
in {
  options.services.gleaner = {
    enable = mkEnableOption "gleaner — quota-aware coding-agent dispatcher";

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

    workTreeRoot = mkOption {
      type = types.str;
      default = "/var/lib/gleaner/worktrees";
      description = "Where gleaner creates per-task git worktrees.";
    };

    timer = {
      onUnitActiveSec = mkOption {
        type = types.str;
        default = "10min";
        description = "How often to evaluate the predicate.";
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
      description = "Gleaner — one-shot dispatch evaluation";
      wantedBy = [ ]; # triggered by .timer, not at boot
      after = [ "network-online.target" ];
      wants = [ "network-online.target" ];

      serviceConfig = {
        Type = "oneshot";
        User = cfg.user;
        ExecStart = "${cfg.package}/bin/gleaner drain --config ${cfg.configFile} --worktree-root ${cfg.workTreeRoot}";
        # Lock the dispatcher so two ticks can't overlap on a slow run.
        RuntimeDirectory = "gleaner";
        StateDirectory = "gleaner";
        # Soft hardening — gleaner needs read access to user's HOME for journals.
        ProtectSystem = "strict";
        ProtectHome = false;
        ProtectKernelTunables = true;
        ProtectKernelModules = true;
        NoNewPrivileges = true;
        # Gleaner shells out to `gh`, `git`, and the configured executor — keep PATH usable.
      };

      path = [ pkgs.git pkgs.gh ];
    };

    systemd.timers.gleaner = {
      description = "Gleaner — poll predicate every ${cfg.timer.onUnitActiveSec}";
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
