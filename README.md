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

## Running Tests

Two containerd configuration file are provided for testing on a single host

```
containerd -c config_a.toml &
containerd -c config_b.toml &

Coming soon ....

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
