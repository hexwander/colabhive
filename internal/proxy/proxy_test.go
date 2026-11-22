package proxy

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestMaskAndParseProxyURL(t *testing.T) {
	masked := MaskProxyURL("socks5://alice:s3cr3t@38.12.90.4:1080")
	if masked != "socks5://38.12.*.*:1080" {
		t.Fatalf("unexpected masked URL: %q", masked)
	}
	if strings.Contains(masked, "s3cr3t") || strings.Contains(masked, "alice") {
		t.Fatal("MaskProxyURL leaked credentials")
	}
	if got := MaskProxyURL(""); got != "Direct" {
		t.Fatalf("expected Direct for empty URL, got %q", got)
	}
	if _, _, err := ParseProtocol("ftp://1.2.3.4:21"); err == nil {
		t.Fatal("expected error for unsupported scheme")
	}
}

func TestHTTPProxyAndConsistentUserAgent(t *testing.T) {
	var proxyHits atomic.Int32
	var seenUA atomic.Value

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenUA.Store(r.Header.Get("User-Agent"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"ip":     "203.0.113.42",
			"region": "US-SJC",
		})
	}))
	defer target.Close()

	// Forward HTTP proxy server
	httpProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		outReq, err := http.NewRequestWithContext(r.Context(), r.Method, r.RequestURI, r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		outReq.Header = r.Header.Clone()
		resp, err := http.DefaultClient.Do(outReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vals := range resp.Header {
			for _, v := range vals {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer httpProxy.Close()

	factory := NewTransportFactory()
	factory.SetProbeURL(target.URL)

	probe, err := factory.ProbeProxy(context.Background(), httpProxy.URL)
	if err != nil {
		t.Fatalf("ProbeProxy via HTTP proxy failed: %v", err)
	}
	if !probe.Healthy || probe.ExitIP != "203.0.113.42" || probe.Region != "US-SJC" || probe.LatencyMs < 1 {
		t.Fatalf("unexpected probe result: %+v", probe)
	}
	if proxyHits.Load() == 0 {
		t.Fatal("expected request to traverse HTTP proxy")
	}
	if ua, _ := seenUA.Load().(string); ua != DefaultUserAgent {
		t.Fatalf("expected consistent User-Agent %q, got %q", DefaultUserAgent, ua)
	}
}

func TestSOCKS5ProxyHTTPAndWebSocket(t *testing.T) {
	socksAddr, closeSOCKS := startMinimalSOCKS5Server(t, "hiveuser", "hivepass")
	defer closeSOCKS()

	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ws" {
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			mt, msg, err := conn.ReadMessage()
			if err == nil {
				_ = conn.WriteMessage(mt, append([]byte("echo:"), msg...))
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ip":"198.51.100.88","country":"JP"}`))
	}))
	defer target.Close()

	socksURL := fmt.Sprintf("socks5://hiveuser:hivepass@%s", socksAddr)
	factory := NewTransportFactory()

	probe, err := factory.ProbeProxyWithTarget(context.Background(), socksURL, target.URL)
	if err != nil {
		t.Fatalf("ProbeProxy over SOCKS5 failed: %v", err)
	}
	if !probe.Healthy || probe.ExitIP != "198.51.100.88" || probe.Region != "JP" {
		t.Fatalf("unexpected SOCKS5 probe result: %+v", probe)
	}

	// Verify WebSocket Dialer traverses SOCKS5 cleanly
	wsDialer, err := factory.WebSocketDialer(socksURL, 5*time.Second)
	if err != nil {
		t.Fatalf("WebSocketDialer failed: %v", err)
	}
	wsURL := "ws" + strings.TrimPrefix(target.URL, "http") + "/ws"
	conn, _, err := wsDialer.Dial(wsURL, factory.DefaultHeadersForWebSocket(nil))
	if err != nil {
		t.Fatalf("WebSocket Dial over SOCKS5 failed: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte("zmq_ping")); err != nil {
		t.Fatal(err)
	}
	_, reply, err := conn.ReadMessage()
	if err != nil || string(reply) != "echo:zmq_ping" {
		t.Fatalf("unexpected ws reply over SOCKS5: %q (%v)", string(reply), err)
	}
}

func TestHTTPSProxyConnectTunnelWebSocketAndProbe(t *testing.T) {
	var connectHits atomic.Int32
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ws" {
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			mt, msg, err := conn.ReadMessage()
			if err == nil {
				_ = conn.WriteMessage(mt, append([]byte("https-proxy-ws:"), msg...))
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ip":"203.0.113.99","country":"SG"}`))
	}))
	defer target.Close()

	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("pxuser:pxpass"))
	httpsProxy := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != wantAuth {
			w.Header().Set("Proxy-Authenticate", `Basic realm="ColabHiveTest"`)
			http.Error(w, "proxy auth required", http.StatusProxyAuthRequired)
			return
		}
		if r.Method != http.MethodConnect {
			http.Error(w, "expected CONNECT", http.StatusMethodNotAllowed)
			return
		}
		connectHits.Add(1)
		upstream, err := net.DialTimeout("tcp", r.Host, 5*time.Second)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			_ = upstream.Close()
			http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
			return
		}
		clientConn, clientBuf, err := hj.Hijack()
		if err != nil {
			_ = upstream.Close()
			return
		}
		_, _ = io.WriteString(clientConn, "HTTP/1.1 200 Connection Established\r\n\r\n")
		if clientBuf != nil && clientBuf.Reader.Buffered() > 0 {
			buffered := make([]byte, clientBuf.Reader.Buffered())
			_, _ = clientBuf.Read(buffered)
			_, _ = upstream.Write(buffered)
		}
		go func() {
			defer upstream.Close()
			defer clientConn.Close()
			_, _ = io.Copy(upstream, clientConn)
		}()
		go func() {
			defer upstream.Close()
			defer clientConn.Close()
			_, _ = io.Copy(clientConn, upstream)
		}()
	}))
	// Force HTTP/1.1 on test CONNECT proxy so http.Hijacker works cleanly over TLS.
	httpsProxy.EnableHTTP2 = false
	httpsProxy.StartTLS()
	defer httpsProxy.Close()

	parsedProxy, err := url.Parse(httpsProxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	parsedProxy.User = url.UserPassword("pxuser", "pxpass")
	rawHTTPSProxyURL := parsedProxy.String()

	factory := NewTransportFactory()
	// Trust both self-signed test certs (HTTPS proxy + WSS target) in this hermetic test.
	tlsCfg := httpsProxy.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	tlsCfg.InsecureSkipVerify = true
	factory.SetProxyTLSConfig(tlsCfg)

	probe, err := factory.ProbeProxyWithTarget(context.Background(), rawHTTPSProxyURL, target.URL)
	if err != nil {
		t.Fatalf("ProbeProxy over https:// proxy failed: %v", err)
	}
	if !probe.Healthy || probe.ExitIP != "203.0.113.99" || probe.Region != "SG" {
		t.Fatalf("unexpected HTTPS proxy probe result: %+v", probe)
	}

	wsDialer, err := factory.WebSocketDialer(rawHTTPSProxyURL, 5*time.Second)
	if err != nil {
		t.Fatalf("WebSocketDialer(https://...) failed: %v", err)
	}
	wssURL := "wss" + strings.TrimPrefix(target.URL, "https") + "/ws"
	conn, _, err := wsDialer.Dial(wssURL, factory.DefaultHeadersForWebSocket(nil))
	if err != nil {
		t.Fatalf("WSS Dial over https:// proxy failed: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte("hello_tls_proxy")); err != nil {
		t.Fatal(err)
	}
	_, reply, err := conn.ReadMessage()
	if err != nil || string(reply) != "https-proxy-ws:hello_tls_proxy" {
		t.Fatalf("unexpected WSS reply over https:// proxy: %q (%v)", string(reply), err)
	}
	if connectHits.Load() < 2 {
		t.Fatalf("expected >= 2 CONNECT tunnels through https:// proxy, got %d", connectHits.Load())
	}
}

// startMinimalSOCKS5Server starts an in-process RFC 1928 + RFC 1929 (user/pass) SOCKS5 server for hermetic tests.
func startMinimalSOCKS5Server(t *testing.T, wantUser, wantPass string) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen socks5: %v", err)
	}

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go handleSOCKS5Conn(c, wantUser, wantPass)
		}
	}()

	return ln.Addr().String(), func() { _ = ln.Close() }
}

