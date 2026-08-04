.PHONY: build test lint clean

build:
	go build -o bin/httphop-server ./cmd/server
	go build -o bin/httphop-client ./cmd/client

test:
	go test -race ./...

lint:
	go vet ./...

clean:
	rm -rf bin/
