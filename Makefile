GO ?= go
LDFLAGS = -s -w

# The Lab release a build says it is (release/build.sh passes the tag), and
# the release key lab-agent carries: the one in release/allowed_signers.
LAB_RELEASE ?= dev
RELEASE_KEY ?= $(shell awk 'NF { print $$(NF-1), $$NF; exit }' release/allowed_signers)
MODULE = github.com/omacom/omarchy-maclab
STAMP = -X main.version=$(LAB_RELEASE)
AGENT_STAMP = -X $(MODULE)/internal/agent.LabRelease=$(LAB_RELEASE) -X '$(MODULE)/internal/agent.ReleaseKey=$(RELEASE_KEY)'

all:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -ldflags '$(LDFLAGS)' -o bin/labd ./cmd/labd
	CGO_ENABLED=0 $(GO) build -ldflags '$(LDFLAGS)' -o bin/lab ./cmd/lab
	CGO_ENABLED=0 $(GO) build -ldflags '$(LDFLAGS)' -o bin/lab-loop ./cmd/lab-loop
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -ldflags "$(LDFLAGS) $(AGENT_STAMP)" -o bin/lab-agent-linux-arm64 ./cmd/lab-agent
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -ldflags '$(LDFLAGS)' -o bin/lab-linux-arm64 ./cmd/lab
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 $(GO) build -ldflags '$(LDFLAGS)' -o bin/oobd-darwin-arm64 ./cmd/oobd
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 $(GO) build -ldflags '$(LDFLAGS)' -o bin/lab-builder-darwin-arm64 ./cmd/lab-builder

test:
	$(GO) vet ./...
	$(GO) test -race ./...
	python3 -B -m unittest discover -s builder -p 'test_*.py'

.PHONY: all test

# The Go commands of a Lab release, cross-compiled for release/build.sh: lab
# for its three machines, and the mini's bundle (labd, oobd, lab-builder and
# the lab-agent handed to Macs). lab-loop, lab-esp, lab-workflow and
# lab-recovery-default are not shipped: whoever needs one builds it from source.
RELEASE_OUT ?= dist/unsigned
RELEASE_BUILD = CGO_ENABLED=0 $(GO) build -trimpath
SERVER_BIN = $(RELEASE_OUT)/.stage/lab-server/bin
release-binaries:
	mkdir -p "$(SERVER_BIN)"
	GOOS=darwin GOARCH=arm64 $(RELEASE_BUILD) -ldflags "$(LDFLAGS) $(STAMP)" -o "$(RELEASE_OUT)/lab-darwin-arm64" ./cmd/lab
	GOOS=linux GOARCH=arm64 $(RELEASE_BUILD) -ldflags "$(LDFLAGS) $(STAMP)" -o "$(RELEASE_OUT)/lab-linux-arm64" ./cmd/lab
	GOOS=linux GOARCH=amd64 $(RELEASE_BUILD) -ldflags "$(LDFLAGS) $(STAMP)" -o "$(RELEASE_OUT)/lab-linux-amd64" ./cmd/lab
	GOOS=darwin GOARCH=arm64 $(RELEASE_BUILD) -ldflags "$(LDFLAGS) $(STAMP)" -o "$(SERVER_BIN)/labd" ./cmd/labd
	GOOS=darwin GOARCH=arm64 $(RELEASE_BUILD) -ldflags "$(LDFLAGS)" -o "$(SERVER_BIN)/oobd" ./cmd/oobd
	GOOS=darwin GOARCH=arm64 $(RELEASE_BUILD) -ldflags "$(LDFLAGS)" -o "$(SERVER_BIN)/lab-builder" ./cmd/lab-builder
	GOOS=linux GOARCH=arm64 $(RELEASE_BUILD) -ldflags "$(LDFLAGS) $(AGENT_STAMP)" -o "$(SERVER_BIN)/lab-agent-linux-arm64" ./cmd/lab-agent

.PHONY: release-binaries

# Produces a private local install prefix. Never installs a service or contacts a Mac.
BOOT_TEST_OUT ?= dist/boot-test
boot-test-dist:
	test ! -e "$(BOOT_TEST_OUT)" || { echo "Refusing existing boot-test output prefix" >&2; exit 1; }
	mkdir -p "$(BOOT_TEST_OUT)/bin"
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o "$(BOOT_TEST_OUT)/bin/lab" ./cmd/lab
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o "$(BOOT_TEST_OUT)/bin/lab-workflow" ./cmd/lab-workflow
	python3 -E -B scripts/boot-test/bundle.py --output "$(BOOT_TEST_OUT)/frontend-resources.tar.gz"
	tar -xzf "$(BOOT_TEST_OUT)/frontend-resources.tar.gz" -C "$(BOOT_TEST_OUT)"

.PHONY: boot-test-dist

# Model-checks the TLA+ specs under specs/ (see specs/README.md). Needs java.
TLA2TOOLS_VERSION = 1.7.4
TLA2TOOLS_SHA256 = 936a262061c914694dfd669a543be24573c45d5aa0ff20a8b96b23d01e050e88
TLA2TOOLS = specs/.tools/tla2tools.jar

$(TLA2TOOLS):
	mkdir -p $(dir $@)
	curl -fsSL -o $@.tmp https://github.com/tlaplus/tlaplus/releases/download/v$(TLA2TOOLS_VERSION)/tla2tools.jar
	echo "$(TLA2TOOLS_SHA256)  $@.tmp" | sha256sum -c -
	mv $@.tmp $@

spec: $(TLA2TOOLS)
	TLA2TOOLS=$(abspath $(TLA2TOOLS)) specs/labboot/check.sh

.PHONY: spec
