FROM golang

ADD . /go/src/github.com/d4l3k/comic-starship

RUN go install github.com/d4l3k/comic-starship

ENTRYPOINT /go/bin/comic-starship

EXPOSE 8282
