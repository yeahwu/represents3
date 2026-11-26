package main

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	applog "represents3/internal/logger"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

const letsEncryptStagingDirectory = "https://acme-staging-v02.api.letsencrypt.org/directory"

type config struct {
	ProxyDomains  string
	ProxyUser     string
	ProxyPass     string
	AcmeEmail     string
	AcmeStaging   bool
	CertCacheDir  string
	ListenHTTP    string
	ListenHTTPS   string
	UpstreamDialS time.Duration
	LogLevel      string
	LogFormat     string
	LogSource     bool
}

type proxyServer struct {
	transport      *http.Transport
	user           string
	pass           string
	connectTimeout time.Duration
}

var hopHeaders = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

func main() {
	cfg := loadConfig()
	if err := cfg.validate(); err != nil {
		log.Fatalf("config error: %v", err)
	}
	logger, err := applog.New(applog.Config{
		Level:     cfg.LogLevel,
		Format:    cfg.LogFormat,
		AddSource: cfg.LogSource,
	})
	if err != nil {
		log.Fatalf("logger config error: %v", err)
	}
	slog.SetDefault(logger)

	domains := splitAndTrim(cfg.ProxyDomains)
	manager := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Cache:      autocert.DirCache(cfg.CertCacheDir),
		HostPolicy: autocert.HostWhitelist(domains...),
		Email:      cfg.AcmeEmail,
	}
	if cfg.AcmeStaging {
		manager.Client = &acme.Client{DirectoryURL: letsEncryptStagingDirectory}
		slog.Info("ACME staging enabled")
	}

	ps := &proxyServer{
		user:           cfg.ProxyUser,
		pass:           cfg.ProxyPass,
		connectTimeout: cfg.UpstreamDialS,
		transport: &http.Transport{
			Proxy: nil,
			DialContext: (&net.Dialer{
				Timeout:   cfg.UpstreamDialS,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          200,
			MaxIdleConnsPerHost:   50,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 45 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}

	httpSrv := &http.Server{
		Addr: cfg.ListenHTTP,
		Handler: manager.HTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "HTTPS forward proxy endpoint", http.StatusBadRequest)
		})),
		ReadHeaderTimeout: 10 * time.Second,
	}

	httpsSrv := &http.Server{
		Addr:              cfg.ListenHTTPS,
		Handler:           http.HandlerFunc(ps.handle),
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       120 * time.Second,
		WriteTimeout:      0,
		TLSConfig: &tls.Config{
			MinVersion:     tls.VersionTLS12,
			GetCertificate: manager.GetCertificate,
		},
	}

	errCh := make(chan error, 2)

	go func() {
		slog.Info("ACME HTTP challenge listening", "addr", cfg.ListenHTTP)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	go func() {
		slog.Info("HTTPS forward proxy listening", "addr", cfg.ListenHTTPS, "domains", domains)
		if err := httpsSrv.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		fatal("server error", "error", err)
	case sig := <-sigCh:
		slog.Info("received signal, shutting down", "signal", sig.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpsSrv.Shutdown(ctx); err != nil {
		slog.Warn("HTTPS server shutdown error", "error", err)
	}
	if err := httpSrv.Shutdown(ctx); err != nil {
		slog.Warn("HTTP server shutdown error", "error", err)
	}
	slog.Info("proxy stopped")
}

func (p *proxyServer) handle(w http.ResponseWriter, r *http.Request) {
	if !p.authorized(r) {
		slog.Warn("proxy auth required", "client", r.RemoteAddr, "method", r.Method, "host", r.Host)
		w.Header().Set("Proxy-Authenticate", `Basic realm="proxy"`)
		http.Error(w, "proxy auth required", http.StatusProxyAuthRequired)
		return
	}

	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
		return
	}
	p.handleHTTP(w, r)
}

func (p *proxyServer) authorized(r *http.Request) bool {
	if p.user == "" && p.pass == "" {
		return true
	}

	raw := r.Header.Get("Proxy-Authorization")
	if raw == "" {
		raw = r.Header.Get("Authorization")
	}
	if !strings.HasPrefix(strings.ToLower(raw), "basic ") {
		return false
	}

	enc := strings.TrimSpace(raw[6:])
	dec, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return false
	}
	parts := strings.SplitN(string(dec), ":", 2)
	if len(parts) != 2 {
		return false
	}

	userOK := subtle.ConstantTimeCompare([]byte(parts[0]), []byte(p.user)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(parts[1]), []byte(p.pass)) == 1
	return userOK && passOK
}

