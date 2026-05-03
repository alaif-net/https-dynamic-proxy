package main

import (
	"crypto/tls"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(status int) {
	rw.status = status
	rw.ResponseWriter.WriteHeader(status)
}

func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		log.Printf("%s %s %d %s %s",
			r.Method, r.URL.Path, rw.status,
			time.Since(start).Round(time.Millisecond),
			r.RemoteAddr,
		)
	})
}

type config struct {
	proxyDomain      string
	registerToken    string
	backendTLSVerify bool
	dataDir          string
}

type state struct {
	mu   sync.RWMutex
	fqdn string
}

func (s *state) get() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fqdn
}

func (s *state) set(fqdn string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fqdn = fqdn
}

func loadFQDN(dataDir string) string {
	data, err := os.ReadFile(filepath.Join(dataDir, "backend_fqdn.txt"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func saveFQDN(dataDir, fqdn string) error {
	return os.WriteFile(filepath.Join(dataDir, "backend_fqdn.txt"), []byte(fqdn), 0644)
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	cfg := config{
		proxyDomain:      os.Getenv("PROXY_DOMAIN"),
		registerToken:    os.Getenv("REGISTER_TOKEN"),
		backendTLSVerify: os.Getenv("BACKEND_TLS_VERIFY") != "false",
		dataDir:          getenv("DATA_DIR", "/data"),
	}
	if cfg.proxyDomain == "" {
		log.Fatal("PROXY_DOMAIN is required")
	}
	if cfg.registerToken == "" {
		log.Fatal("REGISTER_TOKEN is required")
	}

	if err := os.MkdirAll(cfg.dataDir, 0755); err != nil {
		log.Fatalf("failed to create data dir: %v", err)
	}

	st := &state{fqdn: loadFQDN(cfg.dataDir)}
	if st.fqdn != "" {
		log.Printf("loaded backend FQDN: %s", st.fqdn)
	}

	var tlsConfig *tls.Config
	var httpHandler http.Handler

	devCert, devKey := os.Getenv("DEV_TLS_CERT"), os.Getenv("DEV_TLS_KEY")
	if devCert != "" && devKey != "" {
		cert, err := tls.LoadX509KeyPair(devCert, devKey)
		if err != nil {
			log.Fatalf("failed to load dev TLS cert: %v", err)
		}
		tlsConfig = &tls.Config{Certificates: []tls.Certificate{cert}} //nolint:gosec
		httpHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://"+cfg.proxyDomain+r.RequestURI, http.StatusMovedPermanently)
		})
		log.Printf("dev mode: using TLS cert from %s", devCert)
	} else {
		certManager := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(cfg.proxyDomain),
			Cache:      autocert.DirCache(filepath.Join(cfg.dataDir, "certs")),
		}
		tlsConfig = certManager.TLSConfig()
		httpHandler = certManager.HTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://"+cfg.proxyDomain+r.RequestURI, http.StatusMovedPermanently)
		}))
	}

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: !cfg.backendTLSVerify, //nolint:gosec
		},
	}

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			fqdn := st.get()
			req.URL.Scheme = "https"
			req.URL.Host = fqdn
			req.Host = fqdn
		},
		Transport: transport,
	}

	httpsMux := http.NewServeMux()

	httpsMux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+cfg.registerToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var body struct {
			FQDN string `json:"fqdn"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.FQDN == "" {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		st.set(body.FQDN)
		if err := saveFQDN(cfg.dataDir, body.FQDN); err != nil {
			log.Printf("failed to save FQDN: %v", err)
		}
		log.Printf("registered backend FQDN: %s", body.FQDN)
		w.WriteHeader(http.StatusOK)
	})

	httpsMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if st.get() == "" {
			http.Error(w, "no backend registered", http.StatusServiceUnavailable)
			return
		}
		proxy.ServeHTTP(w, r)
	})

	httpServer := &http.Server{
		Addr:    ":80",
		Handler: httpHandler,
	}

	httpsServer := &http.Server{
		Addr:      ":443",
		Handler:   accessLog(httpsMux),
		TLSConfig: tlsConfig,
	}

	go func() {
		log.Printf("HTTP server listening on :80")
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	log.Printf("HTTPS server listening on :443 (domain: %s)", cfg.proxyDomain)
	if err := httpsServer.ListenAndServeTLS("", ""); err != nil {
		log.Fatalf("HTTPS server error: %v", err)
	}
}
