.PHONY: build test lint clean config-local

build:
	go build -o bin/httphop-server ./cmd/server
	go build -o bin/httphop-client ./cmd/client

test:
	go test -race ./...

lint:
	go vet ./...

clean:
	rm -rf bin/

# Copy local dev examples into configs/local/ and configs/secrets/ (both gitignored).
config-local:
	mkdir -p configs/local configs/secrets
	cp configs/examples/local/server.yaml.example configs/local/server.yaml
	cp configs/examples/local/client.yaml.example configs/local/client.yaml
	@for f in configs/examples/secrets/*.token.example; do \
		base=$$(basename "$$f" .example); \
		dest="configs/secrets/$$base"; \
		if [ ! -f "$$dest" ]; then cp "$$f" "$$dest"; fi; \
	done
	@echo "configs/local/ + configs/secrets/ ready. Generate secrets, e.g.:"
	@echo "  openssl rand -hex 32 > configs/secrets/home-gpu-01.token"

