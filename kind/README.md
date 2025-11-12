# Testing on KinD with locally ran application

It deploys all infra components in the KinD cluster with the main application
ran locally on host.

Start the application:

```sh
make build
set -a ; kind/kind.env ; set +a
./s3-proxy
```

Create the cluster:

```sh
./kind-create.sh
./install.sh
```

Deploy infra components and proxy to the host for locally ran application:

```sh
./kind-create.sh
./install.sh
```

Access it:

<http://s3-proxy.127.0.0.1.sslip.io:31080/>
