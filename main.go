package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

var (
	validName     = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	reservedNames = map[string]bool{"backends": true}
)

// backend represents a single upstream server.
type backend struct {
	FQDN string `json:"fqdn"`
	Port string `json:"port,omitempty"`
}

func (b backend) urlHost() string {
	if b.Port != "" {
		return b.FQDN + ":" + b.Port
	}
	return b.FQDN
}

func (b backend) empty() bool { return b.FQDN == "" }

// routeKey is used to pass routing results through request context.
type routeKey struct{}

type routeResult struct {
	be         backend
	targetPath string
}

// state holds all registered backends.
type state struct {
	mu        sync.RWMutex
	defaultBE backend
	named     map[string]backend
}

// route resolves a request path to a backend and target path.
// Named backends take priority over the default backend.
func (s *state) route(path string) (routeResult, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	seg, rest := firstSegment(path)
	if seg != "" {
		if be, ok := s.named[seg]; ok {
			return routeResult{be, rest}, true
		}
	}
	if !s.defaultBE.empty() {
		return routeResult{s.defaultBE, path}, true
	}
	return routeResult{}, false
}

func (s *state) setDefault(be backend) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.defaultBE = be
}

func (s *state) setNamed(name string, be backend) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.named[name] = be
}

func (s *state) deleteDefault() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.defaultBE.empty() {
		return false
	}
	s.defaultBE = backend{}
	return true
}

func (s *state) deleteNamed(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.named[name]; !ok {
		return false
	}
	delete(s.named, name)
	return true
}

func (s *state) list() (def backend, named map[string]backend) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := make(map[string]backend, len(s.named))
	for k, v := range s.named {
		cp[k] = v
	}
	return s.defaultBE, cp
}

// firstSegment splits /seg/rest into ("seg", "/rest").
func firstSegment(path string) (seg, rest string) {
	trimmed := strings.TrimPrefix(path, "/")
	if i := strings.Index(trimmed, "/"); i >= 0 {
		return trimmed[:i], "/" + trimmed[i+1:]
	}
	if trimmed == "" {
		return "", "/"
	}
	return trimmed, "/"
}

// Persistence

type persistedData struct {
	Default backend            `json:"default,omitempty"`
	Named   map[string]backend `json:"named,omitempty"`
}

func loadState(dataDir string) *state {
	st := &state{named: make(map[string]backend)}

	data, err := os.ReadFile(filepath.Join(dataDir, "backends.json"))
	if err == nil {
		var pd persistedData
		if json.Unmarshal(data, &pd) == nil {
			st.defaultBE = pd.Default
			if pd.Named != nil {
				st.named = pd.Named
			}
			return st
		}
	}

	// Migrate from legacy backend_fqdn.txt
	old, err := os.ReadFile(filepath.Join(dataDir, "backend_fqdn.txt"))
	if err == nil {
		saved := strings.TrimSpace(string(old))
		if i := strings.LastIndex(saved, ":"); i > 0 {
			st.defaultBE = backend{FQDN: saved[:i], Port: saved[i+1:]}
		} else if saved != "" {
			st.defaultBE = backend{FQDN: saved}
		}
	}
	return st
}

func saveState(dataDir string, st *state) error {
	def, named := st.list()
	pd := persistedData{Default: def, Named: named}
	data, err := json.MarshalIndent(pd, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dataDir, "backends.json"), data, 0644)
}

// Middleware

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

// Config

type config struct {
	proxyDomain      string
	registerToken    string
	backendTLSVerify bool
	dataDir          string
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

	st := loadState(cfg.dataDir)
	{
		def, named := st.list()
		if !def.empty() {
			log.Printf("loaded default backend: %s", def.urlHost())
		}
		for name, be := range named {
			log.Printf("loaded named backend %q: %s", name, be.urlHost())
		}
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
			result, ok := req.Context().Value(routeKey{}).(routeResult)
			if !ok {
				return
			}
			req.URL.Scheme = "https"
			req.URL.Host = result.be.urlHost()
			req.URL.Path = result.targetPath
			req.URL.RawPath = ""
			req.Host = result.be.FQDN
		},
		Transport: transport,
	}

	auth := func(r *http.Request) bool {
		return r.Header.Get("Authorization") == "Bearer "+cfg.registerToken
	}

	save := func() {
		if err := saveState(cfg.dataDir, st); err != nil {
			log.Printf("failed to save state: %v", err)
		}
	}

	mux := http.NewServeMux()

	// GET /backends — list all registered backends
	mux.HandleFunc("GET /backends", func(w http.ResponseWriter, r *http.Request) {
		if !auth(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		def, named := st.list()
		type response struct {
			Default *backend            `json:"default,omitempty"`
			Named   map[string]backend  `json:"named"`
		}
		resp := response{Named: named}
		if !def.empty() {
			resp.Default = &def
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	// POST /backends — register a backend (name optional → default)
	mux.HandleFunc("POST /backends", func(w http.ResponseWriter, r *http.Request) {
		if !auth(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var body struct {
			Name string `json:"name"`
			FQDN string `json:"fqdn"`
			Port string `json:"port"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.FQDN == "" {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		be := backend{FQDN: body.FQDN, Port: body.Port}
		if body.Name == "" {
			st.setDefault(be)
			log.Printf("registered default backend: %s", be.urlHost())
		} else {
			if !validName.MatchString(body.Name) {
				http.Error(w, "invalid name: use [a-zA-Z0-9_-]+", http.StatusBadRequest)
				return
			}
			if reservedNames[body.Name] {
				http.Error(w, "name is reserved", http.StatusBadRequest)
				return
			}
			st.setNamed(body.Name, be)
			log.Printf("registered backend %q: %s", body.Name, be.urlHost())
		}
		save()
		w.WriteHeader(http.StatusOK)
	})

	// DELETE /backends — delete default backend
	mux.HandleFunc("DELETE /backends", func(w http.ResponseWriter, r *http.Request) {
		if !auth(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !st.deleteDefault() {
			http.Error(w, "no default backend registered", http.StatusNotFound)
			return
		}
		save()
		log.Printf("deleted default backend")
		w.WriteHeader(http.StatusOK)
	})

	// DELETE /backends/{name} — delete named backend
	mux.HandleFunc("DELETE /backends/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !auth(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		name := r.PathValue("name")
		if !st.deleteNamed(name) {
			http.Error(w, "backend not found", http.StatusNotFound)
			return
		}
		save()
		log.Printf("deleted backend %q", name)
		w.WriteHeader(http.StatusOK)
	})

	// All other requests → route to backend
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		result, ok := st.route(r.URL.Path)
		if !ok {
			http.Error(w, "no backend registered", http.StatusServiceUnavailable)
			return
		}
		ctx := context.WithValue(r.Context(), routeKey{}, result)
		proxy.ServeHTTP(w, r.WithContext(ctx))
	})

	httpServer := &http.Server{
		Addr:    ":80",
		Handler: httpHandler,
	}
	httpsServer := &http.Server{
		Addr:      ":443",
		Handler:   accessLog(mux),
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
