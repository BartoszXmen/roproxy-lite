package main

import (
	"errors"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/valyala/fasthttp"
)

var (
	timeout, _ = strconv.Atoi(os.Getenv("TIMEOUT"))
	retries, _ = strconv.Atoi(os.Getenv("RETRIES"))
	port        = os.Getenv("PORT")
)

var client *fasthttp.Client

func main() {
	// Inicjalizacja klienta HTTP z timeoutem
	client = &fasthttp.Client{
		ReadTimeout:       time.Duration(timeout) * time.Second,
		MaxIdleConnDuration: 60 * time.Second,
	}

	// Start serwera
	h := requestHandler
	if err := fasthttp.ListenAndServe(":"+port, h); err != nil {
		log.Fatalf("Error in ListenAndServe: %s", err)
	}
}

func requestHandler(ctx *fasthttp.RequestCtx) {
	// Weryfikacja klucza PROXYKEY (env var KEY)
	if key, ok := os.LookupEnv("KEY"); ok && string(ctx.Request.Header.Peek("PROXYKEY")) != key {
		ctx.SetStatusCode(fasthttp.StatusProxyAuthRequired) // 407
		ctx.SetBody([]byte("Missing or invalid PROXYKEY header."))
		return
	}

	// Parsowanie ścieżki: obsługa /v1/<service>/... oraz /<service>/...
	service, path, err := parsePath(ctx)
	if err != nil {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		ctx.SetBody([]byte(err.Error()))
		return
	}

	// Wysłanie zapytania do odpowiedniego subdomeny Roblox
	response := makeRequest(ctx, service, path, 1)
	defer fasthttp.ReleaseResponse(response)

	// Kopiowanie statusu, nagłówków i ciała odpowiedzi
	ctx.SetStatusCode(response.StatusCode())
	ctx.SetBody(response.Body())
	response.Header.VisitAll(func(key, value []byte) {
		ctx.Response.Header.Set(string(key), string(value))
	})
}

// parsePath rozbija URI na service i ścieżkę, obsługując prefix /v1
func parsePath(ctx *fasthttp.RequestCtx) (service string, rest string, err error) {
	// Ścieżka bez query, np. "/v1/users/123" lub "/games/456"
	path := string(ctx.URI().Path())
	trimmed := strings.TrimPrefix(path, "/")
	parts := strings.SplitN(trimmed, "/", 3)

	if len(parts) >= 3 && parts[0] == "v1" {
		service = parts[1]
		rest = parts[2]
	} else if len(parts) >= 2 {
		service = parts[0]
		rest = parts[1]
	} else {
		return "", "", errors.New("URL format invalid.")
	}

	// Dołączenie query string, jeśli istnieje
	if qs := string(ctx.URI().QueryString()); qs != "" {
		rest = rest + "?" + qs
	}

	return service, rest, nil
}

// makeRequest próbuje wysłać req do https://<service>.roblox.com/<path>, z retry
func makeRequest(ctx *fasthttp.RequestCtx, service, path string, attempt int) *fasthttp.Response {
	if attempt > retries {
		resp := fasthttp.AcquireResponse()
		resp.SetBody([]byte("Proxy failed to connect. Please try again."))
		resp.SetStatusCode(fasthttp.StatusInternalServerError)
		return resp
	}

	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)

	// Budujemy URL docelowy
	req.SetRequestURI("https://" + service + ".roblox.com/" + path)
	req.Header.SetMethod(string(ctx.Method()))

	// Kopiowanie nagłówków od klienta
	ctx.Request.Header.VisitAll(func(key, value []byte) {
		req.Header.Set(string(key), string(value))
	})

	// Nadpisanie User-Agent i usunięcie niepotrzebnych nagłówków
	req.Header.Set("User-Agent", "RoProxy")
	req.Header.Del("Roblox-Id")

	resp := fasthttp.AcquireResponse()
	if err := client.Do(req, resp); err != nil {
		fasthttp.ReleaseResponse(resp)
		// retry przy błędzie sieci
		return makeRequest(ctx, service, path, attempt+1)
	}
	return resp
}
