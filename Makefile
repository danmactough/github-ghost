BINARY := github-ghost
BUILD_DIR := bin
PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
DIST_DIR ?= dist/$(VERSION)
LDFLAGS := -s -w -X main.version=$(VERSION)
PLATFORMS := darwin/arm64 darwin/amd64 linux/arm64 linux/amd64

.PHONY: build test vet fmt install uninstall clean dist release check-release

build:
	go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY) .

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
	rm -rf $(BUILD_DIR) dist

dist:
	mkdir -p $(DIST_DIR)
	set -e; for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		name=$(BINARY)_$(VERSION)_$${os}_$${arch}; \
		mkdir -p $(DIST_DIR)/$$name; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/$$name/$(BINARY) .; \
		cp README.md $(DIST_DIR)/$$name/; \
		tar -czf $(DIST_DIR)/$$name.tar.gz -C $(DIST_DIR) $$name; \
	done
	cd $(DIST_DIR) && shasum -a 256 *.tar.gz > checksums.txt

check-release:
	@echo "$(VERSION)" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$$' || { echo "usage: make release VERSION=vX.Y.Z (got: $(VERSION))"; exit 1; }
	@test "$$(git branch --show-current)" = main || { echo "release must run on main"; exit 1; }
	@test -z "$$(git status --porcelain)" || { echo "working tree has uncommitted changes"; exit 1; }
	@! git rev-parse -q --verify "refs/tags/$(VERSION)" >/dev/null || { echo "tag $(VERSION) already exists"; exit 1; }
	@test -z "$$(git ls-remote --tags origin "refs/tags/$(VERSION)")" || { echo "tag $(VERSION) already exists on origin"; exit 1; }

release: check-release vet test dist
	git tag -a $(VERSION) -m "$(VERSION)"
	git push origin refs/tags/$(VERSION)
	gh release create $(VERSION) $(DIST_DIR)/*.tar.gz $(DIST_DIR)/checksums.txt --repo danmactough/github-ghost --title $(VERSION) --generate-notes --verify-tag
