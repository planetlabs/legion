FROM golang:1.11-alpine3.8 AS build

RUN apk update && apk add git curl

WORKDIR /go/src/code.earth.planet.com/product/legion
COPY . .

RUN curl -sSL -o /dep https://github.com/golang/dep/releases/download/v0.5.0/dep-linux-amd64 && chmod +x /dep
RUN /dep ensure
RUN go build -o /legion ./cmd/legion

FROM alpine:3.8

RUN apk update && apk add ca-certificates
COPY --from=build /legion /legion
