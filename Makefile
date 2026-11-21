GO ?= go
BINARY := vshm
DIST := dist
LDFLAGS := -s -w

.PHONY: build test lint check clean validate-config release install verify-unit

# Build target (C-free static binary)
build:
	CGO_ENABLED=0 go build -o $(BINARY) ./cmd/vshm

# Test target
test:
	go test -race ./...

# Lint target
lint:
	go vet ./...

# Combined static checks + tests
check: lint test

# Clean target
clean:
	rm -f $(BINARY)
	rm -rf $(DIST)
	go clean

# Validate configuration
validate-config: build
	./$(BINARY) validate-config

# Linux amd64 release artifact (spec §8 release gate): CGO_ENABLED=0, C-free,
# installs as /usr/bin/vshm.
release:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o $(DIST)/vshm-linux-amd64 ./cmd/vshm

# Install release binary and systemd unit (DESTDIR-aware for packaging).
# Run validate-config and validate-matrix, then enable/start the service.
install: release
	install -D -m 0755 $(DIST)/vshm-linux-amd64 $(DESTDIR)/usr/bin/vshm
	install -D -m 0644 packaging/vshm.service $(DESTDIR)/usr/lib/systemd/system/vshm.service

# Verify the packaged unit with systemd-analyze where available.
verify-unit:
	systemd-analyze verify packaging/vshm.service
