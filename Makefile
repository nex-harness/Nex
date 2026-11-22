.PHONY: test test-web verify-nocgo build-web build build-runners package-server run-local clean client-generate client-dev client-dev-ios client-dev-android build-clients-desktop build-client-ios build-client-android

NODE_PATH_DIR := $(shell dirname "$$(command -v node 2>/dev/null || echo /Users/tylinux/.nvm/versions/node/v23.9.0/bin/node)")
MYGO_CLI := go run github.com/egoist/mygo/cmd/mygo@v0.10.0

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
	CGO_ENABLED=0 go build -trimpath -o bin/nex-relay ./cmd/nex-relay
	CGO_ENABLED=0 go build -trimpath -o bin/nex-app ./cmd/nex-app

build-runners:
	mkdir -p bin/runners
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/runners/nex-runner-linux-amd64 ./cmd/nex-runner
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o bin/runners/nex-runner-linux-arm64 ./cmd/nex-runner
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -o bin/runners/nex-runner-darwin-arm64 ./cmd/nex-runner
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -o bin/runners/nex-runner-darwin-amd64 ./cmd/nex-runner
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -o bin/runners/nex-runner-windows-amd64.exe ./cmd/nex-runner

package-server:
	./scripts/package-server.sh

run-local: build
	./scripts/start-local.sh

clean:
	rm -rf bin dist coverage.out .mygo

client-generate:
	PATH="$(NODE_PATH_DIR):$$PATH" $(MYGO_CLI) generate

client-dev:
	PATH="$(NODE_PATH_DIR):$$PATH" $(MYGO_CLI) dev

client-dev-ios:
	PATH="$(NODE_PATH_DIR):$$PATH" $(MYGO_CLI) dev -platform ios

client-dev-android:
	PATH="$(NODE_PATH_DIR):$$PATH" $(MYGO_CLI) dev -platform android

build-clients-desktop:
	PATH="$(NODE_PATH_DIR):$$PATH" ./scripts/build-clients.sh desktop

build-client-ios:
	PATH="$(NODE_PATH_DIR):$$PATH" ./scripts/build-clients.sh ios

build-client-android:
	PATH="$(NODE_PATH_DIR):$$PATH" ./scripts/build-clients.sh android
