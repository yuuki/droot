BIN = droot

all: clean build test

dep:
	glide install
	rm -fr vendor/github.com/docker/docker/vendor/golang.org/x/net

test:
	go test -v $$(go list ./... | grep -v vendor)

build:
	CGO_ENABLED=1 go build -o $(BIN) ./cmd/droot/.../

fmt:
	gofmt -s -w $$(git ls | grep -e '\.go$$' | grep -v vendor)

vet:
	go vet $$(go list ./... | grep -v vendor)

lint:
	golint $$(go list ./... | grep -v vendor)

patch: gobump
	./script/release.sh patch

minor: gobump
	./script/release.sh minor

gobump:
	go get github.com/motemen/gobump/cmd/gobump

clean:
	go clean

.PHONY: test build lint patch minor gobump deps testdeps clean
