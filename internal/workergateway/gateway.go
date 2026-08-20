package workergateway

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
)

const (
	// The address a worker is told to call. It was the bridge gateway on the
	// worker's own host for as long as GARM ran there too. Clustering ended
	// that: a worker can be placed on any member, so the endpoint it reaches
	// has to be one address the whole fleet shares, and the default below is
	// only the single-host case.
	// ListenAddressRequired is deliberately empty. A clustered deployment has
	// no universal listen address; every unit must pass --listen explicitly.
	ListenAddressRequired = ""
	ExpectedUpstreamURL   = "http://127.0.0.1:9997"
	MaxRequestBodyBytes   = int64(1 << 20)
)

// Gateway exposes only the GARM endpoints required by disposable workers. The
// GARM administrative API remains bound to loopback and is never routed here.
type Gateway struct {
	proxy  *httputil.ReverseProxy
	logger *slog.Logger
}

func New(upstream *url.URL, logger *slog.Logger) (*Gateway, error) {
	if err := validateUpstream(upstream); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}

	proxy := &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(upstream)
			request.Out.Host = upstream.Host
			request.Out.Header.Del("Forwarded")
			request.Out.Header.Del("X-Forwarded-For")
			request.Out.Header.Del("X-Forwarded-Host")
			request.Out.Header.Del("X-Forwarded-Proto")
			request.SetXForwarded()
			request.Out.Header.Set("X-Forwarded-Proto", "https")
		},
		ErrorHandler: func(writer http.ResponseWriter, request *http.Request, err error) {
			logger.ErrorContext(request.Context(), "worker gateway upstream failure", "error", err)
			http.Error(writer, "upstream unavailable", http.StatusBadGateway)
		},
		ModifyResponse: func(response *http.Response) error {
			response.Header.Set("X-Content-Type-Options", "nosniff")
			response.Header.Set("Cache-Control", "no-store")
			return nil
		},
	}

	return &Gateway{proxy: proxy, logger: logger}, nil
}

func (g *Gateway) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request == nil {
		http.Error(writer, "invalid request", http.StatusBadRequest)
		return
	}
	if err := validateRequestTarget(request); err != nil {
		g.deny(writer, request, http.StatusBadRequest, err)
		return
	}
	allowedMethods, allowed := routePolicy(request.URL.Path)
	if !allowed {
		g.deny(writer, request, http.StatusNotFound, errors.New("route is not exposed"))
		return
	}
	if !methodAllowed(request.Method, allowedMethods) {
		writer.Header().Set("Allow", strings.Join(allowedMethods, ", "))
		g.deny(writer, request, http.StatusMethodNotAllowed, errors.New("method is not allowed"))
		return
	}
	if err := validateQuery(request); err != nil {
		g.deny(writer, request, http.StatusBadRequest, err)
		return
	}
	if request.ContentLength > MaxRequestBodyBytes {
		g.deny(writer, request, http.StatusRequestEntityTooLarge, errors.New("request body is too large"))
		return
	}

	request.Body = http.MaxBytesReader(writer, request.Body, MaxRequestBodyBytes)
	g.proxy.ServeHTTP(writer, request)
}

func (g *Gateway) deny(writer http.ResponseWriter, request *http.Request, status int, reason error) {
	g.logger.WarnContext(
		request.Context(),
		"worker gateway request denied",
		"method", request.Method,
		"path", request.URL.Path,
		"remote_address", request.RemoteAddr,
		"status", status,
		"reason", reason.Error(),
	)
	http.Error(writer, http.StatusText(status), status)
}

// The upstream was loopback-only for as long as GARM ran on the same host as
// the workers it served. Clustering ended that: a worker may be placed on any
// member, and the queue runs on none of them. The gateway therefore has to be
// able to cross one network hop -- and it is the right thing to do it, because
// it is the only component allowed to. A worker is rejected from every private
// destination except its own bridge, so widening the worker's reach instead
// would hand every disposable VM a route to the estate.
//
// What stays refused is anything that would make the hop ambiguous or
// credentialed: a name (DNS would decide which machine terminates the session),
// a path, a query, or embedded credentials.
func validateUpstream(upstream *url.URL) error {
	if upstream == nil {
		return errors.New("upstream URL is required")
	}
	if upstream.Scheme != "http" || upstream.User != nil || upstream.Path != "" ||
		upstream.RawQuery != "" || upstream.Fragment != "" {
		return errors.New("upstream must be an uncredentialed HTTP origin")
	}
	host, port, err := net.SplitHostPort(upstream.Host)
	if err != nil || net.ParseIP(host) == nil || port == "" {
		return errors.New("upstream must use a literal IP and explicit port")
	}
	if address := net.ParseIP(host); address.IsUnspecified() || address.IsMulticast() {
		return errors.New("upstream must name one reachable host")
	}
	return nil
}

