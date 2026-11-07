.PHONY: build test lint clean validate-config

# Build target
build:
	CGO_ENABLED=0 go build -o vshm ./cmd/vshm

# Test target
test:
	go test -race ./...

# Lint target
lint:
	go vet ./...

# Clean target
clean:
	rm -f vshm
	go clean

# Validate configuration
validate-config: build
	./vshm validate-config
