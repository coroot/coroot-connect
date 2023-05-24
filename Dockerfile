FROM golang:1.19-bullseye AS builder

WORKDIR /go/src/coroot-connect

COPY go.mod .
COPY go.sum .
RUN go mod download

COPY . .
ARG VERSION=unknown
RUN CGO_ENABLED=0 go install -mod=readonly -ldflags "-X main.version=$VERSION" .


FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /go/bin/coroot-connect /coroot-connect
CMD ["/coroot-connect"]
