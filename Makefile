.PHONY: build test vet check clean

build:
	CGO_ENABLED=0 go build -trimpath -o bin/x-pull ./cmd/x-pull

test:
	go test -race ./...

vet:
	go vet ./...

check:
	go run ./cmd/x-pull -config configs/config.example.json -check

clean:
	rm -rf bin
