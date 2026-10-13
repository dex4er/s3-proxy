package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// TestS3Mock runs the proxy against a real S3 API, S3Mock in CI, so that
// what the unit tests assume about S3 responses is checked too. It needs
// S3PROXY_TEST_ENDPOINT, for example http://localhost:9090, and a bucket
// named by S3PROXY_TEST_BUCKET (default "test-bucket"); without the endpoint
// it is skipped.
func TestS3Mock(t *testing.T) {
	endpoint := os.Getenv("S3PROXY_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("S3PROXY_TEST_ENDPOINT is not set")
	}
	bucket := os.Getenv("S3PROXY_TEST_BUCKET")
	if bucket == "" {
		bucket = "test-bucket"
	}

	// The configuration is built by hand rather than loaded, so that no
	// local AWS profile or credentials take part in the test.
	cfg := aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
	}
	client := s3.NewFromConfig(cfg, s3ClientOptions(endpoint, true))

	payload := make([]byte, 300000)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	key := "stories/story.mp4"
	if _, err := client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket:       aws.String(bucket),
		Key:          aws.String(key),
		Body:         bytes.NewReader(payload),
		ContentType:  aws.String("video/mp4"),
		CacheControl: aws.String("public, max-age=3600"),
	}); err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	useClient(t, client, bucket)
	server := httptest.NewServer(http.HandlerFunc(handleRequest))
	t.Cleanup(server.Close)

	head := request(t, server.URL, http.MethodHead, "/"+key, nil)
	etag, lastModified := head.Header.Get("ETag"), head.Header.Get("Last-Modified")
	if etag == "" || lastModified == "" {
		t.Fatalf("HEAD returned no validators: ETag %q, Last-Modified %q", etag, lastModified)
	}

	size := len(payload)
	tests := []struct {
		name         string
		method       string
		path         string
		header       map[string]string
		status       int
		body         []byte
		contentRange string
	}{
		{"whole object", http.MethodGet, key, nil, http.StatusOK, payload, ""},
		{"first two bytes", http.MethodGet, key, map[string]string{"Range": "bytes=0-1"}, http.StatusPartialContent, payload[0:2], fmt.Sprintf("bytes 0-1/%d", size)},
		{"middle range", http.MethodGet, key, map[string]string{"Range": "bytes=1000-1999"}, http.StatusPartialContent, payload[1000:2000], fmt.Sprintf("bytes 1000-1999/%d", size)},
		{"suffix range", http.MethodGet, key, map[string]string{"Range": "bytes=-500"}, http.StatusPartialContent, payload[size-500:], fmt.Sprintf("bytes %d-%d/%d", size-500, size-1, size)},
		{"open range", http.MethodGet, key, map[string]string{"Range": "bytes=299990-"}, http.StatusPartialContent, payload[299990:], fmt.Sprintf("bytes 299990-%d/%d", size-1, size)},
		{"range past the end", http.MethodGet, key, map[string]string{"Range": "bytes=999999-"}, http.StatusRequestedRangeNotSatisfiable, nil, fmt.Sprintf("bytes */%d", size)},
		{"multiple ranges", http.MethodGet, key, map[string]string{"Range": "bytes=0-1,5-9"}, http.StatusOK, payload, ""},
		{"HEAD", http.MethodHead, key, nil, http.StatusOK, nil, ""},
		{"HEAD missing", http.MethodHead, "stories/missing.mp4", nil, http.StatusNotFound, nil, ""},
		{"GET missing", http.MethodGet, "stories/missing.mp4", nil, http.StatusNotFound, nil, ""},
		{"If-None-Match", http.MethodGet, key, map[string]string{"If-None-Match": etag}, http.StatusNotModified, nil, ""},
		{"If-None-Match on HEAD", http.MethodHead, key, map[string]string{"If-None-Match": etag}, http.StatusNotModified, nil, ""},
		{"If-Modified-Since", http.MethodGet, key, map[string]string{"If-Modified-Since": lastModified}, http.StatusNotModified, nil, ""},
		{"If-Match with another ETag", http.MethodGet, key, map[string]string{"If-Match": `"another"`}, http.StatusPreconditionFailed, nil, ""},
		{"If-Range matching", http.MethodGet, key, map[string]string{"If-Range": etag, "Range": "bytes=0-1"}, http.StatusPartialContent, payload[0:2], fmt.Sprintf("bytes 0-1/%d", size)},
		{"If-Range stale", http.MethodGet, key, map[string]string{"If-Range": `"stale"`, "Range": "bytes=0-1"}, http.StatusOK, payload, ""},
		{"If-Range stale past the end", http.MethodGet, key, map[string]string{"If-Range": `"stale"`, "Range": "bytes=999999-"}, http.StatusOK, payload, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := request(t, server.URL, tt.method, "/"+tt.path, tt.header)

			if resp.StatusCode != tt.status {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.status)
			}
			if got := resp.Header.Get("Content-Range"); got != tt.contentRange {
				t.Errorf("Content-Range = %q, want %q", got, tt.contentRange)
			}
			if tt.body != nil && !bytes.Equal(resp.body, tt.body) {
				t.Errorf("body is %d bytes, want %d bytes of the object", len(resp.body), len(tt.body))
			}
			if tt.status == http.StatusOK || tt.status == http.StatusPartialContent {
				for _, name := range []string{"Accept-Ranges", "ETag", "Last-Modified", "Content-Type", "Cache-Control"} {
					if resp.Header.Get(name) == "" {
						t.Errorf("no %s header", name)
					}
				}
			}
		})
	}
}

type response struct {
	*http.Response
	body []byte
}

func request(t *testing.T, base, method, path string, header map[string]string) response {
	t.Helper()
	req, err := http.NewRequest(method, base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response{resp, body}
}
