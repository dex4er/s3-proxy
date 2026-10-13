# S3 Proxy

An HTTP server that proxies GET and HEAD requests to S3 bucket objects, with byte ranges and conditional requests, preserving cache headers and returning generic error messages.

## Features

- Proxies HTTP GET and HEAD requests to S3 bucket objects
- Byte-range requests (`Range`, answered with `206 Partial Content`), which video playback in Safari and on iOS requires
- Conditional requests (`If-None-Match`, `If-Modified-Since`, `If-Match`, `If-Unmodified-Since`, `If-Range`)
- Preserves the `Cache-Control`, `Content-Type`, `ETag` and `Last-Modified` headers from S3
- Works with S3-compatible servers through `--endpoint` and `--path-style`
- Returns original HTTP status codes (404, 403, etc.) with generic error messages
- Access logging for all HTTP requests with timing and status information
- Configurable via command-line flags or environment variables
- Built with Cobra + Viper for flexible configuration
- Uses AWS SDK v2

## Requirements

- AWS credentials configured (via environment variables, AWS credentials file, or IAM role)

## How to build

On Mac and Linux install tools using [mise](https://mise.jdx.dev/):

```sh
mise install
make download
make build
```

On Windows:

```powershell
winget install GoLang.Go
winget install goreleaser.goreleaser
./Makefile.ps1 download
./Makefile.ps1 build
```

## How to verify a release

Every released binary is published with an SPDX SBOM
(`<binary>.sbom.json`) and a build provenance attestation. Both the binaries
and the SBOMs can be verified with the GitHub CLI:

```sh
gh attestation verify s3-proxy-linux-amd64 --repo dex4er/s3-proxy
```

Note that `gh attestation verify` prints its result only on a terminal: in CI
or when its output is piped it reports through the exit status alone. Use
`--format json` when a script needs the details.

The current vulnerability reports are in
[Security scan for main branch](https://github.com/dex4er/s3-proxy/discussions/28)
and
[Security scan for the last release](https://github.com/dex4er/s3-proxy/discussions/29).

## Usage

### Command Line

```bash
# Basic usage
./s3-proxy --bucket my-bucket --region us-west-2

# Custom port
./s3-proxy --bucket my-bucket --region us-west-2 --port 3000

# With debug logging
./s3-proxy --bucket my-bucket --region us-west-2 --loglevel debug
```

### Environment Variables

Environment variables use the `S3PROXY_` prefix:

```bash
export S3PROXY_BUCKET=my-bucket
export S3PROXY_REGION=us-west-2
export S3PROXY_PORT=8080
export S3PROXY_LOGLEVEL=info
./s3-proxy
```

### Command-Line Flags

| Flag           | Environment Variable | Default      | Description                                             |
|----------------|----------------------|--------------|---------------------------------------------------------|
| `--bucket`     | `S3PROXY_BUCKET`     | (required)   | S3 bucket name                                          |
| `--region`     | `S3PROXY_REGION`     | `us-east-1`  | AWS region                                              |
| `--port`       | `S3PROXY_PORT`       | `8080`       | HTTP server port                                        |
| `--loglevel`   | `S3PROXY_LOGLEVEL`   | `info`       | Log level (debug, info, warn, error)                    |
| `--endpoint`   | `S3PROXY_ENDPOINT`   | AWS endpoint | S3 endpoint URL, for S3-compatible servers              |
| `--path-style` | `S3PROXY_PATH_STYLE` | `false`      | Put the bucket in the URL path instead of the host name |

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

# Request the first two bytes of a video, as Safari does before playing it
curl -r 0-1 http://localhost:8080/stories/story.mp4

# Request only the headers
curl -I http://localhost:8080/stories/story.mp4
```

## Error Handling

The server returns original HTTP status codes with generic error messages:

- **404 Not Found**: "The requested resource was not found"
- **403 Forbidden**: "Access to the requested resource is forbidden"
- **405 Method Not Allowed**: "Method not allowed", for anything but GET and HEAD
- **412 Precondition Failed**: "The precondition given in the request failed"
- **416 Range Not Satisfiable**: "The requested range is not satisfiable", with `Content-Range: bytes */<size>`
- **500 Internal Server Error**: "An error occurred while processing your request"

A conditional request whose object has not changed gets `304 Not Modified` with no body.

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
go run . --bucket my-bucket --region us-west-2

# Build
make build

# Run tests
make test

# Run the tests against S3Mock too
docker run -d --rm --name s3mock -p 9090:9090 \
  -e COM_ADOBE_TESTING_S3MOCK_STORE_INITIAL_BUCKETS=test-bucket adobe/s3mock:5.2.3
S3PROXY_TEST_ENDPOINT=http://localhost:9090 make test
```

## License

MIT
