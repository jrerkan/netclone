VERSION ?= 1.0.0
BIN     := bin

.PHONY: all build test vet fmt iso clean

all: build

build:
	@mkdir -p $(BIN)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o $(BIN)/netclone-server ./cmd/netclone-server
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o $(BIN)/netclone-client ./cmd/netclone-client

test:
	go test ./internal/nci

vet:
	GOOS=linux GOARCH=amd64 go vet ./...

fmt:
	gofmt -w .

iso:
	bash build/build-iso.sh

clean:
	rm -rf $(BIN) out
	cd build/live && rm -rf chroot binary .build \
		config/binary config/chroot config/bootstrap config/common config/source \
		live-image-amd64.iso live-image-amd64.hybrid.iso
