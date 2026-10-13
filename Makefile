# microvms Makefile
HOST ?= 192.168.0.118
USER ?= $(shell whoami)
TARGET ?= $(USER)@$(HOST)

.PHONY: build test vet image assets deploy redeploy preflight clean

build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o bin/microvmsd ./cmd/microvmsd

test:
	go test ./...

vet:
	go vet ./...

# guest rootfs + firecracker kernel (needs Docker; kernel needs network)
assets:
	scripts/install-kernel.sh images
	scripts/build-image.sh images/rootfs.ext4

image:
	scripts/build-image.sh images/rootfs.ext4

deploy:
	scripts/deploy.sh $(TARGET) --full

# fast iteration: rebuild + ship binary + restart daemon (no image/kernel upload)
# one ssh session with sudo (password prompted once)
redeploy:
	$(MAKE) build
	scp -q bin/microvmsd $(TARGET):/tmp/microvmsd
	ssh -t $(TARGET) "sudo install -m 0755 /tmp/microvmsd /usr/local/bin/microvmsd && sudo systemctl restart microvmsd"

# host checks need no root: /dev/kvm writability is reported for the login user
preflight:
	ssh $(TARGET) "bash -s" < scripts/preflight.sh

clean:
	rm -rf bin dev-data
