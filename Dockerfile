FROM golang:1.17-stretch AS builder

WORKDIR /go/src/promtun

COPY go.mod .
COPY go.sum .
RUN go mod download

COPY . .
ARG VERSION=unknown
RUN CGO_ENABLED=0 go install -mod=readonly -ldflags "-X main.version=$VERSION" .


FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /go/bin/promtun /promtun
CMD ["/promtun"]
