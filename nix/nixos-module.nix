{ self ? null }:
{ config, lib, pkgs, ... }:

let
  cfg = config.programs.bin;
  defaultPackage =
    if self != null
    then self.packages.${pkgs.system}.default
    else pkgs.bin;

  binSpec = lib.types.submodule ({ name, ... }: {
    options = {
      url = lib.mkOption {
        type = lib.types.str;
        description = "Repository or provider URL understood by bin.";
        example = "github.com/atuinsh/atuin";
      };
      name = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "Installed binary name. Defaults to the attribute name.";
      };
      path = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "Absolute installation path. Defaults to installDir/name.";
      };
      provider = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "Provider override, for example github, gitlab, codeberg, hashicorp, or goinstall.";
      };
      version = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "Release tag/version to install. Null reuses existing state or resolves latest on first apply.";
      };
      asset = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "Exact release asset to use, avoiding interactive selection.";
        example = "atuin-aarch64-unknown-linux-musl.tar.gz";
      };
      packagePath = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "Exact file inside an archive to install.";
      };
      description = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "Optional description stored in the bin manifest.";
      };
      tags = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ "nix" ];
        description = "Tags assigned to the managed binary.";
      };
      patch = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = "Whether bin should patch ELF interpreter/RUNPATH for this host.";
      };
      force = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = "Reinstall this binary on every apply.";
      };
      refresh = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = "Resolve latest instead of reusing existing state on apply.";
      };
    };
  });

  mkDesiredBin = name: spec: {
    inherit (spec) url provider version asset description tags patch force refresh;
    name = if spec.name == null then name else spec.name;
    path = spec.path;
    package_path = spec.packagePath;
  };

  desired = pkgs.writeText "bin-desired.json" (builtins.toJSON {
    default_path = cfg.installDir;
    bins = lib.mapAttrs mkDesiredBin cfg.binaries;
  });

  binNames = lib.mapAttrsToList (name: spec: if spec.name == null then name else spec.name) cfg.binaries;
  managedPath = pkgs.runCommand "bin-managed-path" { } ''
    mkdir -p "$out/bin"
    ${lib.concatMapStringsSep "\n" (name: ''
      ln -s ${lib.escapeShellArg "${cfg.installDir}/${name}"} "$out/bin/${name}"
    '') binNames}
  '';
in
{
  options.programs.bin = {
    enable = lib.mkEnableOption "bin declarative binary manager";
    package = lib.mkOption {
      type = lib.types.package;
      default = defaultPackage;
      defaultText = lib.literalExpression "inputs.bin.packages.\${pkgs.system}.default";
      description = "bin package to run.";
    };
    installDir = lib.mkOption {
      type = lib.types.str;
      default = "/var/lib/bin/bin";
      description = "Directory where managed binaries are installed.";
    };
    configFile = lib.mkOption {
      type = lib.types.str;
      default = "/var/lib/bin/list.json";
      description = "bin manifest path for this module-managed instance.";
    };
    stateFile = lib.mkOption {
      type = lib.types.str;
      default = "/var/lib/bin/config.state.json";
      description = "bin state path for this module-managed instance.";
    };
    addToPath = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Add wrappers for managed binaries to environment.systemPackages.";
    };
    refresh = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Pass --refresh to bin apply for all entries.";
    };
    service.enable = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Enable the systemd oneshot that applies the declarative manifest.";
    };
    binaries = lib.mkOption {
      type = lib.types.attrsOf binSpec;
      default = { };
      description = "Declarative binaries managed by bin.";
    };
  };

  config = lib.mkIf cfg.enable {
    environment.systemPackages = [ cfg.package ] ++ lib.optional cfg.addToPath managedPath;

    systemd.tmpfiles.rules = [
      "d ${cfg.installDir} 0755 root root -"
      "d ${dirOf cfg.configFile} 0755 root root -"
      "d ${dirOf cfg.stateFile} 0755 root root -"
    ];

    systemd.services.bin-apply = lib.mkIf cfg.service.enable {
      description = "Apply declarative bin-managed binaries";
      wantedBy = [ "multi-user.target" ];
      wants = [ "network-online.target" ];
      after = [ "network-online.target" ];
      restartTriggers = [ desired cfg.package ];
      environment = {
        BIN_CONFIG_FILE = cfg.configFile;
        BIN_STATE_FILE = cfg.stateFile;
        BIN_DEFAULT_PATH = cfg.installDir;
        BIN_NONINTERACTIVE = "1";
      };
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
      };
      script = ''
        exec ${cfg.package}/bin/bin apply ${desired} --non-interactive ${lib.optionalString cfg.refresh "--refresh"}
      '';
    };
  };
}
