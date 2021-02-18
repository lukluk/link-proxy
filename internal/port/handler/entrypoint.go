package handler

import (
	"github.com/lukluk/link-proxy/config"
	"github.com/lukluk/link-proxy/config/circuitbreaker"
	"github.com/lukluk/link-proxy/config/upstream"
	"github.com/lukluk/link-proxy/internal/adapter/proxy"
	"github.com/lukluk/link-proxy/internal/adapter/storage/inmemory"
	"github.com/lukluk/link-proxy/internal/adapter/storage/inmemory/scheme"
	"github.com/lukluk/link-proxy/internal/domain/validation"
	"github.com/rs/zerolog/log"
	"io/ioutil"
	"net/http"
	"strings"
)

type entryPoint struct {
	config config.Config
	circuitBreakerData *inmemory.CircuitBreakerData
	validation validation.IValidation
}
func NewEntryPoint(cfg config.Config,
	circuitBreakerData *inmemory.CircuitBreakerData,
	iValidation validation.IValidation) *entryPoint {
	return &entryPoint{
		cfg,
		circuitBreakerData,
		iValidation,
	}
}
func (e *entryPoint) Handler() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request){
		e.proxy(w, r)
	})
}

func (e *entryPoint) proxy(w http.ResponseWriter, r *http.Request) {
	backend, backendId := findUpstreamsByPathURL(r.URL.EscapedPath(), e.config.Upstreams)
	if backendId == "" {
		w.WriteHeader(http.StatusNotAcceptable)
		return
	}
	cbKey, err := e.buildRequestKey(backend, r)
	if err != nil {
		log.Warn().Msgf("failed to build proxy key," +
			"this request will forwarded but circuit breaker will not applied, error: %v", err)
		forwardAndResponse(backend.Host, w, r)
		return
	}
	instance := e.circuitBreakerData.Get(cbKey)
	if instance.Traffic.IsOnClosed() {
		fallback(w, e.config.CircuitBreaker.Fallback)
		return
	}
	if instance.Traffic.Check()  {
		instance.Traffic.IncTrafficCount()
		statusCode, respBody := forwardAndResponse(backend.Host, w, r)
		e.updateStat(backendId, instance, respBody, statusCode)
	} else {
		fallback(w, e.config.CircuitBreaker.Fallback)
		return
	}
}

func (e *entryPoint) updateStat(backendId string, instance *scheme.Instance, respBody []byte, statusCode int) {
	if statusCode >= 500 ||  e.validation.ValidateAdditionalErrorsFromResponse(backendId, respBody) {
		instance.Stats.IncErrorCount()
	}
}


func forwardAndResponse(host string, w http.ResponseWriter, r *http.Request) (int, []byte) {
	resp, err := proxy.ForwardRequest(host, r)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return http.StatusBadGateway, nil
	}
	if resp.Header != nil {
		copyHeader(w, resp.Header)
	}
	w.WriteHeader(resp.StatusCode)
	var body []byte
	if resp.Body != nil {
		body, _ = ioutil.ReadAll(resp.Body)
		w.Write(body)
	}
	return resp.StatusCode, body
}

func copyHeader(w http.ResponseWriter, sourceHeader http.Header) {
	for header, values := range sourceHeader {
		for _, value := range values {
			w.Header().Add(header, value)
		}
	}
}

func fallback(w http.ResponseWriter, fb circuitbreaker.Fallback) {
	w.WriteHeader(fb.HttpStatus)
	return
}

func findUpstreamsByPathURL(path string, upstreamMap map[string]upstream.Upstream) (upstream.Upstream, string) {
	for key, val := range upstreamMap {
		if strings.Contains(path, key) {
			return val, key
		}
	}
	return upstream.Upstream{}, ""
}