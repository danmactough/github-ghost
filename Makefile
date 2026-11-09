BINARY := github-ghost
BUILD_DIR := bin
PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin

.PHONY: build test vet fmt install uninstall clean

build:
	go build -o $(BUILD_DIR)/$(BINARY) .

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

install: build
	install -d $(BINDIR)
	install -m 755 $(BUILD_DIR)/$(BINARY) $(BINDIR)/$(BINARY)

uninstall:
	rm -f $(BINDIR)/$(BINARY)

clean:
	rm -rf $(BUILD_DIR)
