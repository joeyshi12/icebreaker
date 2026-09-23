BINARY := icebreaker
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test race vet fmt run clean

build:
	go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY) ./cmd/icebreaker

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

# STUN only; add TURN_SECRET and TURN_URLS to advertise a relay
run: build
	./$(BINARY)

clean:
	rm -f $(BINARY)
