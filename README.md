# crismux

crismux is a gRPC proxy server for Kubernetes Container Runtime Interface (CRI) that supports multiple runtime classes. It allows you to route CRI requests to different runtime endpoints based on the runtime class specified in the request.

## Features

- Supports multiple runtime classes
- Reuses gRPC connections for efficiency
- Supports Unix, vsock, and TCP endpoints
- TLS support for secure communication

## Project Structure

```
.
├── go.mod
├── go.sum
├── main.go
└── main_test.go
```

## Getting Started

### Prerequisites

- Go 1.20 or later
- Kubernetes CRI API
- gRPC

### Installation

1. Clone the repository:

   ```sh
   git clone https://git.gitlab.arm.com/research/smarter/edgeai/crismux.git
   cd crismux
   ```

2. Build the project:

   ```sh
   go build -o crismux main.go
   ```

### Configuration

Create a `config.yaml` file with the following structure:

```yaml
runtimes:
  default: "unix:///run/containerd_a/containerd.sock"
  nelly: "unix:///run/containerd_b/containerd.sock"
tls:
  cert: "/path/to/cert.pem"
  key: "/path/to/key.pem"
  ca: "/path/to/ca.pem"
```

### Running the Proxy

Start the crismux server:

```sh
./crismux
```

The server will listen on `/var/run/crismux.sock` by default.

## Usage

The crismux server will route CRI requests to the appropriate runtime endpoint based on the runtime class specified in the request.

## Example

Two containerd configuration files are provided for testing on a single host

```sh
containerd -c config_a.toml > a.log 2>&1 &
containerd -c config_b.toml > b.log 2>&1 &

crismux > c.log 2>&1 
```

Start k3s configured to use /var/run/crismux.sock as the container-runtime-endpoint
This can usually be done by editing /etc/systemd/system/k3s.service


Use crictl to query each of the containerd instances or both via crismux:
```sh
X=--runtime-endpoint=unix:///run/crismux.sock
B=--runtime-endpoint=unix:///var/run/containerd_b/containerd.sock
A=--runtime-endpoint=unix:///var/run/containerd_a/containerd.sock

crictl $A pods
crictl $B pods
crictl $X pods
```

Add a new runtime class:

```sh
kubectl apply -f nelly.yaml
```

Deploy an example container:
```sh
kubectl apply -f example.yaml

kubectl get pods
NAME            READY   STATUS    RESTARTS   AGE
example-sxcfw   1/1     Running   0          21m
```

Deploy an example container using the nelly runtime
```sh
kubectl apply -f nex.yaml

kubectl get pods
NAME                  READY   STATUS    RESTARTS   AGE
example-sxcfw         1/1     Running   0          22m
nelly-example-m4lwx   1/1     Running   0          17s
```

We can use crictl to see where the pods are running:
```sh
crictl $A pods
POD ID              CREATED             STATE               NAME                NAMESPACE           ATTEMPT             RUNTIME
2906c33d045b6       22 minutes ago      Ready               example-sxcfw       default             0                   (default)

crictl $B pods
POD ID              CREATED             STATE               NAME                  NAMESPACE           ATTEMPT             RUNTIME
732dabbcfbedf       44 seconds ago      Ready               nelly-example-m4lwx   default             0                   nelly
```

We can query also crismux get a unified view:

```sh
crictl $X pods
POD ID              CREATED             STATE               NAME                  NAMESPACE           ATTEMPT             RUNTIME
732dabbcfbedf       2 minutes ago       Ready               nelly-example-m4lwx   default             0                   nelly
2906c33d045b6       24 minutes ago      Ready               example-sxcfw         default             0                   (default)
``

We can delete the pod:
```sh
kubectl delete -f nex.yaml
daemonset.apps "nelly-example" deleted

kubectl get pods
NAME            READY   STATUS    RESTARTS   AGE
example-sxcfw   1/1     Running   0          27m

crictl $B pods
POD ID              CREATED             STATE               NAME                NAMESPACE           ATTEMPT             RUNTIME
```

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
