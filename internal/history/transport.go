package history

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// transport.go reads authenticated local client resources without exposing credentials.
// The self-signed-certificate exception applies exclusively to the fixed loopback
// origin assembled by Get. Remote SGP and public configuration use a separate
// certificate-validating client, and neither client follows redirects carrying credentials.

// Access identifies one in-memory authenticated client and its same-region account context.
type Access struct {
	Port     int    // Local HTTPS listening port.
	Token    string // Ephemeral local authentication secret.
	Region   string // Current client region code.
	Platform string // Current game server code.
	Account  string // Current logged-in account identifier.
}

// HTTPError retains an HTTP classification without the authenticated URL or headers.
type HTTPError struct {
	Status int // Returned server HTTP status.
}

// Error returns a sanitized status message suitable for browser-visible state.
func (e HTTPError) Error() string { return fmt.Sprintf("数据接口 HTTP %d", e.Status) }

// Transport instances separate trusted public HTTPS from loopback self-signed traffic and forbid automatic authenticated redirects.
var localHTTP = &http.Client{Timeout: 6 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, MaxIdleConnsPerHost: 8}, CheckRedirect: noRedirect}
var remoteHTTP = &http.Client{Timeout: 12 * time.Second, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, MaxIdleConnsPerHost: 8}, CheckRedirect: noRedirect}

// noRedirect prevents credentials from following another origin through an HTTP redirect.
func noRedirect(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }

// Get reads one internally selected loopback resource into the caller's destination.
// Paths must be absolute relative resources rather than arbitrary URLs. Cancellation
// interrupts the request, and errors omit request paths, account identities, and credentials.
func Get(ctx context.Context, a Access, path string, out any) error {
	if a.Port < 1 || a.Port > 65535 || a.Token == "" || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return errors.New("客户端连接参数未就绪")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://127.0.0.1:"+strconv.Itoa(a.Port)+path, nil)
	if err != nil {
		return errors.New("客户端读取参数无效")
	}
	req.SetBasicAuth("riot", a.Token)
	return decode(localHTTP, req, out)
}

// decode bounds response bytes and classifies failures without returning authenticated request material.
// The caller supplies an origin-appropriate client. JSON numbers retain their integer
// spelling before domain adapters consume them, avoiding precision changes during normalization.
func decode(client *http.Client, req *http.Request, out any) error {
	response, err := client.Do(req)
	if err != nil {
		if req.Context().Err() != nil {
			return req.Context().Err()
		}
		return errors.New("数据接口连接失败或超时")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return HTTPError{Status: response.StatusCode}
	}
	if out == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return err
	}
	// Bound one provider response to sixteen MiB so malformed remote payloads cannot exhaust the process while decoding.
	decoder := json.NewDecoder(io.LimitReader(response.Body, 16*1024*1024))
	decoder.UseNumber()
	if err := decoder.Decode(out); err != nil {
		return errors.New("数据接口返回格式不可读")
	}
	return nil
}

// remoteGet reads a validated SGP origin using only its current bearer credential.
func remoteGet(ctx context.Context, access sgpAccess, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(access.Origin, "/")+path, nil)
	if err != nil {
		return errors.New("战绩请求参数无效")
	}
	req.Header.Set("Authorization", "Bearer "+access.Bearer)
	return decode(remoteHTTP, req, out)
}
