{
  description = "Hourly Telegram task reminders";

  inputs.nixpkgs.url = "https://channels.nixos.org/nixos-unstable/nixexprs.tar.xz";

  outputs = { self, nixpkgs }:
    let
      eachSystem = nixpkgs.lib.genAttrs [ "x86_64-linux" "aarch64-linux" ];
    in {
      packages = eachSystem (system:
        let pkgs = nixpkgs.legacyPackages.${system}; in {
          default = pkgs.buildGoModule {
            pname = "sakanotifybot";
            version = "0.1.0";
            src = pkgs.lib.fileset.toSource {
              root = ./.;
              fileset = pkgs.lib.fileset.unions [ ./go.mod ./go.sum (pkgs.lib.fileset.fileFilter (file: file.hasExt "go") ./.) ];
            };
            vendorHash = "sha256-5PxQ2reSVeZzmDqC8OpaRV8Q9brupVZjzC5Ia6GZovo=";
            env.CGO_ENABLED = "0";
          };
        });

      devShells = eachSystem (system:
        let pkgs = nixpkgs.legacyPackages.${system}; in {
          default = pkgs.mkShell { packages = [ pkgs.go pkgs.gopls ]; };
        });
    };
}
