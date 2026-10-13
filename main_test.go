package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

const (
	testBody = "0123456789"
	testETag = `"0123abcd"`
)

var testLastModified = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

// fakeS3 records the requests the proxy makes and answers them with the
// functions a test sets.
type fakeS3 struct {
	get   func(in *s3.GetObjectInput) (*s3.GetObjectOutput, error)
	head  func(in *s3.HeadObjectInput) (*s3.HeadObjectOutput, error)
	gets  []*s3.GetObjectInput
	heads []*s3.HeadObjectInput
}

func (f *fakeS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	copied := *in
	f.gets = append(f.gets, &copied)
	return f.get(in)
}

func (f *fakeS3) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	copied := *in
	f.heads = append(f.heads, &copied)
	return f.head(in)
}

func useFake(t *testing.T, f *fakeS3) {
	t.Helper()
	useClient(t, f, "test-bucket")
}

func useClient(t *testing.T, client s3API, bucket string) {
	t.Helper()
	savedClient, savedBucket, savedLogger := s3Client, bucketName, logger
	s3Client, bucketName, logger = client, bucket, slog.New(slog.NewTextHandler(io.Discard, nil))
	t.Cleanup(func() { s3Client, bucketName, logger = savedClient, savedBucket, savedLogger })
}

// object answers like S3 for testBody: the whole object, or the single
// range it was asked for.
func object(in *s3.GetObjectInput) (*s3.GetObjectOutput, error) {
	out := &s3.GetObjectOutput{
		CacheControl:  aws.String("public, max-age=3600"),
		ContentType:   aws.String("video/mp4"),
		ETag:          aws.String(testETag),
		LastModified:  aws.Time(testLastModified),
		ContentLength: aws.Int64(int64(len(testBody))),
		Body:          io.NopCloser(strings.NewReader(testBody)),
	}
	if in.Range != nil && *in.Range == "bytes=2-4" {
		out.ContentRange = aws.String("bytes 2-4/10")
		out.ContentLength = aws.Int64(3)
		out.Body = io.NopCloser(strings.NewReader(testBody[2:5]))
	}
	return out, nil
}

func objectHead(*s3.HeadObjectInput) (*s3.HeadObjectOutput, error) {
	return &s3.HeadObjectOutput{
		CacheControl:  aws.String("public, max-age=3600"),
		ContentType:   aws.String("video/mp4"),
		ETag:          aws.String(testETag),
		LastModified:  aws.Time(testLastModified),
		ContentLength: aws.Int64(int64(len(testBody))),
	}, nil
}

// s3Error builds an error shaped like the ones the SDK returns.
func s3Error(status int, code string, header http.Header) error {
	if header == nil {
		header = http.Header{}
	}
	return &awshttp.ResponseError{ResponseError: &smithyhttp.ResponseError{
		Response: &smithyhttp.Response{Response: &http.Response{StatusCode: status, Header: header}},
		Err:      &smithy.GenericAPIError{Code: code, Message: code},
	}}
}

