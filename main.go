package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var version = "dev"

var (
	bucketName string
	region     string
	port       string
	logLevel   string
	s3Client   *s3.Client
	logger     *slog.Logger
)

var rootCmd = &cobra.Command{
	Use:     "s3-proxy",
	Short:   "HTTP proxy server for S3 bucket objects",
	Long:    "An HTTP server that proxies GET requests to S3 bucket objects, preserving cache headers and returning generic error messages.",
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

	if err := rootCmd.MarkPersistentFlagRequired("bucket"); err != nil {
		panic(err)
	}

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
}

func initConfig() {
	viper.SetEnvPrefix("S3PROXY")
	viper.AutomaticEnv()
}

func runServer() error {
	bucketName = viper.GetString("bucket")
	region = viper.GetString("region")
	port = viper.GetString("port")
	logLevel = viper.GetString("loglevel")

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

	s3Client = s3.NewFromConfig(cfg)

	http.HandleFunc("/health", handleHealth)
	http.HandleFunc("/", loggingMiddleware(handleRequest))

	addr := fmt.Sprintf(":%s", port)
	logger.Info("Starting S3 proxy server",
		"address", addr,
		"bucket", bucketName,
		"region", region,
		"loglevel", logLevel)
	return http.ListenAndServe(addr, nil)
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
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get the object path from the request URL
	objectKey := r.URL.Path
	if len(objectKey) > 0 && objectKey[0] == '/' {
		objectKey = objectKey[1:]
	}

	// If path is empty, return error
	if objectKey == "" {
		http.Error(w, "The requested resource was not found", http.StatusNotFound)
		return
	}

	// Get object from S3
	input := &s3.GetObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(objectKey),
	}

	result, err := s3Client.GetObject(context.TODO(), input)
	if err != nil {
		handleS3Error(w, err)
		return
	}
	defer func() {
		if err := result.Body.Close(); err != nil {
			logger.Error("Error closing response body", "error", err)
		}
	}()

	// Pass through Cache-Control header if present
	if result.CacheControl != nil {
		w.Header().Set("Cache-Control", *result.CacheControl)
	}

	// Set Content-Type if available
	if result.ContentType != nil {
		w.Header().Set("Content-Type", *result.ContentType)
	}

	// Set Content-Length if available
	if result.ContentLength != nil {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", *result.ContentLength))
	}

	// Copy the S3 object body to response
	_, err = io.Copy(w, result.Body)
	if err != nil {
		logger.Error("Error writing response", "error", err)
	}
}

func handleS3Error(w http.ResponseWriter, err error) {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		switch ae.ErrorCode() {
		case "NoSuchKey":
			http.Error(w, "The requested resource was not found", http.StatusNotFound)
		case "AccessDenied", "Forbidden":
			http.Error(w, "Access to the requested resource is forbidden", http.StatusForbidden)
		case "InvalidBucketName", "NoSuchBucket":
			http.Error(w, "The requested source does not exist", http.StatusNotFound)
		default:
			http.Error(w, "An error occurred while processing your request", http.StatusInternalServerError)
		}
		logger.Error("S3 error", "code", ae.ErrorCode(), "message", ae.ErrorMessage())
	} else {
		http.Error(w, "An error occurred while processing your request", http.StatusInternalServerError)
		logger.Error("Error", "error", err)
	}
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
