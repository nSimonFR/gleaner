{
  description = "gleaner — a quota-aware coding-agent dispatcher";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-parts = {
      url = "github:hercules-ci/flake-parts";
      inputs.nixpkgs-lib.follows = "nixpkgs";
    };
  };

  outputs = inputs @ { self, nixpkgs, flake-parts, ... }:
    flake-parts.lib.mkFlake { inherit inputs; } {
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];

      perSystem = { pkgs, system, ... }:
        let
          gleaner = pkgs.buildGoModule {
            pname = "gleaner";
            version = "0.0.1";
            src = ./.;
            vendorHash = null; # no external deps in v0.0.1
            subPackages = [ "cmd/gleaner" ];
            ldflags = [ "-s" "-w" ];
            meta = with pkgs.lib; {
              description = "Quota-aware coding-agent dispatcher";
              homepage = "https://github.com/nSimonFR/gleaner";
              license = licenses.mit;
              mainProgram = "gleaner";
            };
          };
        in {
          packages.gleaner = gleaner;
          packages.default = gleaner;

          devShells.default = pkgs.mkShell {
            packages = [ pkgs.go pkgs.gopls pkgs.golangci-lint ];
          };
        };
    };
}
