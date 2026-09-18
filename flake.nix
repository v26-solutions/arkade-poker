{
  description = "Arkade Poker";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-parts.url = "github:hercules-ci/flake-parts";
  };

  outputs =
    inputs@{ flake-parts, ... }:
    flake-parts.lib.mkFlake { inherit inputs; } {
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "aarch64-darwin"
      ];

      perSystem =
        { config, pkgs, ... }:
        {
          packages.default = config.packages.poker;
          packages.poker = pkgs.buildGoModule {
            pname = "arkade-poker";
            version = "0-unstable";
            src = pkgs.lib.fileset.toSource {
              root = ./.;
              fileset = pkgs.lib.fileset.unions [
                ./go.mod
                ./go.sum
                ./cmd
                ./internal
              ];
            };
            vendorHash = "sha256-mBNOewljxhcg/kAORaMUu1BQlqQjB4BJD8NEcylyHMU=";
            subPackages = [ "cmd/poker" ];
            env.CGO_ENABLED = "0";
            tags = [ "purego" ];
            buildFlags = [ "-buildvcs=false" ];
            ldflags = pkgs.lib.optional (inputs.self ? rev || inputs.self ? dirtyRev)
              "-X=arkade-poker/go/internal/buildinfo.Version=${inputs.self.shortRev or inputs.self.dirtyShortRev}";
            meta.mainProgram = "poker";
          };

          devShells.default = pkgs.mkShell {
            DOCKER_CLI_PLUGIN_EXTRA_DIRS = "${pkgs.docker-compose}/libexec/docker/cli-plugins";
            packages = with pkgs; [
              cacert
              docker
              docker-compose
              gnumake
              go
              gopls
              nodejs_22
            ];
          };
        };
    };
}
