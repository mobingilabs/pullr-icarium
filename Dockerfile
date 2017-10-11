FROM golang

ADD . /go/src/pullr-icarium
WORKDIR /go/src/pullr-icarium
RUN go build -v
ENTRYPOINT ["/go/src/pullr-icarium/pullr-icarium"]
