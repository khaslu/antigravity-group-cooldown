.PHONY: build test clean

SO_OUT := bin/antigravity-group-cooldown.so

all: test build

test:
	go test -v ./...

build:
	@mkdir -p bin
	go build -buildmode=c-shared -o $(SO_OUT) .
	@echo "Build complete: $(SO_OUT)"
	@ls -lh $(SO_OUT)

clean:
	rm -rf bin/ /tmp/antigravity-group-cooldown.so /tmp/antigravity-group-cooldown.h
