# S3 Proxy

An HTTP server that proxies GET requests to S3 bucket objects, preserving cache headers and returning generic error messages.

## Features

- Proxies HTTP GET requests to S3 bucket objects
- Preserves original Cache-Control headers from S3
- Returns original HTTP status codes (404, 403, etc.) with generic error messages
- Access logging for all HTTP requests with timing and status information
- Configurable via command-line flags or environment variables
- Built with Cobra + Viper for flexible configuration
- Uses AWS SDK v2

## Requirements

- Go 1.21 or later
- AWS credentials configured (via environment variables, AWS credentials file, or IAM role)

## Installation

```bash
go build -o s3-proxy
```

## Usage

### Command Line

```bash
# Basic usage
./s3-proxy --bucket my-bucket --region us-west-2

# Custom port
./s3-proxy --bucket my-bucket --region us-west-2 --port 3000

# With debug logging
./s3-proxy --bucket my-bucket --region us-west-2 --loglevel debug

# With custom S3 endpoint (e.g., MinIO, S3-compatible service)
./s3-proxy --bucket my-bucket --region us-west-2 --endpoint https://minio.example.com

# With local MinIO (using path-style addressing)
./s3-proxy --bucket my-bucket --region us-east-1 --endpoint http://localhost:9000 --use-path-style
```

### Environment Variables

Environment variables use the `S3PROXY_` prefix:

```bash
export S3PROXY_BUCKET=my-bucket
export S3PROXY_REGION=us-west-2
export S3PROXY_PORT=8080
export S3PROXY_LOGLEVEL=info
export S3PROXY_ENDPOINT=https://s3.custom-domain.com
export S3PROXY_USE_PATH_STYLE=true
./s3-proxy
```

### Command-Line Flags

| Flag | Environment Variable | Default | Description |
|------|---------------------|---------|-------------|
| `--bucket` | `S3PROXY_BUCKET` | (required) | S3 bucket name |
| `--region` | `S3PROXY_REGION` | `us-east-1` | AWS region |
| `--port` | `S3PROXY_PORT` | `8080` | HTTP server port |
| `--loglevel` | `S3PROXY_LOGLEVEL` | `info` | Log level (debug, info, warn, error) |
| `--endpoint` | `S3PROXY_ENDPOINT` | | Custom S3 endpoint URL (falls back to `AWS_ENDPOINT_URL`) |
| `--use-path-style` | `S3PROXY_USE_PATH_STYLE` | `false` | Use path-style addressing for S3 (falls back to `AWS_S3_FORCE_PATH_STYLE`) |

## Endpoint Configuration

The S3 endpoint is determined in the following priority order:
1. `--endpoint` command-line flag
2. `S3PROXY_ENDPOINT` environment variable
3. `AWS_ENDPOINT_URL` environment variable (AWS CLI/SDK standard)
4. Default AWS S3 endpoints (if none specified)

Examples using `AWS_ENDPOINT_URL`:

```bash
# Using AWS standard environment variables (works with AWS CLI too)
export AWS_ENDPOINT_URL=http://localhost:9000
export AWS_S3_FORCE_PATH_STYLE=true
./s3-proxy --bucket my-bucket

# This is equivalent to:
./s3-proxy --bucket my-bucket --endpoint http://localhost:9000 --use-path-style
```

## AWS Credentials

The application uses AWS SDK v2 default credential chain, which checks:

1. Environment variables (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`)
2. Shared credentials file (`~/.aws/credentials`)
3. IAM role (when running on EC2, ECS, or Lambda)

Example:

```bash
export AWS_ACCESS_KEY_ID=your-access-key
export AWS_SECRET_ACCESS_KEY=your-secret-key
export AWS_REGION=us-west-2
./s3-proxy --bucket my-bucket
```

## Example Requests

```bash
# Start the server
./s3-proxy --bucket my-bucket --region us-west-2

# Request an object
curl http://localhost:8080/path/to/object.txt

# Request with path
curl http://localhost:8080/images/logo.png
```

## Error Handling

The server returns original HTTP status codes with generic error messages:

- **404 Not Found**: "The requested resource was not found"
- **403 Forbidden**: "Access to the requested resource is forbidden"
- **500 Internal Server Error**: "An error occurred while processing your request"

## Access Logging

All HTTP requests are logged with the following information:

- HTTP method
- Request path
- Remote address (client IP)
- HTTP status code
- Request duration in milliseconds
- User agent

Example access log output:

```text
time=2025-10-23T... level=INFO msg="HTTP request" method=GET path=/index.html remote_addr=127.0.0.1:52301 status=200 duration_ms=45 user_agent="Mozilla/5.0..."
time=2025-10-23T... level=INFO msg="HTTP request" method=GET path=/missing.txt remote_addr=127.0.0.1:52302 status=404 duration_ms=12 user_agent="curl/7.88.1"
```

## Development

```bash
# Run without building
go run main.go --bucket my-bucket --region us-west-2

# Build
go build -o s3-proxy

# Run tests (when added)
go test ./...
```

## License

MIT
