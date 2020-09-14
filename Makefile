BIN = droot

.PHONY: test
test: vet
	go test -v $$(go list ./... | grep -v vendor)

.PHONY: build
build:
	CGO_ENABLED=1 go build -o $(BIN) ./cmd/droot/.../

.PHONY: fmt
fmt:
	gofmt -s -w $$(git ls | grep -e '\.go$$' | grep -v vendor)

.PHONY: vet
vet:
	go vet $$(go list ./... | grep -v vendor)

.PHONY: lint
lint:
	golint $$(go list ./... | grep -v vendor)

.PHONY: patch
patch: gobump
	./script/release.sh patch

.PHONY: minor
minor: gobump
	./script/release.sh minor

.PHONY: gobump
gobump:
	go get github.com/motemen/gobump/cmd/gobump
