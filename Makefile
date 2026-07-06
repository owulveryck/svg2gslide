GOROOT := $(shell go env GOROOT)

.PHONY: build wasm serve test

build:
	go build ./...

test:
	go test ./...

wasm:
	GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" -o web/main.wasm ./cmd/wasm
	install -m 0644 "$(GOROOT)/lib/wasm/wasm_exec.js" web/wasm_exec.js

serve: wasm
	@echo "open http://localhost:8000"
	cd web && python3 -m http.server 8000
