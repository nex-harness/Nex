.PHONY: test test-web verify-nocgo build-web build run-local clean

NODE_PATH_DIR := $(shell dirname "$$(command -v node 2>/dev/null || echo /Users/tylinux/.nvm/versions/node/v23.9.0/bin/node)")

test:
	CGO_ENABLED=0 go test -race -cover ./...

test-web:
	PATH="$(NODE_PATH_DIR):$$PATH" npm --prefix clients/web test

verify-nocgo:
	./scripts/verify-nocgo.sh

build-web:
	@if [ -f clients/web/package.json ]; then \
		PATH="$(NODE_PATH_DIR):$$PATH" npm --prefix clients/web run build; \
	fi

build: build-web
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -o bin/nex-server ./cmd/nex-server
	CGO_ENABLED=0 go build -trimpath -o bin/nex-runner ./cmd/nex-runner
	CGO_ENABLED=0 go build -trimpath -o bin/nex-app ./cmd/nex-app

run-local: build
	./scripts/start-local.sh

clean:
	rm -rf bin dist coverage.out
