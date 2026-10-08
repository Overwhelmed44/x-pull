.PHONY: build test vet docker-build clean

build:
	CGO_ENABLED=0 go build -trimpath -o bin/x-pull ./cmd/x-pull

test:
	go test ./tests

vet:
	go vet ./...

docker-build:
	docker build -t x-pull:local .

clean:
	rm -rf bin
