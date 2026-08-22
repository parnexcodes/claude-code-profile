.PHONY: build install vet fmt clean test test-race

build:
	go build -o ccp ./cmd/ccp

install: build
	mkdir -p ~/.local/bin
	install -m 0755 ccp ~/.local/bin/ccp

vet:
	go vet ./...

fmt:
	gofmt -w cmd internal .

test:
	go test ./... -count=1

test-race:
	go test ./... -count=1 -race

clean:
	rm -f ccp
