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
        { pkgs, ... }:
        {
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
