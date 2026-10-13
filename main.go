package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var version = "dev"

// s3API is the part of the S3 client the proxy uses, so that tests can
// replace it.
type s3API interface {
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	HeadObject(ctx context.Context, params *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
}

var (
	bucketName string
	region     string
	port       string
	logLevel   string
	endpoint   string
	pathStyle  bool
	s3Client   s3API
	logger     *slog.Logger
)

var rootCmd = &cobra.Command{
	Use:     "s3-proxy",
	Short:   "HTTP proxy server for S3 bucket objects",
	Long:    "An HTTP server that proxies GET and HEAD requests to S3 bucket objects, preserving cache headers and returning generic error messages.",
	Version: version,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runServer()
	},
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().StringVar(&bucketName, "bucket", "", "S3 bucket name (required)")
	rootCmd.PersistentFlags().StringVar(&region, "region", "us-east-1", "AWS region")
	rootCmd.PersistentFlags().StringVar(&port, "port", "8080", "HTTP server port")
	rootCmd.PersistentFlags().StringVar(&logLevel, "loglevel", "info", "Log level (debug, info, warn, error)")
	rootCmd.PersistentFlags().StringVar(&endpoint, "endpoint", "", "S3 endpoint URL (default: the AWS endpoint for the region)")
	rootCmd.PersistentFlags().BoolVar(&pathStyle, "path-style", false, "Use path-style addressing (bucket in the URL path)")

	if err := viper.BindPFlag("bucket", rootCmd.PersistentFlags().Lookup("bucket")); err != nil {
		panic(err)
	}
	if err := viper.BindPFlag("region", rootCmd.PersistentFlags().Lookup("region")); err != nil {
		panic(err)
	}
	if err := viper.BindPFlag("port", rootCmd.PersistentFlags().Lookup("port")); err != nil {
		panic(err)
	}
	if err := viper.BindPFlag("loglevel", rootCmd.PersistentFlags().Lookup("loglevel")); err != nil {
		panic(err)
	}
	if err := viper.BindPFlag("endpoint", rootCmd.PersistentFlags().Lookup("endpoint")); err != nil {
		panic(err)
	}
	if err := viper.BindPFlag("path-style", rootCmd.PersistentFlags().Lookup("path-style")); err != nil {
		panic(err)
	}
}

func initConfig() {
	viper.SetEnvPrefix("S3PROXY")
	// --path-style is read from S3PROXY_PATH_STYLE.
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	viper.AutomaticEnv()
}

func runServer() error {
	bucketName = viper.GetString("bucket")
	region = viper.GetString("region")
	port = viper.GetString("port")
	logLevel = viper.GetString("loglevel")
	endpoint = viper.GetString("endpoint")
	pathStyle = viper.GetBool("path-style")

	// Initialize logger
	var level slog.Level
	switch strings.ToLower(logLevel) {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	})
	logger = slog.New(handler)

	if bucketName == "" {
		return fmt.Errorf("bucket name is required")
	}

	// Initialize AWS SDK v2
	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion(region),
	)
	if err != nil {
		return fmt.Errorf("failed to load AWS config: %w", err)
	}

	s3Client = s3.NewFromConfig(cfg, s3ClientOptions(endpoint, pathStyle))

	http.HandleFunc("/health", handleHealth)
	http.HandleFunc("/", loggingMiddleware(handleRequest))

	addr := fmt.Sprintf(":%s", port)
	logger.Info("Starting S3 proxy server",
		"address", addr,
		"bucket", bucketName,
		"region", region,
		"endpoint", endpoint,
		"path_style", pathStyle,
		"loglevel", logLevel)
	return http.ListenAndServe(addr, nil)
}

// s3ClientOptions points the client at another S3-compatible endpoint, if
// one is given. Path-style addressing puts the bucket in the URL path, which
// servers without per-bucket host names, such as S3Mock, need.
func s3ClientOptions(endpoint string, pathStyle bool) func(*s3.Options) {
	return func(o *s3.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
		o.UsePathStyle = pathStyle
	}
}

// responseWriter wraps http.ResponseWriter to capture the status code
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func newResponseWriter(w http.ResponseWriter) *responseWriter {
	return &responseWriter{w, http.StatusOK}
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// loggingMiddleware logs HTTP access information
func loggingMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		wrapped := newResponseWriter(w)

		next(wrapped, r)

		duration := time.Since(start)
		logger.Debug("HTTP request",
			"method", r.Method,
			"path", r.URL.Path,
			"remote_addr", r.RemoteAddr,
			"status", wrapped.statusCode,
			"duration_ms", duration.Milliseconds(),
			"user_agent", r.UserAgent(),
		)
	}
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func handleRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get the object path from the request URL
	objectKey := strings.TrimPrefix(r.URL.Path, "/")

	// If path is empty, return error
	if objectKey == "" {
		http.Error(w, "The requested resource was not found", http.StatusNotFound)
		return
	}

	if r.Method == http.MethodHead {
		headObject(w, r, objectKey)
		return
	}
	getObject(w, r, objectKey)
}

