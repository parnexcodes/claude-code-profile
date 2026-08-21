.PHONY: build install vet fmt clean

build:
	go build -o ccp .

install: build
	mkdir -p ~/.local/bin
	install -m 0755 ccp ~/.local/bin/ccp

vet:
	go vet ./...

fmt:
	gofmt -w .

clean:
	rm -f ccp
