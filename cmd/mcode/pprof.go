package main

import (
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"runtime"
	"time"
)

func pprofAddr(raw string) (string, bool) {
	host, port, err := net.SplitHostPort(raw)
	if err != nil || port == "" || (host != "127.0.0.1" && host != "::1" && host != "localhost") {
		return "", false
	}
	return raw, true
}

func startPprofFromEnv() {
	raw := os.Getenv("MCODE_PPROF")
	if raw == "" {
		return
	}
	addr, ok := pprofAddr(raw)
	if !ok {
		log.Printf("invalid MCODE_PPROF address: %q", raw)
		return
	}

	runtime.SetMutexProfileFraction(5)
	runtime.SetBlockProfileRate(1000000)

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	go func() {
		server := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		log.Printf("starting pprof server on %s", addr)
		if err := server.ListenAndServe(); err != nil {
			log.Printf("pprof server: %v", err)
		}
	}()
}