func headObject(w http.ResponseWriter, r *http.Request, objectKey string) {
	p := requestPreconditions(r)
	result, err := s3Client.HeadObject(r.Context(), &s3.HeadObjectInput{
		Bucket:            aws.String(bucketName),
		Key:               aws.String(objectKey),
		IfMatch:           p.ifMatch,
		IfNoneMatch:       p.ifNoneMatch,
		IfModifiedSince:   p.ifModifiedSince,
		IfUnmodifiedSince: p.ifUnmodifiedSince,
	})
	if err != nil {
		handleS3Error(w, err)
		return
	}

	setObjectHeaders(w.Header(), result.CacheControl, result.ContentType, result.ETag, result.LastModified)
	setContentLength(w.Header(), result.ContentLength)
	w.WriteHeader(http.StatusOK)
}

func getObject(w http.ResponseWriter, r *http.Request, objectKey string) {
	p := requestPreconditions(r)
	input := &s3.GetObjectInput{
		Bucket:            aws.String(bucketName),
		Key:               aws.String(objectKey),
		IfMatch:           p.ifMatch,
		IfNoneMatch:       p.ifNoneMatch,
		IfModifiedSince:   p.ifModifiedSince,
		IfUnmodifiedSince: p.ifUnmodifiedSince,
	}
	if byteRange := requestRange(r); byteRange != "" {
		input.Range = aws.String(byteRange)
	}
	ifRange := r.Header.Get("If-Range")

	result, err := s3Client.GetObject(r.Context(), input)

	// The part is only valid if the object is still the one the client got
	// its validator from. Checking the response rather than asking first
	// keeps the common case to one request and leaves no window for the
	// object to change in between.
	if err == nil && input.Range != nil && ifRange != "" && !ifRangeMatches(ifRange, result.ETag, result.LastModified) {
		closeBody(result.Body)
		input.Range = nil
		result, err = s3Client.GetObject(r.Context(), input)
	}

	if err != nil {
		if input.Range != nil && isInvalidRange(err) {
			rangeNotSatisfiable(w, r, input, ifRange)
			return
		}
		handleS3Error(w, err)
		return
	}

	writeObject(w, result)
}

// rangeNotSatisfiable answers a range that starts past the end of the
// object. The 416 has to carry the object size, which the S3 error does
// not, hence the HeadObject. If-Range is evaluated before Range, so a stale
// validator still gets the whole object instead.
func rangeNotSatisfiable(w http.ResponseWriter, r *http.Request, input *s3.GetObjectInput, ifRange string) {
	head, err := s3Client.HeadObject(r.Context(), &s3.HeadObjectInput{
		Bucket:            input.Bucket,
		Key:               input.Key,
		IfMatch:           input.IfMatch,
		IfNoneMatch:       input.IfNoneMatch,
		IfModifiedSince:   input.IfModifiedSince,
		IfUnmodifiedSince: input.IfUnmodifiedSince,
	})
	if err != nil {
		handleS3Error(w, err)
		return
	}

	if ifRange != "" && !ifRangeMatches(ifRange, head.ETag, head.LastModified) {
		input.Range = nil
		result, err := s3Client.GetObject(r.Context(), input)
		if err != nil {
			handleS3Error(w, err)
			return
		}
		writeObject(w, result)
		return
	}

	if head.ContentLength != nil {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", *head.ContentLength))
	}
	http.Error(w, "The requested range is not satisfiable", http.StatusRequestedRangeNotSatisfiable)
}

func writeObject(w http.ResponseWriter, result *s3.GetObjectOutput) {
	defer closeBody(result.Body)

	setObjectHeaders(w.Header(), result.CacheControl, result.ContentType, result.ETag, result.LastModified)
	setContentLength(w.Header(), result.ContentLength)

	status := http.StatusOK
	if result.ContentRange != nil {
		w.Header().Set("Content-Range", *result.ContentRange)
		status = http.StatusPartialContent
	}
	w.WriteHeader(status)

	// Copy the S3 object body to response
	if _, err := io.Copy(w, result.Body); err != nil {
		logger.Error("Error writing response", "error", err)
	}
}

func closeBody(body io.ReadCloser) {
	if err := body.Close(); err != nil {
		logger.Error("Error closing response body", "error", err)
	}
}

// setObjectHeaders copies the object metadata a client needs to cache the
// object and to ask for parts of it.
func setObjectHeaders(h http.Header, cacheControl, contentType, etag *string, lastModified *time.Time) {
	h.Set("Accept-Ranges", "bytes")
	if cacheControl != nil {
		h.Set("Cache-Control", *cacheControl)
	}
	if contentType != nil {
		h.Set("Content-Type", *contentType)
	}
	if etag != nil {
		h.Set("ETag", *etag)
	}
	if lastModified != nil {
		h.Set("Last-Modified", lastModified.UTC().Format(http.TimeFormat))
	}
}

