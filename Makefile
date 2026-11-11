# 构建:make(本机)/ make dist(linux amd64+arm64,静态,写入版本号)/ make test
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -buildid= \
  -X github.com/domoxiaojun/Meterly/internal/version.Version=$(VERSION) \
  -X github.com/domoxiaojun/Meterly/internal/version.Commit=$(COMMIT) \
  -X github.com/domoxiaojun/Meterly/internal/version.Date=$(DATE)
GOFLAGS := -trimpath
BINS    := meterly-agent meterly-server
ARCHS   := amd64 arm64

.PHONY: all build dist test test-short vet clean checksums

all: build

build:
	@mkdir -p build
	@for b in $(BINS) mockclash demoseed; do \
	  CGO_ENABLED=0 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o build/$$b ./cmd/$$b || exit 1; done
	@echo "已构建 build/ ($(VERSION))"

dist:
	@mkdir -p dist
	@for a in $(ARCHS); do for b in $(BINS); do \
	  echo "→ dist/$$b-linux-$$a"; \
	  CGO_ENABLED=0 GOOS=linux GOARCH=$$a go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o dist/$$b-linux-$$a ./cmd/$$b || exit 1; \
	done; done
	@$(MAKE) --no-print-directory checksums

checksums:
	@cd dist && sha256sum meterly-* | grep -v SHA256SUMS > SHA256SUMS && cat SHA256SUMS

vet:
	go vet ./...

test: vet
	go test -count=1 ./...

test-short: vet
	go test -count=1 -short ./...

clean:
	rm -rf build dist
