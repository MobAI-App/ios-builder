package otainstall

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The vectors below are the worked examples of the Amazon S3 API Reference,
// "Authenticating Requests (AWS Signature Version 4)": the header-signed
// examples and the presigned URL example, all for examplebucket in us-east-1
// on 24 May 2013 with the documentation's example key.
var (
	exampleCreds = AWSCredentials{AccessKeyID: "AKIAIOSFODNN7EXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}
	exampleTime  = time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
)

func exampleSigner() *sigv4 { return &sigv4{creds: exampleCreds, region: "us-east-1", service: "s3"} }

func TestSigV4PresignAWSVector(t *testing.T) {
	u, _ := url.Parse("https://examplebucket.s3.amazonaws.com/test.txt")
	got := exampleSigner().presign("GET", u, 86400*time.Second, exampleTime)
	want := "https://examplebucket.s3.amazonaws.com/test.txt" +
		"?X-Amz-Algorithm=AWS4-HMAC-SHA256" +
		"&X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20130524%2Fus-east-1%2Fs3%2Faws4_request" +
		"&X-Amz-Date=20130524T000000Z&X-Amz-Expires=86400&X-Amz-SignedHeaders=host" +
		"&X-Amz-Signature=aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404"
	if got != want {
		t.Errorf("presign\n got %s\nwant %s", got, want)
	}
}

func TestSigV4HeaderAWSVectors(t *testing.T) {
	for _, tc := range []struct {
		name, method, url, payload string
		headers                    map[string]string
		wantSigned, wantSig        string
	}{
		{
			name: "GET Object", method: "GET", url: "https://examplebucket.s3.amazonaws.com/test.txt", payload: emptySHA256,
			headers:    map[string]string{"Range": "bytes=0-9"},
			wantSigned: "host;range;x-amz-content-sha256;x-amz-date",
			wantSig:    "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41",
		},
		{
			name: "PUT Object", method: "PUT", url: "https://examplebucket.s3.amazonaws.com/test$file.text",
			payload:    "44ce7dd67c959e0d3524ffac1771dfbba87d2b6b4b4e99e42034a8b803f8b072",
			headers:    map[string]string{"Date": "Fri, 24 May 2013 00:00:00 GMT", "X-Amz-Storage-Class": "REDUCED_REDUNDANCY"},
			wantSigned: "date;host;x-amz-content-sha256;x-amz-date;x-amz-storage-class",
			wantSig:    "98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd",
		},
		{
			name: "GET Bucket Lifecycle", method: "GET", url: "https://examplebucket.s3.amazonaws.com/?lifecycle", payload: emptySHA256,
			wantSigned: "host;x-amz-content-sha256;x-amz-date",
			wantSig:    "fea454ca298b7da1c68078a5d1bdbfbbe0d65c699e0f91ac7a200a0136783543",
		},
		{
			name: "Get Bucket (List Objects)", method: "GET", url: "https://examplebucket.s3.amazonaws.com/?max-keys=2&prefix=J", payload: emptySHA256,
			wantSigned: "host;x-amz-content-sha256;x-amz-date",
			wantSig:    "34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, tc.url, nil)
			if err != nil {
				t.Fatal(err)
			}
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			exampleSigner().sign(req, tc.payload, exampleTime)
			want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, SignedHeaders=" + tc.wantSigned + ", Signature=" + tc.wantSig
			if got := req.Header.Get("Authorization"); got != want {
				t.Errorf("Authorization\n got %s\nwant %s", got, want)
			}
		})
	}
}

// A session token is signed into the query, and a non-default port stays in
// the signed host.
func TestSigV4PresignSessionTokenAndPort(t *testing.T) {
	s := &sigv4{creds: AWSCredentials{AccessKeyID: "AK", SecretAccessKey: "SK", SessionToken: "tok/en+="}, region: "auto", service: "s3"}
	u, _ := url.Parse("http://127.0.0.1:9000/bucket/a b.ipa")
	got := s.presign("GET", u, time.Hour, exampleTime)
	if !strings.HasPrefix(got, "http://127.0.0.1:9000/bucket/a%20b.ipa?") || !strings.Contains(got, "&X-Amz-Security-Token=tok%2Fen%2B%3D&") {
		t.Errorf("presign = %s", got)
	}
	if hostOf(u) != "127.0.0.1:9000" {
		t.Errorf("host = %s", hostOf(u))
	}
	u443, _ := url.Parse("https://h:443/x")
	if hostOf(u443) != "h" {
		t.Errorf("host = %s", hostOf(u443))
	}
}
