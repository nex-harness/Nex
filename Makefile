.PHONY: test verify-nocgo build-web build clean

test:
	CGO_ENABLED=0 go test -race -cover ./...

verify-nocgo:
	./scripts/verify-nocgo.sh

build-web:
	@if [ -f clients/web/package.json ]; then \
		PATH="/Users/tylinux/.nvm/versions/node/v23.9.0/bin:$$PATH" npm --prefix clients/web run build; \
	fi

build: build-web
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -o bin/nex-server ./cmd/nex-server
	CGO_ENABLED=0 go build -trimpath -o bin/nex-runner ./cmd/nex-runner

clean:
	rm -rf bin dist coverage.out
