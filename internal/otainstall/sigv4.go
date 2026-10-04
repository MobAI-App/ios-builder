package otainstall

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// AWSCredentials sign S3 requests; SessionToken is set for temporary ones.
type AWSCredentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

// sigv4 is AWS Signature Version 4 for one region and service, just what the
// S3 backend needs: header-signed requests and query-presigned URLs.
type sigv4 struct {
	creds   AWSCredentials
	region  string
	service string
}

const (
	sigv4Algorithm = "AWS4-HMAC-SHA256"
	amzDateFormat  = "20060102T150405Z"
	// unsignedPayload lets a request stream a body it has not hashed.
	unsignedPayload = "UNSIGNED-PAYLOAD"
	// emptySHA256 is the hash of an empty body.
	emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// sign adds X-Amz-Date, X-Amz-Content-Sha256, X-Amz-Security-Token and
// Authorization to req. Every header already on req is signed, plus Host.
func (s *sigv4) sign(req *http.Request, payloadHash string, now time.Time) {
	now = now.UTC()
	req.Header.Set("X-Amz-Date", now.Format(amzDateFormat))
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if s.creds.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", s.creds.SessionToken)
	}
	headers := map[string]string{"host": hostOf(req.URL)}
	for k, v := range req.Header {
		headers[strings.ToLower(k)] = strings.Join(trimAll(v), ",")
	}
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, k := range names {
		canonHeaders.WriteString(k + ":" + headers[k] + "\n")
	}
	signed := strings.Join(names, ";")
	canonical := strings.Join([]string{
		req.Method, canonicalPath(req.URL), canonicalQuery(req.URL.Query()), canonHeaders.String(), signed, payloadHash,
	}, "\n")
	scope := s.scope(now)
	sig := s.signature(now, scope, canonical)
	req.Header.Set("Authorization", sigv4Algorithm+" Credential="+s.creds.AccessKeyID+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
}

// presign returns u with the query parameters that let anyone holding the
// URL make method on it until now+expires, signing only the host header.
func (s *sigv4) presign(method string, u *url.URL, expires time.Duration, now time.Time) string {
	now = now.UTC()
	scope := s.scope(now)
	q := u.Query()
	q.Set("X-Amz-Algorithm", sigv4Algorithm)
	q.Set("X-Amz-Credential", s.creds.AccessKeyID+"/"+scope)
	q.Set("X-Amz-Date", now.Format(amzDateFormat))
	q.Set("X-Amz-Expires", strconv.FormatInt(int64(expires/time.Second), 10))
	q.Set("X-Amz-SignedHeaders", "host")
	if s.creds.SessionToken != "" {
		q.Set("X-Amz-Security-Token", s.creds.SessionToken)
	}
	query := canonicalQuery(q)
	canonical := strings.Join([]string{
		method, canonicalPath(u), query, "host:" + hostOf(u) + "\n", "host", unsignedPayload,
	}, "\n")
	sig := s.signature(now, scope, canonical)
	return u.Scheme + "://" + u.Host + canonicalPath(u) + "?" + query + "&X-Amz-Signature=" + sig
}

func (s *sigv4) scope(now time.Time) string {
	return now.Format("20060102") + "/" + s.region + "/" + s.service + "/aws4_request"
}

func (s *sigv4) signature(now time.Time, scope, canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	toSign := sigv4Algorithm + "\n" + now.Format(amzDateFormat) + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
	key := hmacSHA256([]byte("AWS4"+s.creds.SecretAccessKey), now.Format("20060102"))
	key = hmacSHA256(key, s.region)
	key = hmacSHA256(key, s.service)
	key = hmacSHA256(key, "aws4_request")
	return hex.EncodeToString(hmacSHA256(key, toSign))
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

// hostOf is the Host header Go sends: the port is dropped when it is the
// scheme's default.
func hostOf(u *url.URL) string {
	host := u.Host
	if (u.Scheme == "https" && strings.HasSuffix(host, ":443")) || (u.Scheme == "http" && strings.HasSuffix(host, ":80")) {
		host = host[:strings.LastIndex(host, ":")]
	}
	return host
}

// canonicalPath is the URI-encoded path, slashes kept, as S3 wants it (S3 is
// the one service that does not double-encode).
func canonicalPath(u *url.URL) string {
	p := u.Path
	if p == "" {
		return "/"
	}
	return uriEncode(p, false)
}

// canonicalQuery sorts by key, then value, and encodes both.
func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, uriEncode(k, true)+"="+uriEncode(v, true))
		}
	}
	return strings.Join(parts, "&")
}

// uriEncode percent-encodes every byte but A-Z a-z 0-9 - . _ ~ (and '/'
// unless encodeSlash), in upper-case hex.
func uriEncode(s string, encodeSlash bool) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '.', c == '_', c == '~':
			b.WriteByte(c)
		case c == '/' && !encodeSlash:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&15])
		}
	}
	return b.String()
}

func trimAll(vs []string) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = strings.Join(strings.Fields(v), " ")
	}
	return out
}
