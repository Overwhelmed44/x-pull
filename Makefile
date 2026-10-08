.PHONY: build test vet check docker-build clean

build:
	CGO_ENABLED=0 go build -trimpath -o bin/x-pull ./cmd/x-pull
	CGO_ENABLED=0 go build -trimpath -o bin/x-pull-container ./cmd/x-pull-container

docker-build:
	docker build -t x-pull:local .

test:
	go test -race ./...

vet:
	go vet ./...

check:
	go run ./cmd/x-pull -config configs/config.example.json -check

clean:
	rm -rf bin
