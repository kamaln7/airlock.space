.PHONY: help hooks ci update-vendor-hash

help:
	@echo "make hooks                 register the vendor-hash pre-commit hook (once per clone)"
	@echo "make update-vendor-hash    rewrite nix/package.nix vendorHash from go.sum"
	@echo "make ci                    fail if vendorHash does not match go.sum"

# Named hook in .gitconfig (Git 2.36+ / 2.55). Does not replace .git/hooks.
# include.path is local to this clone; Git will not auto-run tracked hooks.
hooks:
	git config --local --unset-all core.hooksPath >/dev/null 2>&1 || true
	git config --local include.path ../.gitconfig
	@echo "include.path=../.gitconfig  (hook.vendor-hash on pre-commit)"

ci:
	./nix/vendor-hash.sh check

update-vendor-hash:
	./nix/vendor-hash.sh update
