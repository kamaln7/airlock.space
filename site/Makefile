.PHONY: wasm

# Go 1.26 avoids TinyGo's Go 1.27 hash/maphash incompatibility.
export GOTOOLCHAIN ?= go1.26.8
wasm:
	go -C .. tool booba-assets --no-index site/.generated/
	git -C .. apply site/patches/ghostty-web-host-policy.patch
	# Regex initialization exceeds TinyGo's default 64 KiB task stack.
	# Precise GC skips image bytes instead of treating them as possible pointers.
	cd .. && tinygo build -target wasm -gc=precise -stack-size=512KB -no-debug -opt=z -trimpath -o site/.generated/app.wasm ./cmd/airlockweb
	cp "$$(tinygo env TINYGOROOT)/targets/wasm_exec.js" .generated/wasm_exec.js
