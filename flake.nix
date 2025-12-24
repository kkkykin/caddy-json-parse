{
  description = "A Caddy module development environment";

  inputs.nixpkgs.url = "nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let

      supportedSystems = [ "x86_64-linux" "x86_64-darwin" "aarch64-linux" "aarch64-darwin" ];

      forAllSystems = nixpkgs.lib.genAttrs supportedSystems;

      nixpkgsFor = forAllSystems (system: import nixpkgs { inherit system; });

    in
    {

      packages = forAllSystems (system:
        let
          pkgs = nixpkgsFor.${system};
        in
        {
          inherit (pkgs) caddy xcaddy;
        });

      devShells = forAllSystems (system:
        let
          pkgs = nixpkgsFor.${system};
        in
        {
          default = pkgs.mkShell {
            buildInputs = with pkgs; [
              # Go toolchain
              go
              gopls
              gotools
              go-tools
              delve
              golangci-lint

              # Caddy build tools
              caddy
              xcaddy

              # Typical dev helpers
              git
            ];

            GOPROXY = "https://goproxy.cn,direct";
          };
        });

      # The default package for 'nix build'.
      defaultPackage = forAllSystems (system: self.packages.${system}.caddy);
    };
}
