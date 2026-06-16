APP := personality

.PHONY: build run test tidy docker

build:
	go build ./...

run:
	go run ./cmd

test:
	go test ./...

tidy:
	go mod tidy

docker:
	docker build -t $(APP) .
