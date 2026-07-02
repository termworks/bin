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
        description = "Provider override.";
      };
      version = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "Release tag/version to install.";
      };
      asset = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "Exact release asset to use.";
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
      default = "${config.home.homeDirectory}/.local/bin";
      description = "Directory where managed binaries are installed.";
    };
    configFile = lib.mkOption {
      type = lib.types.str;
      default = "${config.xdg.configHome}/bin/list.json";
      description = "bin manifest path for this Home Manager instance.";
    };
    stateFile = lib.mkOption {
      type = lib.types.str;
      default = "${config.xdg.dataHome}/bin/config.state.json";
      description = "bin state path for this Home Manager instance.";
    };
    addToPath = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Add wrappers for managed binaries to home.packages.";
    };
    refresh = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Pass --refresh to bin apply for all entries.";
    };
    binaries = lib.mkOption {
      type = lib.types.attrsOf binSpec;
      default = { };
      description = "Declarative binaries managed by bin.";
    };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ] ++ lib.optional cfg.addToPath managedPath;

    home.activation.binApply = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
      run mkdir -p ${lib.escapeShellArg cfg.installDir} ${lib.escapeShellArg (dirOf cfg.configFile)} ${lib.escapeShellArg (dirOf cfg.stateFile)}
      run env \
        BIN_CONFIG_FILE=${lib.escapeShellArg cfg.configFile} \
        BIN_STATE_FILE=${lib.escapeShellArg cfg.stateFile} \
        BIN_DEFAULT_PATH=${lib.escapeShellArg cfg.installDir} \
        BIN_NONINTERACTIVE=1 \
        ${cfg.package}/bin/bin apply ${desired} --non-interactive ${lib.optionalString cfg.refresh "--refresh"}
    '';
  };
}
