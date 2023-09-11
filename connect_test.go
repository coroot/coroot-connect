package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"github.com/hashicorp/yamux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func init() {
	timeout = time.Second
	tlsSkipVerify = true
	version = "1.2.3"
}

func TestHandshakeTimeout(t *testing.T) {
	token := "b8ea8af6-ffee-44b3-aa9a-1fc02233cfb7"
	addr, stop := gateway(t, func(listener net.Listener) {
		// doesn't accept connections
	})
	defer stop()
	var err error
	_, err = connect(addr, "", token, []byte("config_data"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deadline exceeded")
}

func TestHandshakeError(t *testing.T) {
	token := "b8ea8af6-ffee-44b3-aa9a-1fc02233cfb7"
	addr, stop := gateway(t, func(listener net.Listener) {
		conn, err := listener.Accept()
		require.NoError(t, err)
		readHeaderAndConfig(t, conn, token, []byte("config_data"))
		writeResponse(t, conn, 500, "internal server error")
	})
	defer stop()
	_, err := connect(addr, "", token, []byte("config_data"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "internal server error")
}

func TestProxy(t *testing.T) {
	sessionChan := make(chan *yamux.Session)
	token := "b8ea8af6-ffee-44b3-aa9a-1fc02233cfb7"
	version = "1.2.3"

	addr, stop := gateway(t, func(listener net.Listener) {
		conn, err := listener.Accept()
		require.NoError(t, err)
		readHeaderAndConfig(t, conn, token, []byte("config_data"))
		writeResponse(t, conn, 200, "")

		cfg := yamux.DefaultConfig()
		cfg.KeepAliveInterval = time.Second

		session, err := yamux.Client(conn, cfg)
		require.NoError(t, err)

		sessionChan <- session
	})
	defer stop()

	prometheus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "Prometheus is Healthy.")
	}))
	defer prometheus.Close()

	pyroscope := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "Pyroscope is Healthy.")
	}))
	defer pyroscope.Close()

	gwConn, err := connect(addr, "", token, []byte("config_data"))
	go func() {
		require.NoError(t, proxy(context.Background(), gwConn))
	}()

	session := <-sessionChan

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			stream, err := session.Open()
			if err != nil {
				return nil, err
			}
			if err := binary.Write(stream, binary.LittleEndian, uint16(len(prometheus.Listener.Addr().String()))); err != nil {
				return nil, err
			}
			if _, err = stream.Write([]byte(prometheus.Listener.Addr().String())); err != nil {
				return nil, err
			}
			return stream, nil
		},
	}
	client := &http.Client{Transport: transport}

	res, err := client.Get("http://any/-/healthy")
	require.NoError(t, err)
	data, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	res.Body.Close()
	assert.Equal(t, "Prometheus is Healthy.", string(data))

	transport = &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			stream, err := session.Open()
			if err != nil {
				return nil, err
			}
			if err := binary.Write(stream, binary.LittleEndian, uint16(len(pyroscope.Listener.Addr().String()))); err != nil {
				return nil, err
			}
			if _, err = stream.Write([]byte(pyroscope.Listener.Addr().String())); err != nil {
				return nil, err
			}
			return stream, nil
		},
	}
	client = &http.Client{Transport: transport}

	res, err = client.Get("http://any/-/healthy")
	require.NoError(t, err)
	data, err = io.ReadAll(res.Body)
	require.NoError(t, err)
	res.Body.Close()
	assert.Equal(t, "Pyroscope is Healthy.", string(data))

}

func readHeaderAndConfig(t *testing.T, conn net.Conn, token string, config []byte) {
	h := RequestHeader{}
	require.NoError(t, binary.Read(conn, binary.LittleEndian, &h))
	require.Equal(t, token, string(h.Token[:]))
	require.Equal(t, version, string(bytes.Trim(h.Version[:], "\x00")))

	buf := make([]byte, int(h.ConfigSize))
	_, err := io.ReadFull(conn, buf)
	require.NoError(t, err)
	require.Equal(t, config, buf)
}

func writeResponse(t *testing.T, conn net.Conn, status uint16, message string) {
	err := binary.Write(conn, binary.LittleEndian, ResponseHeader{Status: status, MessageSize: uint16(len(message))})
	require.NoError(t, err)
	_, err = conn.Write([]byte(message))
	require.NoError(t, err)
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
