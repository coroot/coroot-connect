package main

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"github.com/hashicorp/yamux"
	"github.com/jpillora/backoff"
	"io"
	"io/ioutil"
	"k8s.io/klog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

var (
	version                  = "unknown"
	timeout                  = 10 * time.Second
	tlsSkipVerify            = false
	endpointsRefreshInterval = 10 * time.Minute
	backoffFactor            = 2.
	backoffMin               = 5 * time.Second
	backoffMax               = time.Minute
	streamTimeout            = time.Minute
)

type Tunnel struct {
	promAddr     string
	address      string
	projectToken string
	serverName   string
	cancelFn     context.CancelFunc
	gwConn       net.Conn
}

func NewTunnel(address, serverName, promAddr, projectToken string) *Tunnel {
	t := &Tunnel{
		address:      address,
		promAddr:     promAddr,
		projectToken: projectToken,
		serverName:   serverName,
	}
	var ctx context.Context
	ctx, t.cancelFn = context.WithCancel(context.Background())
	go t.keepConnected(ctx)
	return t
}

func (t *Tunnel) keepConnected(ctx context.Context) {
	b := backoff.Backoff{Factor: backoffFactor, Min: backoffMin, Max: backoffMax}
	var err error
	for {
		select {
		case <-ctx.Done():
			return
		default:
			t.gwConn, err = connect(t.address, t.serverName, t.projectToken)
			if err != nil {
				d := b.Duration()
				klog.Errorf("%s, reconnecting to %s in %.0fs", err, t.address, d.Seconds())
				time.Sleep(d)
				continue
			}
			b.Reset()
			proxy(ctx, t.gwConn, t.promAddr)
			_ = t.gwConn.Close()
		}
	}
}

func (t *Tunnel) Close() {
	t.cancelFn()
	if t.gwConn != nil {
		_ = t.gwConn.Close()
	}
}

func main() {
	klog.Infof("version: %s", version)
	resolverUrl := os.Getenv("RESOLVER_URL")
	if resolverUrl == "" {
		resolverUrl = "https://gw.coroot.com/promtun/resolve"
	}
	promAddr := mustEnv("PROMETHEUS_ADDRESS")
	projectToken := mustEnv("PROJECT_TOKEN")

	u, err := url.Parse(resolverUrl)
	if err != nil {
		klog.Exitf("invalid RESOLVER_URL %s: %s", resolverUrl, err)
	}
	serverName := u.Hostname()

	if err := pingProm(promAddr); err != nil {
		klog.Exitf("failed to ping prometheus: %s", err)
	}

	tunnels := map[string]*Tunnel{}

	b := backoff.Backoff{Factor: backoffFactor, Min: backoffMin, Max: backoffMax}
	for {
		klog.Infof("updating gateways endpoints from %s", resolverUrl)
		endpoints, err := getEndpoints(resolverUrl, projectToken)
		if err != nil {
			d := b.Duration()
			klog.Errorf("failed to get gateway endpoints: %s, retry in %.0fs", err, d.Seconds())
			time.Sleep(d)
			continue
		}
		b.Reset()
		klog.Infof("desired endpoints: %s", endpoints)
		fresh := map[string]bool{}
		for _, e := range endpoints {
			fresh[e] = true
			if _, ok := tunnels[e]; !ok {
				klog.Infof("starting tunnel to %s", e)
				tunnels[e] = NewTunnel(e, serverName, promAddr, projectToken)
			}
		}
		for e, t := range tunnels {
			if !fresh[e] {
				klog.Infof("closing tunnel to %s", e)
				t.Close()
				delete(tunnels, e)
			}
		}
		time.Sleep(endpointsRefreshInterval)
	}
}

func getEndpoints(resolverUrl, projectToken string) ([]string, error) {
	req, _ := http.NewRequest("GET", resolverUrl, nil)
	req.Header.Set("X-Token", projectToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: %s", resp.Status, string(payload))
	}
	return strings.Split(strings.TrimSpace(string(payload)), ";"), nil
}

func connect(gwAddr, serverName, projectToken string) (net.Conn, error) {
	klog.Infof("connecting to %s (%s)", gwAddr, serverName)
	deadline := time.Now().Add(timeout)
	dialer := &net.Dialer{Deadline: deadline}
	tlsCfg := &tls.Config{InsecureSkipVerify: tlsSkipVerify, ServerName: serverName}
	gwConn, err := tls.DialWithDialer(dialer, "tcp", gwAddr, tlsCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to establish connection to %s: %s", gwAddr, err)
	}
	klog.Infof("connected to gateway %s", gwAddr)

	_ = gwConn.SetDeadline(deadline)
	if _, err := gwConn.Write([]byte(projectToken)); err != nil {
		_ = gwConn.Close()
		return nil, fmt.Errorf("failed to send project token to %s: %s", gwAddr, err)
	}
	var resp uint16
	if err := binary.Read(gwConn, binary.LittleEndian, &resp); err != nil {
		_ = gwConn.Close()
		return nil, fmt.Errorf("failed to read gateway response from %s: %s", gwAddr, err)
	}
	_ = gwConn.SetDeadline(time.Time{})
	klog.Infof("got from gateway %s: %d", gwAddr, resp)

	if resp != 200 {
		_ = gwConn.Close()
		return nil, fmt.Errorf("failed to authenticate project on %s: %d", gwAddr, resp)
	}
	klog.Infof("ready to proxy requests from %s", gwAddr)
	return gwConn, nil
}

func proxy(ctx context.Context, gwConn net.Conn, promAddr string) {
	cfg := yamux.DefaultConfig()
	cfg.KeepAliveInterval = time.Second
	cfg.LogOutput = ioutil.Discard
	session, err := yamux.Server(gwConn, cfg)
	if err != nil {
		klog.Errorln("failed to start a TCP multiplexing server:", err)
		return
	}
	defer session.Close()
	for {
		select {
		case <-ctx.Done():
			return
		default:
			gwStream, err := session.Accept()
			if err != nil {
				klog.Errorf("failed to accept stream: %s", err)
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				deadline := time.Now().Add(streamTimeout)
				if err := c.SetDeadline(deadline); err != nil {
					klog.Errorf("failed to set deadline to stream: %s", err)
					return
				}
				promConn, err := net.DialTimeout("tcp", promAddr, timeout)
				if err != nil {
					klog.Errorf("failed to establish prometheus connection: %s", err)
					return
				}
				defer promConn.Close()
				if err = promConn.SetDeadline(deadline); err != nil {
					klog.Errorf("failed to set deadline to prometheus connection: %s", err)
					return
				}
				go func() {
					io.Copy(c, promConn)
				}()
				io.Copy(promConn, c)
			}(gwStream)
		}
	}
}

func mustEnv(key string) string {
	value := os.Getenv(key)
	if value == "" {
		klog.Exitln(key, "environment variable is required")
	}
	return value
}

func pingProm(addr string) error {
	c, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return err
	}
	_ = c.Close()
	return nil
}
