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

    rawPath := string(ctx.URI().Path())
    // Bezpośrednie proxy dla /v1/users
    if strings.HasPrefix(rawPath, "/v1/users/") {
        forwardOnce(ctx, "https://users.roblox.com"+rawPath)
        return
    }

    // Parsowanie ścieżki
    service, rest, err := parsePath(ctx)
    if err != nil {
        ctx.SetStatusCode(fasthttp.StatusBadRequest)
        ctx.SetBody([]byte(err.Error()))
        return
    }

    // Jednorazowe proxy dla innych serwisów
    forwardOnce(ctx, "https://"+service+".roblox.com/"+rest)
}

// forwardOnce wysyła pojedyncze zapytanie i zwraca odpowiedź, bez retry
func forwardOnce(ctx *fasthttp.RequestCtx, url string) {
    req := fasthttp.AcquireRequest()
    resp := fasthttp.AcquireResponse()
    defer fasthttp.ReleaseRequest(req)
    defer fasthttp.ReleaseResponse(resp)

    // Budowanie URL z query
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

// parsePath rozbija URI na service i ścieżkę
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
    if qs := string(ctx.URI().QueryString()); qs != "" {
        rest += "?" + qs
    }
    return service, rest, nil
}
