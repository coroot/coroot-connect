package main

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"github.com/hashicorp/yamux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

var projectToken = "b8ea8af6-ffee-44b3-aa9a-1fc02233cfb7"

func init() {
	timeout = time.Second
	tlsSkipVerify = true
}

func TestHandshakeTimeout(t *testing.T) {
	addr, stop := gateway(t, func(listener net.Listener) {
		// doesn't accept connections
	})
	defer stop()
	_, err := connect(addr, "", projectToken)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "context deadline exceeded")
}

func TestHandshakeError(t *testing.T) {
	addr, stop := gateway(t, func(listener net.Listener) {
		conn, err := listener.Accept()
		require.NoError(t, err)
		readToken(t, conn)
		writeStatus(t, conn, 500)
	})
	defer stop()
	_, err := connect(addr, "", projectToken)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to authenticate project")
}

func TestProxy(t *testing.T) {
	endpointCh := make(chan string)
	addr, stop := gateway(t, func(listener net.Listener) {
		conn, err := listener.Accept()
		require.NoError(t, err)
		readToken(t, conn)
		writeStatus(t, conn, 200)

		endpoint, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		endpointCh <- endpoint.Addr().String()

		cfg := yamux.DefaultConfig()
		cfg.KeepAliveInterval = time.Second
		session, err := yamux.Client(conn, cfg)
		require.NoError(t, err)
		clientConn, err := endpoint.Accept()
		require.NoError(t, err)
		tunConn, err := session.Open()
		require.NoError(t, err)
		go func() {
			io.Copy(clientConn, tunConn)
		}()
		io.Copy(tunConn, clientConn)
	})
	defer stop()

	prometheus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "Prometheus is Healthy.")
	}))
	defer prometheus.Close()

	gwConn, err := connect(addr, "", projectToken)
	go func() {
		proxy(context.Background(), gwConn, prometheus.Listener.Addr().String())
	}()

	endpointAddr := <-endpointCh
	res, err := http.Get("http://" + endpointAddr + "/-/healthy")
	require.NoError(t, err)
	data, err := ioutil.ReadAll(res.Body)
	require.NoError(t, err)
	res.Body.Close()
	assert.Equal(t, "Prometheus is Healthy.", string(data))
}

func readToken(t *testing.T, conn net.Conn) {
	buf := make([]byte, 36)
	_, err := conn.Read(buf)
	require.NoError(t, err)
	require.Equal(t, projectToken, string(buf))
}

func writeStatus(t *testing.T, conn net.Conn, status uint16) {
	require.NoError(t, binary.Write(conn, binary.LittleEndian, status))
}

func gateway(t *testing.T, handler func(g net.Listener)) (string, func()) {
	localhostCert := `-----BEGIN CERTIFICATE-----
MIICEzCCAXygAwIBAgIQMIMChMLGrR+QvmQvpwAU6zANBgkqhkiG9w0BAQsFADAS
MRAwDgYDVQQKEwdBY21lIENvMCAXDTcwMDEwMTAwMDAwMFoYDzIwODQwMTI5MTYw
MDAwWjASMRAwDgYDVQQKEwdBY21lIENvMIGfMA0GCSqGSIb3DQEBAQUAA4GNADCB
iQKBgQDuLnQAI3mDgey3VBzWnB2L39JUU4txjeVE6myuDqkM/uGlfjb9SjY1bIw4
iA5sBBZzHi3z0h1YV8QPuxEbi4nW91IJm2gsvvZhIrCHS3l6afab4pZBl2+XsDul
rKBxKKtD1rGxlG4LjncdabFn9gvLZad2bSysqz/qTAUStTvqJQIDAQABo2gwZjAO
BgNVHQ8BAf8EBAMCAqQwEwYDVR0lBAwwCgYIKwYBBQUHAwEwDwYDVR0TAQH/BAUw
AwEB/zAuBgNVHREEJzAlggtleGFtcGxlLmNvbYcEfwAAAYcQAAAAAAAAAAAAAAAA
AAAAATANBgkqhkiG9w0BAQsFAAOBgQCEcetwO59EWk7WiJsG4x8SY+UIAA+flUI9
tyC4lNhbcF2Idq9greZwbYCqTTTr2XiRNSMLCOjKyI7ukPoPjo16ocHj+P3vZGfs
h1fIw3cSS2OolhloGw/XM6RWPWtPAlGykKLciQrBru5NAPvCMsb/I1DAceTiotQM
fblo6RBxUQ==
-----END CERTIFICATE-----`
	localhostKey := `-----BEGIN RSA PRIVATE KEY-----
MIICXgIBAAKBgQDuLnQAI3mDgey3VBzWnB2L39JUU4txjeVE6myuDqkM/uGlfjb9
SjY1bIw4iA5sBBZzHi3z0h1YV8QPuxEbi4nW91IJm2gsvvZhIrCHS3l6afab4pZB
l2+XsDulrKBxKKtD1rGxlG4LjncdabFn9gvLZad2bSysqz/qTAUStTvqJQIDAQAB
AoGAGRzwwir7XvBOAy5tM/uV6e+Zf6anZzus1s1Y1ClbjbE6HXbnWWF/wbZGOpet
3Zm4vD6MXc7jpTLryzTQIvVdfQbRc6+MUVeLKwZatTXtdZrhu+Jk7hx0nTPy8Jcb
uJqFk541aEw+mMogY/xEcfbWd6IOkp+4xqjlFLBEDytgbIECQQDvH/E6nk+hgN4H
qzzVtxxr397vWrjrIgPbJpQvBsafG7b0dA4AFjwVbFLmQcj2PprIMmPcQrooz8vp
jy4SHEg1AkEA/v13/5M47K9vCxmb8QeD/asydfsgS5TeuNi8DoUBEmiSJwma7FXY
fFUtxuvL7XvjwjN5B30pNEbc6Iuyt7y4MQJBAIt21su4b3sjXNueLKH85Q+phy2U
fQtuUE9txblTu14q3N7gHRZB4ZMhFYyDy8CKrN2cPg/Fvyt0Xlp/DoCzjA0CQQDU
y2ptGsuSmgUtWj3NM9xuwYPm+Z/F84K6+ARYiZ6PYj013sovGKUFfYAqVXVlxtIX
qyUBnu3X9ps8ZfjLZO7BAkEAlT4R5Yl6cGhaJQYZHOde3JEMhNRcVFMO8dJDaFeo
f9Oeos0UUothgiDktdQHxdNEwLjQf7lJJBzV+5OtwswCWA==
-----END RSA PRIVATE KEY-----`
	cert, err := tls.X509KeyPair([]byte(localhostCert), []byte(localhostKey))
	require.NoError(t, err)
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	require.NoError(t, err)
	go handler(listener)
	return listener.Addr().String(), func() { listener.Close() }
}
