BIN = droot

all: clean build test

test: testdeps
	go test -v ./...

gen:
	go get github.com/vektra/mockery/.../
	go generate ./...
	mockery -all -inpkg

build: deps gen
	go build -o $(BIN) ./cmd

fmt: deps
	gofmt -s -w .

LINT_RET = .golint.txt
lint: testdeps
	go vet ./...
	rm -f $(LINT_RET)
	golint ./... | tee .golint.txt
	test ! -s $(LINT_RET)

patch: gobump
	./script/release.sh patch

minor: gobump
	./script/release.sh minor

gobump:
	go get github.com/motemen/gobump/cmd/gobump

deps:
	go get -d -v ./...

testdeps:
	go get -d -v -t ./...
	go get github.com/golang/lint/golint
	go get golang.org/x/tools/cmd/cover
	go get github.com/axw/gocov/gocov
	go get github.com/mattn/goveralls

clean:
	go clean

.PHONY: test build lint patch minor gobump deps testdeps clean
