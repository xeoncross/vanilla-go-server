package main

import (
	"context"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"
)

type key int

const (
	requestIDKey key = 0
)

var (
	listenAddr string
	healthy    int32
)

func main() {
	flag.StringVar(&listenAddr, "listen-addr", ":5000", "server listen address")
	flag.Parse()

	logger := log.New(os.Stdout, "http: ", log.LstdFlags)

	nextRequestID := func() string {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}

	server := &http.Server{
		Addr:         listenAddr,
		Handler:      tracing(nextRequestID)(logging(logger)(routes())),
		ErrorLog:     logger,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  15 * time.Second,
	}

	// Listen for CTRL+C or kill and start shutting down the app without
	// disconnecting people by not taking any new requests. ("Graceful Shutdown")
	done := make(chan bool)
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-quit
		logger.Println("Server is shutting down...")
		atomic.StoreInt32(&healthy, 0)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		server.SetKeepAlivesEnabled(false)
		if err := server.Shutdown(ctx); err != nil {
			logger.Fatalf("Could not gracefully shutdown the server: %v\n", err)
		}
		close(done)
	}()

	logger.Println("Server is ready to handle requests at", listenAddr)
	atomic.StoreInt32(&healthy, 1)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Fatalf("Could not listen on %s: %v\n", listenAddr, err)
	}

	<-done
	logger.Println("Server stopped")
}

// Setup all your routes
func routes() *http.ServeMux {
	router := http.NewServeMux()
	router.HandleFunc("/", indexHandler)
	router.HandleFunc("/health", healthHandler)
	router.HandleFunc("/hello", helloHandler)
	router.HandleFunc("/json-as-text", forceTextHandler)
	return router
}

// Shows how to use templates with template functions and data
func indexHandler(w http.ResponseWriter, r *http.Request) {

	if r.URL.Path != "/" {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}

	// Example inline
	var indexHTML = `<h1>{{ .Name | SayHi }}</h1>
  <p>{{ CurrentTime }}</p>
  <p>Your IP: {{ .IP }}</p>
  <ul>{{ range $key, $value := .Links }}
    <li><a href="{{ $value }}">{{ $key }}</a></li>
  {{ end }}</ul>`

	// Anonymous struct to hold template data
	data := struct {
		Name  string
		Links map[string]string
		IP    string
	}{
		Name: "John",
		IP:   r.RemoteAddr,
		Links: map[string]string{
			"Home":         "/",
			"Hello":        "/hello",
			"Health Ping":  "/health",
			"JSON as TEXT": "/json-as-text",
		},
	}

	tmpl, err := template.New("index").Funcs(template.FuncMap{
		"CurrentTime": func() string { return time.Now().Format(time.RFC3339) },
		"SayHi":       func(name string) string { return fmt.Sprintf("Hi %s!", name) },
	}).Parse(indexHTML) // IRL it would be .ParseFiles("templates/index.tpl")

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := tmpl.Execute(w, data); err != nil {
		fmt.Println(err)
	}
}

// Simplest handler we could write
func helloHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(w, "Hello, World!")
}

// Prevent Content-Type sniffing
func forceTextHandler(w http.ResponseWriter, r *http.Request) {
	// https://stackoverflow.com/questions/18337630/what-is-x-content-type-options-nosniff
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, "{\"status\":\"ok\"}")
}

// Report server status
func healthHandler(w http.ResponseWriter, r *http.Request) {
	if atomic.LoadInt32(&healthy) == 1 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusServiceUnavailable)
}

func logging(logger *log.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				requestID, ok := r.Context().Value(requestIDKey).(string)
				if !ok {
					requestID = "unknown"
				}
				logger.Println(requestID, r.Method, r.URL.Path, r.RemoteAddr, r.UserAgent())
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func tracing(nextRequestID func() string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestID := r.Header.Get("X-Request-Id")
			if requestID == "" {
				requestID = nextRequestID()
			}
			ctx := context.WithValue(r.Context(), requestIDKey, requestID)
			w.Header().Set("X-Request-Id", requestID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
