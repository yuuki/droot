FROM golang:1.6.0

RUN go get  github.com/golang/lint/golint \
            github.com/mattn/goveralls \
            golang.org/x/tools/cover \
            github.com/tools/godep \
	          github.com/axw/gocov/gocov \
            github.com/laher/goxc \
	          golang.org/x/tools/cmd/stringer \
	          github.com/golang/mock/mockgen

ENV USER root
WORKDIR /go/src/github.com/yuuki/droot

ADD . /go/src/github.com/yuuki/droot
