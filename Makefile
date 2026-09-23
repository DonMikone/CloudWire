ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))
VERSION ?= $(shell git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//' | grep . || echo 0.1.0)
BUILD ?= $(shell git rev-list --count HEAD 2>/dev/null || echo 1)
CONFIGURATION ?= Release
SDK := $(shell xcrun --sdk macosx --show-sdk-path 2>/dev/null)
GO_TAGS := cmount libsqlite3 sqlite_omit_load_extension
GO_CGO_CFLAGS := -mmacosx-version-min=14.0 -I$(SDK)/usr/include -I$(ROOT)/core/third_party/fuse/include
GO_CGO_LDFLAGS := -mmacosx-version-min=14.0 -L$(SDK)/usr/lib

export VERSION BUILD

.PHONY: core core-dev icons xcodeproj app dmg test e2e clean

core:
	scripts/build-core.sh release universal

core-dev:
	scripts/build-core.sh debug native

icons:
	scripts/make-icons.sh

xcodeproj:
	xcodegen generate --spec app/project.yml

app: core xcodeproj
	xcodebuild -project app/CloudWire.xcodeproj -scheme CloudWire -configuration $(CONFIGURATION) \
		-derivedDataPath build/DerivedData ARCHS="arm64 x86_64" ONLY_ACTIVE_ARCH=NO \
		MARKETING_VERSION=$(VERSION) CURRENT_PROJECT_VERSION=$(BUILD) build
	rm -rf build/CloudWire.app
	ditto build/DerivedData/Build/Products/$(CONFIGURATION)/CloudWire.app build/CloudWire.app

dmg:
	scripts/make-dmg.sh

test: xcodeproj
	cd core && CGO_CFLAGS="$(GO_CGO_CFLAGS)" CGO_LDFLAGS="$(GO_CGO_LDFLAGS)" go vet -tags "$(GO_TAGS)" ./...
	cd core && CGO_CFLAGS="$(GO_CGO_CFLAGS)" CGO_LDFLAGS="$(GO_CGO_LDFLAGS)" go test -race -tags "$(GO_TAGS)" ./...
	xcodebuild test -project app/CloudWire.xcodeproj -scheme CloudWireKitTests -destination 'platform=macOS' \
		-derivedDataPath build/DerivedData

e2e:
	scripts/e2e-nextcloud.sh

clean:
	rm -rf build
