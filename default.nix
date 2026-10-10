{
  lib,
  self,
  buildGoModule,
  git,
  ...
}:
buildGoModule {
  pname = "beads";
  version = "1.3.1";

  src = self;

  # Point to the main Go package
  subPackages = [ "cmd/bd" ];
  tags = [ "gms_pure_go" ];
  doCheck = false;

  # proxyVendor avoids vendor/modules.txt consistency checks when the vendored
  # tree lags go.mod/go.sum.
  proxyVendor = true;
  # Match the locked Go dependencies; recompute after go.mod/go.sum changes
  # with scripts/update-nix-vendorhash.sh or a verified same-source Nix CI hash.
  vendorHash = "sha256-kNnps1bSj5o1mIDO3oJOjhrt1iJZvk75UT2kfqoVYJU=";

  env.GOTOOLCHAIN = "local";

  # Git is required for tests
  nativeBuildInputs = [ git ];

  meta = with lib; {
    description = "beads (bd) - An issue tracker designed for AI-supervised coding workflows";
    homepage = "https://github.com/gastownhall/beads";
    license = licenses.mit;
    mainProgram = "bd";
    maintainers = [ ];
  };
}
