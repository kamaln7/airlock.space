Build the deployable website with `nix build .#site`. The `result/` directory contains only the static files to serve, including the browser WASM and hashed assets. The package supports x86_64 Linux, aarch64 Linux, and Apple Silicon macOS; the SSH server remains the default package on Linux.

The Nix build fetches npm and Go dependencies by fixed hashes, then compiles without network access. When dependencies change, update the corresponding hash in `nix/site.nix`: use `lib.fakeHash`, build, and replace it with the reported `got:` hash. CI builds the site package on x86_64 Linux.

Run `nix develop` for Node 24, Go 1.26.8, TinyGo 0.44.0, Binaryen 133 (`wasm-opt`), and build tools, or install those tools yourself.
From the repository root, run `npm --prefix site ci && make web` to build `site/dist/`; the build selects Go 1.26.8, uses the Booba integration snapshot pinned in `go.mod`, and applies only `patches/ghostty-web-host-policy.patch` for Airlock-specific input, selection, and link styling.
For local development run `npm --prefix site run dev`; header controls retain native Tab navigation while other page-wide keystrokes drive the TUI. Deploy the contents of the Nix package output, or `site/dist/` from a manual build, to a static web host.
Keep HTML revalidating and cache hashed `/assets/*` for one year as immutable. The included `_headers` file configures this on Cloudflare Pages; configure equivalent response headers on other servers.
