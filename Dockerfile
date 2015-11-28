FROM golang:1.5.1

RUN go get  github.com/golang/lint/golint \
            golang.org/x/tools/cmd/vet \
            github.com/mattn/goveralls \
            golang.org/x/tools/cover \
            github.com/tools/godep \
	          github.com/axw/gocov/gocov

ENV USER root
WORKDIR /go/src/github.com/yuuki1/dochroot

ADD . /go/src/github.com/yuuki1/dochroot