func (p *proxyServer) handleConnect(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	target := r.Host
	if !strings.Contains(target, ":") {
		target += ":443"
	}

	dst, err := net.DialTimeout("tcp", target, p.connectTimeout)
	if err != nil {
		slog.Warn("CONNECT dial failed", "client", r.RemoteAddr, "target", target, "error", err)
		http.Error(w, "dial target failed: "+err.Error(), http.StatusServiceUnavailable)
		return
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = dst.Close()
		slog.Error("CONNECT hijack not supported", "client", r.RemoteAddr, "target", target)
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}

	clientConn, rw, err := hj.Hijack()
	if err != nil {
		_ = dst.Close()
		slog.Warn("CONNECT hijack failed", "client", r.RemoteAddr, "target", target, "error", err)
		return
	}
	defer clientConn.Close()
	defer dst.Close()

	_, _ = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
	_ = rw.Flush()
	slog.Info("CONNECT tunnel established", "client", r.RemoteAddr, "target", target)

	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(dst, rw)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(clientConn, dst)
		done <- struct{}{}
	}()
	<-done
	slog.Info("CONNECT tunnel closed", "client", r.RemoteAddr, "target", target, "duration", time.Since(start).String())
}

func (p *proxyServer) handleHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	outReq := r.Clone(r.Context())
	outReq.RequestURI = ""
	removeHopHeaders(outReq.Header)
	outReq.Header.Del("Proxy-Authorization")

	if outReq.URL == nil || !outReq.URL.IsAbs() {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		path := r.URL.Path
		if path == "" {
			path = "/"
		}
		outReq.URL = &url.URL{
			Scheme:   scheme,
			Host:     r.Host,
			Path:     path,
			RawQuery: r.URL.RawQuery,
		}
	}
	targetHost := ""
	if outReq.URL != nil {
		targetHost = outReq.URL.Host
	}

	resp, err := p.transport.RoundTrip(outReq)
	if err != nil {
		targetURL := ""
		if outReq.URL != nil {
			targetURL = outReq.URL.String()
		}
		slog.Error("HTTP upstream error", "client", r.RemoteAddr, "method", r.Method, "target", targetURL, "duration", time.Since(start).String(), "error", err)
		http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	removeHopHeaders(resp.Header)
	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	n, copyErr := io.Copy(w, resp.Body)
	if copyErr != nil {
		slog.Warn("HTTP response copy error", "client", r.RemoteAddr, "method", r.Method, "host", targetHost, "status", resp.StatusCode, "bytes", n, "duration", time.Since(start).String(), "error", copyErr)
		return
	}
	slog.Info("HTTP request proxied", "client", r.RemoteAddr, "method", r.Method, "host", targetHost, "status", resp.StatusCode, "bytes", n, "duration", time.Since(start).String())
}

func removeHopHeaders(h http.Header) {
	conn := h.Values("Connection")

	for _, k := range hopHeaders {
		h.Del(k)
	}

	for _, c := range conn {
		for _, f := range strings.Split(c, ",") {
			h.Del(strings.TrimSpace(f))
		}
	}
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func loadConfig() config {
	return config{
		ProxyDomains:  os.Getenv("PROXY_DOMAINS"),
		ProxyUser:     os.Getenv("PROXY_USER"),
		ProxyPass:     os.Getenv("PROXY_PASS"),
		AcmeEmail:     os.Getenv("ACME_EMAIL"),
		AcmeStaging:   boolEnv("ACME_STAGING", false),
		CertCacheDir:  strEnv("CERT_CACHE_DIR", "./cert-cache"),
		ListenHTTP:    strEnv("LISTEN_HTTP", ":80"),
		ListenHTTPS:   strEnv("LISTEN_HTTPS", ":443"),
		UpstreamDialS: durationEnv("UPSTREAM_DIAL_TIMEOUT", 10*time.Second),
		LogLevel:      strEnv("LOG_LEVEL", "info"),
		LogFormat:     strEnv("LOG_FORMAT", "text"),
		LogSource:     boolEnv("LOG_SOURCE", false),
	}
}

func (c config) validate() error {
	if len(splitAndTrim(c.ProxyDomains)) == 0 {
		return errors.New("PROXY_DOMAINS is required (e.g. proxy.example.com)")
	}
	if (c.ProxyUser == "" && c.ProxyPass != "") || (c.ProxyUser != "" && c.ProxyPass == "") {
		return errors.New("PROXY_USER and PROXY_PASS must be both set or both empty")
	}
	return nil
}

func splitAndTrim(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func strEnv(k, def string) string {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def
	}
	return v
}

func boolEnv(k string, def bool) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(k)))
	if v == "" {
		return def
	}
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func durationEnv(k string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

func fatal(msg string, args ...any) {
	slog.Error(msg, args...)
	os.Exit(1)
}
