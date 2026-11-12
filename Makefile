.PHONY: build test vet
build:
	go build -o gha-stranded ./cmd/gha-stranded
test:
	go test ./...
vet:
	go vet ./...
