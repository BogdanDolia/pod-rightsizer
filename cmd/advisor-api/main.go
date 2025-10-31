package main

import (
	"log"
	"net/http"
	"os"

	"github.com/BogdanDolia/pod-rightsizer/pkg/server"
)

func main() {
	addr := ":8080"
	if v := os.Getenv("PORT"); v != "" {
		addr = ":" + v
	}

	srv := server.New()

	log.Printf("advisor-api listening on %s", addr)
	if err := http.ListenAndServe(addr, srv); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
