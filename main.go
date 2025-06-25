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
    port       = os.Getenv("PORT")
)

var client *fasthttp.Client

func main() {
    // Inicjalizacja klienta HTTP z timeoutem
    client = &fasthttp.Client{
        ReadTimeout:         time.Duration(timeout) * time.Second,
        MaxIdleConnDuration: 60 * time.Second,
    }

    // Start serwera
    if err := fasthttp.ListenAndServe(":"+port, requestHandler); err != nil {
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

    // Obsługa bezpośredniego proxy dla /v1/users/... (Roblox Users API wymaga pełnej ścieżki)
    rawPath := string(ctx.URI().Path())
    if strings.HasPrefix(rawPath, "/v1/users/") {
        directProxy(ctx, "https://users.roblox.com"+rawPath)
        return
    }

    // Parsowanie ścieżki i rozpoznanie serwisu
    service, rest, err := parsePath(ctx)
    if err != nil {
        ctx.SetStatusCode(fasthttp.StatusBadRequest)
        ctx.SetBody([]byte(err.Error()))
        return
    }

    // Wysłanie zapytania do odpowiedniej subdomeny Roblox bez rekursji
    resp := iterativeRequest(ctx, service, rest)
    defer fasthttp.ReleaseResponse(resp)

    // Kopiowanie statusu, nagłówków i ciała odpowiedzi
    ctx.SetStatusCode(resp.StatusCode())
    ctx.SetBody(resp.Body())
    resp.Header.VisitAll(func(key, value []byte) {
        ctx.Response.Header.Set(string(key), string(value))
    })
}

// directProxy przekierowuje request bezpośrednio do pełnego URL
func directProxy(ctx *fasthttp.RequestCtx, url string) {
    req := fasthttp.AcquireRequest()
    defer fasthttp.ReleaseRequest(req)
    resp := fasthttp.AcquireResponse()
    defer fasthttp.ReleaseResponse(resp)

    // Składanie URL z query string
    fullURL := url
    if qs := string(ctx.URI().QueryString()); qs != "" {
        fullURL += "?" + qs
    }
    req.SetRequestURI(fullURL)
    req.Header.SetMethod(string(ctx.Method()))

    // Kopiowanie nagłówków od klienta
    ctx.Request.Header.VisitAll(func(key, value []byte) {
        req.Header.Set(string(key), string(value))
    })
    // Dodanie/zmiana nagłówków
    req.Header.Set("Accept", "application/json")
    if key := os.Getenv("KEY"); key != "" {
        req.Header.Set("PROXYKEY", key)
    }
    req.Header.Set("User-Agent", "RoProxy")
    req.Header.Del("Roblox-Id")

    // Wykonanie request
    if err := client.Do(req, resp); err != nil {
        ctx.SetStatusCode(fasthttp.StatusInternalServerError)
        ctx.SetBody([]byte("Proxy failed to connect. Please try again."))
        return
    }

    // Przekazanie odpowiedzi
    ctx.SetStatusCode(resp.StatusCode())
    ctx.SetBody(resp.Body())
    resp.Header.VisitAll(func(key, value []byte) {
        ctx.Response.Header.Set(string(key), string(value))
    })
}

// parsePath rozbija URI na service i ścieżkę (obsługuje /<service>/<rest>)
func parsePath(ctx *fasthttp.RequestCtx) (service string, rest string, err error) {
    path := string(ctx.URI().Path())
    trimmed := strings.TrimPrefix(path, "/")
    parts := strings.Split(trimmed, "/")

    if len(parts) >= 2 {
        service = parts[0]
        rest = strings.Join(parts[1:], "/")
    } else {
        return "", "", errors.New("URL format invalid.")
    }
    // Dodanie query string jeśli istnieje
    if qs := string(ctx.URI().QueryString()); qs != "" {
        rest += "?" + qs
    }
    return service, rest, nil
}

// iterativeRequest próbuje wysłać req do https://<service>.roblox.com/<path> z pętlą retry bez rekursji
func iterativeRequest(ctx *fasthttp.RequestCtx, service, path string) *fasthttp.Response {
    var resp *fasthttp.Response
    for attempt := 1; attempt <= retries; attempt++ {
        req := fasthttp.AcquireRequest()
        resp = fasthttp.AcquireResponse()

        // Budowanie URL docelowego
        req.SetRequestURI("https://" + service + ".roblox.com/" + path)
        req.Header.SetMethod(string(ctx.Method()))

        // Kopiowanie nagłówków od klienta
        ctx.Request.Header.VisitAll(func(key, value []byte) {
            req.Header.Set(string(key), string(value))
        })
        // Dodanie/zmiana nagłówków
        req.Header.Set("Accept", "application/json")
        if key := os.Getenv("KEY"); key != "" {
            req.Header.Set("PROXYKEY", key)
        }
        req.Header.Set("User-Agent", "RoProxy")
        req.Header.Del("Roblox-Id")

        // Wykonanie request
        err := client.Do(req, resp)
        fasthttp.ReleaseRequest(req)

        if err == nil {
            break // udało się, wychodzimy z pętli
        }
        // jeśli błąd, zwalniamy resp i próbujemy ponownie
        fasthttp.ReleaseResponse(resp)
        time.Sleep(100 * time.Millisecond) // krótka pauza między próbami
    }

    if resp == nil {
        resp = fasthttp.AcquireResponse()
        resp.SetStatusCode(fasthttp.StatusInternalServerError)
        resp.SetBody([]byte("Proxy failed to connect. Please try again."))
    }
    return resp
}