func validateRequestTarget(request *http.Request) error {
	if request.URL == nil || request.URL.Path == "" || !strings.HasPrefix(request.URL.Path, "/") {
		return errors.New("request path must be absolute")
	}
	rawPath := request.URL.RawPath
	if rawPath != "" || strings.Contains(strings.SplitN(request.RequestURI, "?", 2)[0], "%") {
		return errors.New("encoded paths are not accepted")
	}
	if strings.Contains(request.URL.Path, "\\") || strings.Contains(request.URL.Path, "//") {
		return errors.New("ambiguous path is not accepted")
	}
	if request.URL.Path == "/" {
		return nil
	}
	withoutTrailingSlash := strings.TrimSuffix(request.URL.Path, "/")
	if path.Clean(withoutTrailingSlash) != withoutTrailingSlash {
		return errors.New("non-canonical path is not accepted")
	}
	return nil
}

func routePolicy(requestPath string) ([]string, bool) {
	canonical := strings.TrimSuffix(requestPath, "/")
	switch canonical {
	case "/api/v1/callbacks/status", "/api/v1/callbacks/system-info":
		return []string{http.MethodPost, http.MethodOptions}, true
	case "/api/v1/metadata/runner-metadata",
		"/api/v1/metadata/runner-registration-token",
		"/api/v1/metadata/system/service-name",
		"/api/v1/metadata/systemd/unit-file",
		"/api/v1/metadata/system/cert-bundle",
		"/api/v1/metadata/tools/garm-agent",
		"/api/v1/metadata/install-script":
		return []string{http.MethodGet, http.MethodOptions}, true
	}

	segments := strings.Split(strings.TrimPrefix(canonical, "/api/v1/metadata/"), "/")
	switch {
	case len(segments) == 2 && segments[0] == "credentials" && credentialNameAllowed(segments[1]):
		return []string{http.MethodGet, http.MethodOptions}, true
	case len(segments) == 3 && segments[0] == "tools" && segments[1] == "garm-agent" && decimalID(segments[2]):
		return []string{http.MethodGet, http.MethodOptions}, true
	case len(segments) == 4 && segments[0] == "tools" && segments[1] == "garm-agent" &&
		decimalID(segments[2]) && segments[3] == "download":
		return []string{http.MethodGet, http.MethodOptions}, true
	default:
		return nil, false
	}
}

func validateQuery(request *http.Request) error {
	canonical := strings.TrimSuffix(request.URL.Path, "/")
	if canonical != "/api/v1/metadata/systemd/unit-file" {
		if request.URL.RawQuery != "" {
			return errors.New("query parameters are not accepted for this route")
		}
		return nil
	}

	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		return errors.New("query parameters are malformed")
	}
	if len(query) == 0 {
		return nil
	}
	values, exists := query["runAsUser"]
	if !exists || len(query) != 1 || len(values) != 1 || values[0] != "runner" {
		return errors.New("only runAsUser=runner is accepted")
	}
	return nil
}

func methodAllowed(method string, allowed []string) bool {
	for _, candidate := range allowed {
		if method == candidate {
			return true
		}
	}
	return false
}

func credentialNameAllowed(name string) bool {
	switch name {
	case "runner", "credentials", "credentials_rsaparams":
		return true
	default:
		return false
	}
}

func decimalID(value string) bool {
	if value == "" || len(value) > 20 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func ProductionUpstream() (*url.URL, error) {
	upstream, err := url.Parse(ExpectedUpstreamURL)
	if err != nil {
		return nil, fmt.Errorf("parse compiled upstream URL: %w", err)
	}
	return upstream, nil
}

// ValidateListenAddress refuses anything but a literal IP and an explicit port.
// A name here would let DNS decide which machine terminates worker TLS, and the
// certificate the workers pin names addresses, not names.
func ValidateListenAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("listen address must be host:port: %w", err)
	}
	if net.ParseIP(host) == nil || port == "" {
		return errors.New("listen address must use a literal IP and explicit port")
	}
	return nil
}
