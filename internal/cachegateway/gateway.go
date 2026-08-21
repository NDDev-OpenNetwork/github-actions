package cachegateway

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

func ValidateListen(value string) error { return validateAddress(value, "9003") }
func ValidateUpstream(value *url.URL) error {
	if value == nil || value.Scheme != "http" || value.User != nil || value.Path != "" || value.RawQuery != "" || value.Fragment != "" {
		return errors.New("upstream must be an uncredentialed HTTP origin")
	}
	return validateAddress(value.Host, "9102")
}
func validateAddress(value, portWanted string) error {
	host, port, err := net.SplitHostPort(value)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || port != portWanted || ip.IsUnspecified() || ip.IsMulticast() {
		return errors.New("address must use a literal unicast IP and approved port")
	}
	return nil
}

func New(upstream *url.URL, logger *slog.Logger) (http.Handler, error) {
	if err := ValidateUpstream(upstream); err != nil {
		return nil, err
	}
	return newHandler(upstream, logger), nil
}
func newHandler(upstream *url.URL, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	proxy := &httputil.ReverseProxy{Rewrite: func(request *httputil.ProxyRequest) {
		incomingHost := request.In.Host
		request.SetURL(upstream)
		request.Out.Host = incomingHost
		for _, name := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
			request.Out.Header.Del(name)
		}
	}, ErrorHandler: func(writer http.ResponseWriter, request *http.Request, err error) {
		logger.ErrorContext(request.Context(), "cache S3 gateway upstream failure", "error", err)
		http.Error(writer, "cache upstream unavailable", http.StatusBadGateway)
	}, ModifyResponse: func(response *http.Response) error {
		response.Header.Set("X-Content-Type-Options", "nosniff")
		return nil
	}}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request == nil || request.URL == nil || request.URL.Path == "" || !strings.HasPrefix(request.URL.Path, "/") || request.URL.RawPath != "" {
			http.Error(writer, "bad request", http.StatusBadRequest)
			return
		}
		proxy.ServeHTTP(writer, request)
	})
}