func serve(method, path string, header map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	for k, v := range header {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	handleRequest(w, r)
	return w
}

func assertResponse(t *testing.T, w *httptest.ResponseRecorder, status int, body string, header map[string]string) {
	t.Helper()
	if w.Code != status {
		t.Errorf("status = %d, want %d", w.Code, status)
	}
	if got := w.Body.String(); got != body {
		t.Errorf("body = %q, want %q", got, body)
	}
	for k, want := range header {
		if got := w.Header().Get(k); got != want {
			t.Errorf("header %s = %q, want %q", k, got, want)
		}
	}
}

func TestGetWholeObject(t *testing.T) {
	f := &fakeS3{get: object}
	useFake(t, f)

	w := serve(http.MethodGet, "/stories/story.mp4", nil)

	assertResponse(t, w, http.StatusOK, testBody, map[string]string{
		"Accept-Ranges":  "bytes",
		"Cache-Control":  "public, max-age=3600",
		"Content-Type":   "video/mp4",
		"Content-Length": "10",
		"ETag":           testETag,
		"Last-Modified":  "Tue, 13 Oct 2026 12:00:00 GMT",
		"Content-Range":  "",
	})
	if len(f.gets) != 1 || *f.gets[0].Key != "stories/story.mp4" || *f.gets[0].Bucket != "test-bucket" || f.gets[0].Range != nil {
		t.Errorf("unexpected GetObject calls: %+v", f.gets)
	}
}

func TestGetRange(t *testing.T) {
	f := &fakeS3{get: object}
	useFake(t, f)

	w := serve(http.MethodGet, "/story.mp4", map[string]string{"Range": "bytes=2-4"})

	assertResponse(t, w, http.StatusPartialContent, "234", map[string]string{
		"Accept-Ranges":  "bytes",
		"Content-Range":  "bytes 2-4/10",
		"Content-Length": "3",
	})
	if aws.ToString(f.gets[0].Range) != "bytes=2-4" {
		t.Errorf("Range sent to S3 = %q", aws.ToString(f.gets[0].Range))
	}
}

func TestRangeSentToS3(t *testing.T) {
	tests := []struct {
		rangeHeader string
		want        string
	}{
		{"bytes=0-1", "bytes=0-1"},
		{"bytes=100-", "bytes=100-"},
		{"bytes=-500", "bytes=-500"},
		{"bytes=0-1,5-9", ""},
		{"bytes=5-2", ""},
		{"bytes=abc", ""},
		{"items=0-1", ""},
	}
	for _, tt := range tests {
		t.Run(tt.rangeHeader, func(t *testing.T) {
			f := &fakeS3{get: object}
			useFake(t, f)

			serve(http.MethodGet, "/story.mp4", map[string]string{"Range": tt.rangeHeader})

			if got := aws.ToString(f.gets[0].Range); got != tt.want {
				t.Errorf("Range sent to S3 = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHead(t *testing.T) {
	f := &fakeS3{head: objectHead}
	useFake(t, f)

	w := serve(http.MethodHead, "/story.mp4", nil)

	assertResponse(t, w, http.StatusOK, "", map[string]string{
		"Accept-Ranges":  "bytes",
		"Content-Length": "10",
		"ETag":           testETag,
		"Last-Modified":  "Tue, 13 Oct 2026 12:00:00 GMT",
	})
	if len(f.heads) != 1 || len(f.gets) != 0 {
		t.Errorf("HeadObject calls = %d, GetObject calls = %d", len(f.heads), len(f.gets))
	}
}

func TestHeadMissingObject(t *testing.T) {
	// HeadObject errors have no body, so the code is "NotFound", not "NoSuchKey".
	f := &fakeS3{head: func(*s3.HeadObjectInput) (*s3.HeadObjectOutput, error) {
		return nil, s3Error(http.StatusNotFound, "NotFound", nil)
	}}
	useFake(t, f)

	w := serve(http.MethodHead, "/missing.mp4", nil)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	useFake(t, &fakeS3{})

	w := serve(http.MethodPost, "/story.mp4", nil)

	assertResponse(t, w, http.StatusMethodNotAllowed, "Method not allowed\n", map[string]string{"Allow": "GET, HEAD"})
}

func TestEmptyKey(t *testing.T) {
	useFake(t, &fakeS3{})

	w := serve(http.MethodGet, "/", nil)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestPreconditionsSentToS3(t *testing.T) {
	date := "Tue, 13 Oct 2026 12:00:00 GMT"
	tests := []struct {
		name              string
		header            map[string]string
		ifMatch           string
		ifNoneMatch       string
		ifModifiedSince   bool
		ifUnmodifiedSince bool
	}{
		{"If-Match", map[string]string{"If-Match": testETag}, testETag, "", false, false},
		{"If-None-Match", map[string]string{"If-None-Match": testETag}, "", testETag, false, false},
		{"If-Modified-Since", map[string]string{"If-Modified-Since": date}, "", "", true, false},
		{"If-Unmodified-Since", map[string]string{"If-Unmodified-Since": date}, "", "", false, true},
		{"If-None-Match wins over If-Modified-Since", map[string]string{"If-None-Match": testETag, "If-Modified-Since": date}, "", testETag, false, false},
		{"If-Match wins over If-Unmodified-Since", map[string]string{"If-Match": testETag, "If-Unmodified-Since": date}, testETag, "", false, false},
		{"invalid date is ignored", map[string]string{"If-Modified-Since": "yesterday"}, "", "", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeS3{get: object, head: objectHead}
			useFake(t, f)

			serve(http.MethodGet, "/story.mp4", tt.header)
			serve(http.MethodHead, "/story.mp4", tt.header)

			get, head := f.gets[0], f.heads[0]
			if aws.ToString(get.IfMatch) != tt.ifMatch || aws.ToString(head.IfMatch) != tt.ifMatch {
				t.Errorf("If-Match = %q / %q, want %q", aws.ToString(get.IfMatch), aws.ToString(head.IfMatch), tt.ifMatch)
			}
			if aws.ToString(get.IfNoneMatch) != tt.ifNoneMatch || aws.ToString(head.IfNoneMatch) != tt.ifNoneMatch {
				t.Errorf("If-None-Match = %q / %q, want %q", aws.ToString(get.IfNoneMatch), aws.ToString(head.IfNoneMatch), tt.ifNoneMatch)
			}
			if (get.IfModifiedSince != nil) != tt.ifModifiedSince || (head.IfModifiedSince != nil) != tt.ifModifiedSince {
				t.Errorf("If-Modified-Since = %v / %v, want set: %v", get.IfModifiedSince, head.IfModifiedSince, tt.ifModifiedSince)
			}
			if (get.IfUnmodifiedSince != nil) != tt.ifUnmodifiedSince || (head.IfUnmodifiedSince != nil) != tt.ifUnmodifiedSince {
				t.Errorf("If-Unmodified-Since = %v / %v, want set: %v", get.IfUnmodifiedSince, head.IfUnmodifiedSince, tt.ifUnmodifiedSince)
			}
		})
	}
}

func TestNotModified(t *testing.T) {
	header := http.Header{}
	header.Set("ETag", testETag)
	header.Set("Last-Modified", "Tue, 13 Oct 2026 12:00:00 GMT")
	header.Set("Cache-Control", "public, max-age=3600")
	f := &fakeS3{get: func(*s3.GetObjectInput) (*s3.GetObjectOutput, error) {
		return nil, s3Error(http.StatusNotModified, "NotModified", header)
	}}
	useFake(t, f)

	w := serve(http.MethodGet, "/story.mp4", map[string]string{"If-None-Match": testETag})

	assertResponse(t, w, http.StatusNotModified, "", map[string]string{
		"ETag":          testETag,
		"Last-Modified": "Tue, 13 Oct 2026 12:00:00 GMT",
		"Cache-Control": "public, max-age=3600",
	})
}

func TestErrorMapping(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		body   string
	}{
		{"precondition failed", s3Error(http.StatusPreconditionFailed, "PreconditionFailed", nil), http.StatusPreconditionFailed, "The precondition given in the request failed\n"},
		{"no such key", s3Error(http.StatusNotFound, "NoSuchKey", nil), http.StatusNotFound, "The requested resource was not found\n"},
		{"no such bucket", s3Error(http.StatusNotFound, "NoSuchBucket", nil), http.StatusNotFound, "The requested source does not exist\n"},
		{"access denied", s3Error(http.StatusForbidden, "AccessDenied", nil), http.StatusForbidden, "Access to the requested resource is forbidden\n"},
		{"forbidden without a code", s3Error(http.StatusForbidden, "", nil), http.StatusForbidden, "Access to the requested resource is forbidden\n"},
		{"server error", s3Error(http.StatusInternalServerError, "InternalError", nil), http.StatusInternalServerError, "An error occurred while processing your request\n"},
		{"not an S3 error", errors.New("connection refused"), http.StatusInternalServerError, "An error occurred while processing your request\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			useFake(t, &fakeS3{get: func(*s3.GetObjectInput) (*s3.GetObjectOutput, error) { return nil, tt.err }})

			w := serve(http.MethodGet, "/story.mp4", nil)

			assertResponse(t, w, tt.status, tt.body, nil)
		})
	}
}

func TestRangeNotSatisfiable(t *testing.T) {
	f := &fakeS3{
		get: func(*s3.GetObjectInput) (*s3.GetObjectOutput, error) {
			return nil, s3Error(http.StatusRequestedRangeNotSatisfiable, "InvalidRange", nil)
		},
		head: objectHead,
	}
	useFake(t, f)

	w := serve(http.MethodGet, "/story.mp4", map[string]string{"Range": "bytes=100-"})

	assertResponse(t, w, http.StatusRequestedRangeNotSatisfiable, "The requested range is not satisfiable\n", map[string]string{
		"Content-Range": "bytes */10",
	})
}

func TestRangeNotSatisfiableWithStaleIfRange(t *testing.T) {
	// If-Range is evaluated before Range: a stale validator gets the whole
	// object even when the range does not fit the current one.
	f := &fakeS3{
		get: func(in *s3.GetObjectInput) (*s3.GetObjectOutput, error) {
			if in.Range != nil {
				return nil, s3Error(http.StatusRequestedRangeNotSatisfiable, "InvalidRange", nil)
			}
			return object(in)
		},
		head: objectHead,
	}
	useFake(t, f)

	w := serve(http.MethodGet, "/story.mp4", map[string]string{"Range": "bytes=100-", "If-Range": `"stale"`})

	assertResponse(t, w, http.StatusOK, testBody, nil)
}

func TestIfRange(t *testing.T) {
	tests := []struct {
		name    string
		ifRange string
		status  int
		body    string
		gets    int
	}{
		{"matching ETag", testETag, http.StatusPartialContent, "234", 1},
		{"stale ETag", `"stale"`, http.StatusOK, testBody, 2},
		{"weak ETag", "W/" + testETag, http.StatusOK, testBody, 2},
		{"matching date", "Tue, 13 Oct 2026 12:00:00 GMT", http.StatusPartialContent, "234", 1},
		{"other date", "Tue, 13 Oct 2026 11:59:59 GMT", http.StatusOK, testBody, 2},
		{"invalid value", "yesterday", http.StatusOK, testBody, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeS3{get: object}
			useFake(t, f)

			w := serve(http.MethodGet, "/story.mp4", map[string]string{"Range": "bytes=2-4", "If-Range": tt.ifRange})

			assertResponse(t, w, tt.status, tt.body, nil)
			if len(f.gets) != tt.gets {
				t.Errorf("GetObject calls = %d, want %d", len(f.gets), tt.gets)
			}
			if tt.gets == 2 && f.gets[1].Range != nil {
				t.Errorf("second GetObject still has Range %q", *f.gets[1].Range)
			}
		})
	}
}
