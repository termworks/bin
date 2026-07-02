{ self ? null }:
{ config, lib, pkgs, ... }:

let
  cfg = config.programs.bin;
  defaultPackage =
    if self != null
    then self.packages.${pkgs.system}.default
    else pkgs.bin;

  repoName = repo:
    lib.removeSuffix ".git" (baseNameOf (lib.removeSuffix "/" repo));

  entrySpec = lib.types.submodule {
    options = {
      repo = lib.mkOption {
        type = lib.types.str;
        description = "Repository or provider URL understood by bin.";
        example = "github.com/atuinsh/atuin";
      };
      name = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "Installed binary name. Defaults to the repository basename.";
      };
      tag = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "Single bin tag/tier for this binary.";
        example = "essential";
      };
      tags = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ ];
        description = "Bin tags/tiers for this binary. Overrides tag when non-empty.";
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
      description = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "Optional description stored in the bin manifest.";
      };
      patch = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = "Whether bin should patch ELF interpreter/RUNPATH for this host.";
      };
    };
  };

  attrSpec = lib.types.submodule ({ name, ... }: {
    options = (entrySpec.getSubOptions [ ]) // {
      repo = lib.mkOption {
        type = lib.types.str;
        description = "Repository or provider URL understood by bin.";
      };
      name = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = name;
        description = "Installed binary name. Defaults to the attribute name.";
      };
    };
  });

  normalizeEntry = entry:
    let
      e =
        if builtins.isString entry
        then { repo = entry; name = null; tag = null; tags = [ ]; path = null; provider = null; description = null; patch = true; }
        else entry;
      name = if e.name == null then repoName e.repo else e.name;
      tags =
        if e.tags != [ ]
        then e.tags
        else if e.tag != null
        then [ e.tag ]
        else [ "default" ];
      path = if e.path == null then "${cfg.installDir}/${name}" else e.path;
      manifest = lib.filterAttrs (_: v: v != null) {
        inherit path tags;
        url = e.repo;
        provider = e.provider;
        description = e.description;
        patch = e.patch;
      };
    in
    {
      inherit name path manifest;
    };

  attrEntries = lib.mapAttrsToList (_: spec: spec) cfg.binaries;
  normalizedEntries = map normalizeEntry (cfg.entries ++ attrEntries);
  manifestBins = builtins.listToAttrs (map (e: { name = e.path; value = e.manifest; }) normalizedEntries);
  manifest = pkgs.writeText "bin-list.json" (builtins.toJSON {
    default_path = cfg.installDir;
    bins = manifestBins;
  });

  managedPath = pkgs.runCommand "bin-managed-path" { } ''
    mkdir -p "$out/bin"
    ${lib.concatMapStringsSep "\n" (e: ''
      ln -s ${lib.escapeShellArg e.path} "$out/bin/${e.name}"
    '') normalizedEntries}
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
      description = "Generated bin manifest path for this module-managed instance.";
    };
    stateFile = lib.mkOption {
      type = lib.types.str;
      default = "/var/lib/bin/config.state.json";
      description = "Mutable bin state path for versions, hashes, and selected assets.";
    };
    addToPath = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Add wrappers for managed binaries to environment.systemPackages.";
    };
    service.enable = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Enable the systemd oneshot that writes list.json and runs bin ensure.";
    };
    entries = lib.mkOption {
      type = lib.types.listOf (lib.types.either lib.types.str entrySpec);
      default = [ ];
      description = "List of repositories or binary entries. Nix turns this into bin's list.json; bin ensure fills state.";
      example = lib.literalExpression ''
        [
          "github.com/rust-lang/mdBook"
          { repo = "github.com/git-town/git-town"; tag = "essential"; }
        ]
      '';
    };
    binaries = lib.mkOption {
      type = lib.types.attrsOf attrSpec;
      default = { };
      description = "Attribute-set form of entries, keyed by installed binary name.";
      example = lib.literalExpression ''
        {
          mdbook.repo = "github.com/rust-lang/mdBook";
          git-town = { repo = "github.com/git-town/git-town"; tag = "essential"; };
        }
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    environment.systemPackages = [ cfg.package ] ++ lib.optional cfg.addToPath managedPath;

    systemd.tmpfiles.rules = [
      "d ${cfg.installDir} 0755 root root -"
      "d ${dirOf cfg.configFile} 0755 root root -"
      "d ${dirOf cfg.stateFile} 0755 root root -"
    ];

    systemd.services.bin-ensure = lib.mkIf cfg.service.enable {
      description = "Ensure declarative bin-managed binaries";
      wantedBy = [ "multi-user.target" ];
      wants = [ "network-online.target" ];
      after = [ "network-online.target" ];
      restartTriggers = [ manifest cfg.package ];
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
        ${pkgs.coreutils}/bin/install -D -m 0644 ${manifest} ${lib.escapeShellArg cfg.configFile}
        ${pkgs.coreutils}/bin/mkdir -p ${lib.escapeShellArg cfg.installDir} ${lib.escapeShellArg (dirOf cfg.stateFile)}
        exec ${cfg.package}/bin/bin --tag all ensure
      '';
    };
  };
}
