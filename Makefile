# PrivStack — Local Build Commands
# Requires: Go 1.22+, Android SDK, JDK 17

SINGBOX_VERSION ?= latest
SINGBOX_RESOLVED_VERSION := $(shell if [ "$(SINGBOX_VERSION)" = "latest" ]; then gh release list --repo SagerNet/sing-box --limit 1 --json tagName --jq '.[0].tagName' 2>/dev/null | sed 's/^v//'; else echo "$(SINGBOX_VERSION)" | sed 's/^v//'; fi)
VERSION := $(shell git describe --tags --always 2>/dev/null || echo "dev")
OUT_DIR := out
MODULE_DIR := module
SINGBOX_SRC_DIR := /tmp/sing-box-$(SINGBOX_RESOLVED_VERSION)
SINGBOX_TAGS := with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_clash_api,badlinkname,tfogo_checklinkname0
SINGBOX_LDFLAGS := -X 'github.com/sagernet/sing-box/constant.Version=$(SINGBOX_RESOLVED_VERSION)' -X 'internal/godebug.defaultGODEBUG=multipathtcp=0' -checklinkname=0 -s -w -buildid=

.PHONY: all daemon daemon-arm64 daemon-armv7 singbox singbox-src singbox-arm64 singbox-armv7 apk module clean

all: daemon singbox module apk
	@echo "Build complete. Artifacts in $(OUT_DIR)/"

# === Go Daemon (Android ABIs) ===
daemon: daemon-arm64 daemon-armv7
	@echo "  -> $(OUT_DIR)/arm64/privd, $(OUT_DIR)/arm64/privctl"
	@echo "  -> $(OUT_DIR)/armv7/privd, $(OUT_DIR)/armv7/privctl"

daemon-arm64:
	@echo "=== Building daemon (arm64) ==="
	@mkdir -p $(OUT_DIR)/arm64
	cd daemon && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build \
		-ldflags="-s -w -X main.Version=$(VERSION)" \
		-o ../$(OUT_DIR)/arm64/privd ./cmd/privd
	cd daemon && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build \
		-ldflags="-s -w -X main.Version=$(VERSION)" \
		-o ../$(OUT_DIR)/arm64/privctl ./cmd/privctl

daemon-armv7:
	@echo "=== Building daemon (armv7 / armeabi-v7a) ==="
	@mkdir -p $(OUT_DIR)/armv7
	cd daemon && CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build \
		-ldflags="-s -w -X main.Version=$(VERSION)" \
		-o ../$(OUT_DIR)/armv7/privd ./cmd/privd
	cd daemon && CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build \
		-ldflags="-s -w -X main.Version=$(VERSION)" \
		-o ../$(OUT_DIR)/armv7/privctl ./cmd/privctl

# === Build static sing-box ===
singbox: singbox-arm64 singbox-armv7
	@echo "  -> $(OUT_DIR)/arm64/sing-box"
	@echo "  -> $(OUT_DIR)/armv7/sing-box"

singbox-src:
	@if [ -z "$(SINGBOX_RESOLVED_VERSION)" ]; then \
		echo "Failed to resolve sing-box release. Set SINGBOX_VERSION explicitly, e.g. make singbox SINGBOX_VERSION=1.14.0-alpha.16"; \
		exit 1; \
	fi
	@if [ ! -d "$(SINGBOX_SRC_DIR)/.git" ]; then \
		echo "=== Fetching sing-box v$(SINGBOX_RESOLVED_VERSION) source ==="; \
		rm -rf "$(SINGBOX_SRC_DIR)"; \
		git clone --depth 1 --branch "v$(SINGBOX_RESOLVED_VERSION)" https://github.com/SagerNet/sing-box.git "$(SINGBOX_SRC_DIR)"; \
	fi

singbox-arm64: singbox-src
	@echo "=== Building static sing-box v$(SINGBOX_RESOLVED_VERSION) (arm64) ==="
	@mkdir -p $(OUT_DIR)/arm64
	cd $(SINGBOX_SRC_DIR) && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build \
		-trimpath -tags "$(SINGBOX_TAGS)" \
		-ldflags "$(SINGBOX_LDFLAGS)" \
		-o $(abspath $(OUT_DIR))/arm64/sing-box ./cmd/sing-box
	chmod 755 $(OUT_DIR)/arm64/sing-box
	@if readelf -l $(OUT_DIR)/arm64/sing-box | grep -q "Requesting program interpreter"; then \
		echo "sing-box arm64 is dynamically linked; refusing to package it"; exit 1; \
	fi

singbox-armv7: singbox-src
	@echo "=== Building static sing-box v$(SINGBOX_RESOLVED_VERSION) (armv7 / armeabi-v7a) ==="
	@mkdir -p $(OUT_DIR)/armv7
	cd $(SINGBOX_SRC_DIR) && CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build \
		-trimpath -tags "$(SINGBOX_TAGS)" \
		-ldflags "$(SINGBOX_LDFLAGS)" \
		-o $(abspath $(OUT_DIR))/armv7/sing-box ./cmd/sing-box
	chmod 755 $(OUT_DIR)/armv7/sing-box
	@if readelf -l $(OUT_DIR)/armv7/sing-box | grep -q "Requesting program interpreter"; then \
		echo "sing-box armv7 is dynamically linked; refusing to package it"; exit 1; \
	fi

# === Magisk Module ZIP ===
module: daemon singbox
	@echo "=== Building Magisk module ZIP ==="
	@mkdir -p $(MODULE_DIR)/binaries/arm64 $(MODULE_DIR)/binaries/armv7
	cp $(OUT_DIR)/arm64/privd $(OUT_DIR)/arm64/privctl $(OUT_DIR)/arm64/sing-box $(MODULE_DIR)/binaries/arm64/
	cp $(OUT_DIR)/armv7/privd $(OUT_DIR)/armv7/privctl $(OUT_DIR)/armv7/sing-box $(MODULE_DIR)/binaries/armv7/
	chmod 755 $(MODULE_DIR)/binaries/arm64/*
	chmod 755 $(MODULE_DIR)/binaries/armv7/*
	sed -i "s/^version=.*/version=$(VERSION)/" $(MODULE_DIR)/module.prop
	cd $(MODULE_DIR) && zip -r ../$(OUT_DIR)/privstack-$(VERSION)-module.zip . \
		-x "*.git*" "*.DS_Store"
	@echo "  -> $(OUT_DIR)/privstack-$(VERSION)-module.zip"

# === Android APK ===
apk:
	@echo "=== Building APK ==="
	cd app && ./gradlew assembleDebug --no-daemon
	cp app/app/build/outputs/apk/debug/*.apk $(OUT_DIR)/privstack-$(VERSION)-panel.apk
	@echo "  -> $(OUT_DIR)/privstack-$(VERSION)-panel.apk"

# === Test daemon (host arch, for development) ===
daemon-host:
	@echo "=== Building daemon (host arch, for testing) ==="
	cd daemon && go build -o ../$(OUT_DIR)/privd-host ./cmd/privd
	cd daemon && go build -o ../$(OUT_DIR)/privctl-host ./cmd/privctl

# === Go tests ===
test:
	cd daemon && go test ./...

# === Clean ===
clean:
	rm -rf $(OUT_DIR)
	rm -rf $(MODULE_DIR)/binaries
	cd app && ./gradlew clean 2>/dev/null || true
