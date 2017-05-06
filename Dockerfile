FROM golang:1.8.1

RUN go get  github.com/laher/goxc \
	          golang.org/x/tools/cmd/stringer \
	          github.com/golang/mock/mockgen

ENV USER root
WORKDIR /go/src/github.com/yuuki/droot

ADD . /go/src/github.com/yuuki/droot
