PLUGIN_ID := codex-tool-output-normalizer
DIST_DIR := $(CURDIR)/dist
GO_IMAGE ?= golang:1.26-bookworm

UNAME_S := $(shell uname -s)
ifeq ($(UNAME_S),Darwin)
NATIVE_EXT := dylib
else
NATIVE_EXT := so
endif

.PHONY: test build smoke strict linux-amd64 clean

test:
	go test ./...

build: test
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=1 go build -buildmode=c-shared -o $(DIST_DIR)/$(PLUGIN_ID).$(NATIVE_EXT) .
	rm -f $(DIST_DIR)/$(PLUGIN_ID).h

smoke: build
	python3 scripts/abi_smoke_test.py $(DIST_DIR)/$(PLUGIN_ID).$(NATIVE_EXT)

strict: build
	python3 scripts/abi_strict_test.py $(DIST_DIR)/$(PLUGIN_ID).$(NATIVE_EXT)

linux-amd64:
	mkdir -p $(DIST_DIR)
	docker run --rm \
		-v "$(CURDIR):/src" \
		-w /src \
		-e CGO_ENABLED=1 \
		-e GOOS=linux \
		-e GOARCH=amd64 \
		-e GOPROXY=https://goproxy.cn,direct \
		$(GO_IMAGE) \
		sh -c 'go test ./... && go build -buildmode=c-shared -o /src/dist/$(PLUGIN_ID).so . && rm -f /src/dist/$(PLUGIN_ID).h'

clean:
	rm -rf $(DIST_DIR)