func setContentLength(h http.Header, contentLength *int64) {
	if contentLength != nil {
		h.Set("Content-Length", strconv.FormatInt(*contentLength, 10))
	}
}

// singleRange matches one byte range: "bytes=first-[last]" or "bytes=-suffix".
var singleRange = regexp.MustCompile(`^bytes=(?:(\d+)-(\d*)|-\d+)$`)

// requestRange returns the Range header when it asks for a single byte
// range, and "" otherwise. S3 serves one range per request, and RFC 9110
// lets a server ignore a Range it does not support or cannot parse, which
// then gets the whole object with 200.
func requestRange(r *http.Request) string {
	value := r.Header.Get("Range")
	m := singleRange.FindStringSubmatch(value)
	if m == nil {
		return ""
	}
	if m[1] != "" && m[2] != "" {
		first, errFirst := strconv.ParseInt(m[1], 10, 64)
		last, errLast := strconv.ParseInt(m[2], 10, 64)
		if errFirst != nil || errLast != nil || last < first {
			return ""
		}
	}
	return value
}

// preconditions are the conditional request headers S3 evaluates itself.
type preconditions struct {
	ifMatch           *string
	ifNoneMatch       *string
	ifModifiedSince   *time.Time
	ifUnmodifiedSince *time.Time
}

// requestPreconditions reads the conditional headers. As RFC 9110 requires,
// a date condition is dropped when the ETag condition it pairs with is
// present, and an invalid date is ignored.
func requestPreconditions(r *http.Request) preconditions {
	var p preconditions
	if v := r.Header.Get("If-Match"); v != "" {
		p.ifMatch = aws.String(v)
	} else {
		p.ifUnmodifiedSince = headerTime(r, "If-Unmodified-Since")
	}
	if v := r.Header.Get("If-None-Match"); v != "" {
		p.ifNoneMatch = aws.String(v)
	} else {
		p.ifModifiedSince = headerTime(r, "If-Modified-Since")
	}
	return p
}

func headerTime(r *http.Request, name string) *time.Time {
	t, err := http.ParseTime(r.Header.Get(name))
	if err != nil {
		return nil
	}
	return &t
}

// ifRangeMatches evaluates If-Range against the object S3 returned. S3 has
// no If-Range, so the proxy does it: a strong ETag must match exactly and an
// HTTP-date must equal Last-Modified. Anything else, a weak ETag included,
// does not match, and the client gets the whole object.
func ifRangeMatches(ifRange string, etag *string, lastModified *time.Time) bool {
	if strings.HasPrefix(ifRange, `"`) {
		return etag != nil && *etag == ifRange
	}
	if strings.HasPrefix(ifRange, "W/") {
		return false
	}
	t, err := http.ParseTime(ifRange)
	return err == nil && lastModified != nil && lastModified.Truncate(time.Second).Equal(t)
}

func isInvalidRange(err error) bool {
	var re *awshttp.ResponseError
	if errors.As(err, &re) && re.Response != nil && re.HTTPStatusCode() == http.StatusRequestedRangeNotSatisfiable {
		return true
	}
	var ae smithy.APIError
	return errors.As(err, &ae) && ae.ErrorCode() == "InvalidRange"
}

// handleS3Error maps an S3 error to the client response. The HTTP status
// decides where there is one: HeadObject errors have no body, so their code
// is only "NotFound" or "Forbidden", and to the SDK a 304 is an error, not a
// response.
func handleS3Error(w http.ResponseWriter, err error) {
	status := 0
	var re *awshttp.ResponseError
	if errors.As(err, &re) && re.Response != nil {
		status = re.HTTPStatusCode()
	}
	code, message := "", err.Error()
	var ae smithy.APIError
	if errors.As(err, &ae) {
		code, message = ae.ErrorCode(), ae.ErrorMessage()
	}

	switch {
	case status == http.StatusNotModified:
		// A 304 carries the validators and caching headers a 200 would.
		for _, name := range []string{"Cache-Control", "ETag", "Last-Modified"} {
			if v := re.Response.Header.Get(name); v != "" {
				w.Header().Set(name, v)
			}
		}
		w.WriteHeader(http.StatusNotModified)
		logger.Debug("S3 object not modified")
		return
	case status == http.StatusPreconditionFailed || code == "PreconditionFailed":
		http.Error(w, "The precondition given in the request failed", http.StatusPreconditionFailed)
	case code == "InvalidBucketName" || code == "NoSuchBucket":
		http.Error(w, "The requested source does not exist", http.StatusNotFound)
	case code == "NoSuchKey" || status == http.StatusNotFound:
		http.Error(w, "The requested resource was not found", http.StatusNotFound)
	case code == "AccessDenied" || code == "Forbidden" || status == http.StatusForbidden:
		http.Error(w, "Access to the requested resource is forbidden", http.StatusForbidden)
	default:
		http.Error(w, "An error occurred while processing your request", http.StatusInternalServerError)
	}
	logger.Error("S3 error", "status", status, "code", code, "message", message)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
