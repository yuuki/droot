FROM golang:1.5.1

RUN go get  github.com/golang/lint/golint \
            golang.org/x/tools/cmd/vet \
            github.com/mattn/goveralls \
            golang.org/x/tools/cover \
            github.com/tools/godep \
	          github.com/axw/gocov/gocov \
            github.com/laher/goxc \
	          golang.org/x/tools/cmd/stringer \
	          github.com/golang/mock/mockgen

ENV USER root
WORKDIR /go/src/github.com/yuuki1/droot

ADD . /go/src/github.com/yuuki1/droot
