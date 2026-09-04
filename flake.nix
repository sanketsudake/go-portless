{
  description = "Dial services by name with readiness built into the dial — the portless CLI";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";

  outputs =
    { self, nixpkgs }:
    let
      # Keep in step with the release tag; goreleaser stamps the same string
      # into main.version from {{ .Version }}.
      version = "0.4.0";

      systems = [
        "aarch64-darwin"
        "x86_64-darwin"
        "aarch64-linux"
        "x86_64-linux"
      ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      packages = forAllSystems (pkgs: rec {
        portless = pkgs.buildGoModule {
          pname = "portless";
          inherit version;
          src = self;

          # The CLI is its own module under cmd/portless, as the goreleaser
          # build's `dir: cmd/portless` says.
          modRoot = "cmd/portless";
          vendorHash = "sha256-njbGgsjFhY3D5rgIbaRQkPQ6+iqIYhIhv9ht67bOg10=";

          # Matches the goreleaser build: pure Go, no cgo.
          env.CGO_ENABLED = 0;

          ldflags = [
            "-s"
            "-w"
            "-X main.version=${version}"
          ];

          meta = {
            description = "Dial services by name with readiness built into the dial";
            homepage = "https://github.com/sanketsudake/go-portless";
            license = pkgs.lib.licenses.asl20;
            mainProgram = "portless";
          };
        };
        default = portless;
      });

      formatter = forAllSystems (pkgs: pkgs.nixfmt-rfc-style);
    };
}
