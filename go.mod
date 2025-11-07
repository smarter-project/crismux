module github.com/yourusername/cri-proxy

go 1.23.0

require (
	github.com/gogo/protobuf v1.3.2
	github.com/sirupsen/logrus v1.9.3
	google.golang.org/grpc v1.57.0
	gopkg.in/yaml.v2 v2.4.0
	k8s.io/cri-api v0.28.2
)

require (
	github.com/golang/protobuf v1.5.3 // indirect
	github.com/kr/text v0.2.0 // indirect
	golang.org/x/net v0.38.0 // indirect
	golang.org/x/sys v0.31.0 // indirect
	golang.org/x/text v0.23.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20230525234030-28d5490b6b19 // indirect
	google.golang.org/protobuf v1.30.0 // indirect
)

replace k8s.io/cri-api => k8s.io/cri-api v0.28.2