func handleSOCKS5Conn(c net.Conn, wantUser, wantPass string) {
	defer c.Close()
	buf := make([]byte, 260)

	// Greeting: VER, NMETHODS, METHODS...
	if _, err := io.ReadFull(c, buf[:2]); err != nil || buf[0] != 0x05 {
		return
	}
	nMethods := int(buf[1])
	if _, err := io.ReadFull(c, buf[:nMethods]); err != nil {
		return
	}

	// Select Username/Password auth (0x02)
	if _, err := c.Write([]byte{0x05, 0x02}); err != nil {
		return
	}
	// RFC 1929 subnegotiation: VER=0x01, ULEN, UNAME, PLEN, PASSWD
	if _, err := io.ReadFull(c, buf[:2]); err != nil || buf[0] != 0x01 {
		return
	}
	uLen := int(buf[1])
	uBuf := make([]byte, uLen)
	if _, err := io.ReadFull(c, uBuf); err != nil {
		return
	}
	if _, err := io.ReadFull(c, buf[:1]); err != nil {
		return
	}
	pLen := int(buf[0])
	pBuf := make([]byte, pLen)
	if _, err := io.ReadFull(c, pBuf); err != nil {
		return
	}
	if string(uBuf) != wantUser || string(pBuf) != wantPass {
		_, _ = c.Write([]byte{0x01, 0x01})
		return
	}
	_, _ = c.Write([]byte{0x01, 0x00})

	// Request: VER=0x05, CMD=0x01 (CONNECT), RSV=0x00, ATYP
	if _, err := io.ReadFull(c, buf[:4]); err != nil || buf[1] != 0x01 {
		return
	}
	var host string
	switch buf[3] {
	case 0x01: // IPv4
		if _, err := io.ReadFull(c, buf[:4]); err != nil {
			return
		}
		host = net.IP(buf[:4]).String()
	case 0x03: // Domain
		if _, err := io.ReadFull(c, buf[:1]); err != nil {
			return
		}
		dLen := int(buf[0])
		dBuf := make([]byte, dLen)
		if _, err := io.ReadFull(c, dBuf); err != nil {
			return
		}
		host = string(dBuf)
	case 0x04: // IPv6
		ip6 := make([]byte, 16)
		if _, err := io.ReadFull(c, ip6); err != nil {
			return
		}
		host = net.IP(ip6).String()
	default:
		return
	}
	if _, err := io.ReadFull(c, buf[:2]); err != nil {
		return
	}
	port := binary.BigEndian.Uint16(buf[:2])

	upstream, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), 5*time.Second)
	if err != nil {
		_, _ = c.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer upstream.Close()

	// Success reply
	if _, err := c.Write([]byte{0x05, 0x00, 0x00, 0x01, 127, 0, 0, 1, 0, 0}); err != nil {
		return
	}

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, c); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, upstream); done <- struct{}{} }()
	<-done
}
