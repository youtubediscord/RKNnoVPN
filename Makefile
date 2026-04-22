# PrivStack — Local Build Commands
# Requires: Go 1.22+, Android SDK, JDK 17

SINGBOX_VERSION := 1.13.8
VERSION := $(shell git describe --tags --always 2>/dev/null || echo "dev")
OUT_DIR := out
MODULE_DIR := module

.PHONY: all daemon daemon-arm64 daemon-armv7 singbox singbox-arm64 singbox-armv7 apk module clean

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

# === Download sing-box ===
singbox: singbox-arm64 singbox-armv7
	@echo "  -> $(OUT_DIR)/arm64/sing-box"
	@echo "  -> $(OUT_DIR)/armv7/sing-box"

singbox-arm64:
	@echo "=== Downloading sing-box v$(SINGBOX_VERSION) (Android arm64) ==="
	@mkdir -p $(OUT_DIR)/arm64
	curl -fSL "https://github.com/SagerNet/sing-box/releases/download/v$(SINGBOX_VERSION)/sing-box-$(SINGBOX_VERSION)-android-arm64.tar.gz" \
		-o /tmp/singbox-arm64.tar.gz
	tar xzf /tmp/singbox-arm64.tar.gz -C /tmp/
	cp /tmp/sing-box-$(SINGBOX_VERSION)-android-arm64/sing-box $(OUT_DIR)/arm64/sing-box
	chmod 755 $(OUT_DIR)/arm64/sing-box

singbox-armv7:
	@echo "=== Downloading sing-box v$(SINGBOX_VERSION) (Android arm / armeabi-v7a) ==="
	@mkdir -p $(OUT_DIR)/armv7
	curl -fSL "https://github.com/SagerNet/sing-box/releases/download/v$(SINGBOX_VERSION)/sing-box-$(SINGBOX_VERSION)-android-arm.tar.gz" \
		-o /tmp/singbox-armv7.tar.gz
	tar xzf /tmp/singbox-armv7.tar.gz -C /tmp/
	cp /tmp/sing-box-$(SINGBOX_VERSION)-android-arm/sing-box $(OUT_DIR)/armv7/sing-box
	chmod 755 $(OUT_DIR)/armv7/sing-box

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
