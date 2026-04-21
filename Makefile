# PrivStack — Local Build Commands
# Requires: Go 1.22+, Android SDK, JDK 17

SINGBOX_VERSION := 1.13.8
VERSION := $(shell git describe --tags --always 2>/dev/null || echo "dev")
OUT_DIR := out
MODULE_DIR := module

.PHONY: all daemon apk module clean

all: daemon singbox module apk
	@echo "Build complete. Artifacts in $(OUT_DIR)/"

# === Go Daemon (arm64) ===
daemon:
	@echo "=== Building daemon (arm64) ==="
	@mkdir -p $(OUT_DIR)
	cd daemon && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build \
		-ldflags="-s -w -X main.Version=$(VERSION)" \
		-o ../$(OUT_DIR)/privd ./cmd/privd
	cd daemon && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build \
		-ldflags="-s -w -X main.Version=$(VERSION)" \
		-o ../$(OUT_DIR)/privctl ./cmd/privctl
	@echo "  -> $(OUT_DIR)/privd, $(OUT_DIR)/privctl"

# === Download sing-box ===
singbox:
	@echo "=== Downloading sing-box v$(SINGBOX_VERSION) ==="
	@mkdir -p $(OUT_DIR)
	curl -fSL "https://github.com/SagerNet/sing-box/releases/download/v$(SINGBOX_VERSION)/sing-box-$(SINGBOX_VERSION)-linux-arm64.tar.gz" \
		-o /tmp/singbox.tar.gz
	tar xzf /tmp/singbox.tar.gz -C /tmp/
	cp /tmp/sing-box-$(SINGBOX_VERSION)-linux-arm64/sing-box $(OUT_DIR)/sing-box
	chmod 755 $(OUT_DIR)/sing-box
	@echo "  -> $(OUT_DIR)/sing-box"

# === Magisk Module ZIP ===
module: daemon singbox
	@echo "=== Building Magisk module ZIP ==="
	@mkdir -p $(MODULE_DIR)/binaries/arm64
	cp $(OUT_DIR)/privd $(OUT_DIR)/privctl $(OUT_DIR)/sing-box $(MODULE_DIR)/binaries/arm64/
	chmod 755 $(MODULE_DIR)/binaries/arm64/*
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
