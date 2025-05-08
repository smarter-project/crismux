package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"flag"
	"io/ioutil"
	"net"
	"sync"
	"time"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	//	"google.golang.org/grpc/credentials"
	"gopkg.in/yaml.v2"
	"github.com/gogo/protobuf/jsonpb"	
	cri "k8s.io/cri-api/pkg/apis/runtime/v1"
	"strings"
	"os"
)


func (p *CRIProxy) isImagePresent(ctx context.Context, image *cri.ImageSpec, runtimeClass string) bool {
     logrus.Debug("checkStatusRequest called")
     client := p.getImageClient(runtimeClass)
     if client == nil {
       return false
     }
     req := &cri.ImageStatusRequest{Image: image}
     
     resp, err := client.ImageStatus(ctx, req)
     if err!= nil {
       panic(err)
     }
     return resp.Image != nil

}


func printPullImageRequest(req *cri.PullImageRequest) {
	var buf bytes.Buffer
	m := &jsonpb.Marshaler{Indent: "  "}
	if err := m.Marshal(&buf, req); err != nil {
		logrus.Fatalf("Failed to marshal: %v", err)
	}
	logrus.Info(buf.String())
}



func printContainerConfig(config *cri.ContainerConfig) {
	var buf bytes.Buffer
	m := &jsonpb.Marshaler{Indent: "  "}
	if err := m.Marshal(&buf, config); err != nil {
		logrus.Fatalf("Failed to marshal: %v", err)
	}
	logrus.Debug(buf.String())
}


func printPodSandboxConfig(config *cri.PodSandboxConfig) {
	var buf bytes.Buffer
	m := &jsonpb.Marshaler{Indent: "  "}
	if err := m.Marshal(&buf, config); err != nil {
		logrus.Fatalf("Failed to marshal PodSandboxConfig: %v", err)
	}
	logrus.Debug(buf.String())
}

const (
	// FakeVersion is a version of a fake runtime.
	FakeVersion = "0.1.0"
	// FakeRuntimeName is the name of the fake runtime.
	FakeRuntimeName = "fakeRuntime"
)

// Config struct for runtime mappings and TLS settings
type Config struct {
	Runtimes map[string]string `yaml:"runtimes"`
	TLS      struct {
		Cert string `yaml:"cert"`
		Key  string `yaml:"key"`
		CA   string `yaml:"ca"`
	} `yaml:"tls"`
	LogLevel string `yaml:"loglevel"`
}

type PodSandboxInfo struct {
	RuntimeClass string 
	Id string
}




// CRIProxy implements both RuntimeService and ImageService
type CRIProxy struct {
	cri.UnimplementedRuntimeServiceServer
	cri.UnimplementedImageServiceServer
	config    Config
	connMutex sync.Mutex
	connPool  map[string]*grpc.ClientConn
	sandboxes map[string]*PodSandboxInfo
	sandboxconfigs map[string]string
	containers map[string]string	
}


func (p* CRIProxy) print() {
	logrus.Debugf("***** Internal DB START *****")
	logrus.Debugf("PodSandBoxes:")
	for k, sb := range p.sandboxes {
		logrus.Debugf("ID: %s %s", k, sb.RuntimeClass)
	}

	logrus.Debugf("Containers:")	
	for k, rt := range p.containers {
		logrus.Debugf("ID: %s %s", k, rt)
	}
		
	logrus.Debugf("***** Internal DB END *****")	
}


// Load configuration from YAML
func loadConfig(path string) (Config, error) {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	err = yaml.Unmarshal(data, &cfg)
	return cfg, err
}

// Get gRPC connection, reusing existing ones if available
func (p *CRIProxy) getGRPCConn(runtimeClass string) (*grpc.ClientConn, error) {
	p.connMutex.Lock()
	defer p.connMutex.Unlock()
//	logrus.Debugf("getGPRCConn called for runtime: %s", runtimeClass)
	// Use cached connection if available
	if conn, exists := p.connPool[runtimeClass]; exists {
		//	logrus.Debug("Using cached connection")	
		return conn, nil
	}

	// Get endpoint for runtime class
	endpoint, exists := p.config.Runtimes[runtimeClass]
	if !exists {
		endpoint = p.config.Runtimes["default"]
	}

	if endpoint == "" {
		return nil, fmt.Errorf("no endpoint found for runtime class %s", runtimeClass)
	}

	var opts []grpc.DialOption

//	logrus.Debugf("Endpoint: %s", endpoint)
	switch {
	case endpoint[:5] == "unix:":
		opts = append(opts, grpc.WithInsecure(), grpc.WithDialer(func(addr string, timeout time.Duration) (net.Conn, error) {
			return net.DialTimeout("unix", addr[5:], timeout)
		}))
	case endpoint[:6] == "vsock:":
		vsockAddr := fmt.Sprintf("vsock://%s", endpoint[6:])
		opts = append(opts, grpc.WithInsecure(), grpc.WithDialer(func(addr string, timeout time.Duration) (net.Conn, error) {
			return net.DialTimeout("vsock", vsockAddr, timeout)
		}))
	case endpoint[:4] == "tcp:":
		tcpAddr := fmt.Sprintf("%s", endpoint[4:])
		logrus.Debugf("tcp address: %s", tcpAddr)		
		opts = append(opts, grpc.WithInsecure(), grpc.WithDialer(func(addr string, timeout time.Duration) (net.Conn, error) {
			return net.DialTimeout("tcp", tcpAddr, timeout)
		}))
	default:
		return nil, fmt.Errorf("unsupported endpoint type for runtime class %s: %s", runtimeClass, endpoint)
	}

	// Create and cache connection
	conn, err := grpc.Dial(endpoint, opts...)
	if err != nil {
		return nil, err
	}
//	logrus.Debugf("Target: %s      state: %s", conn.Target(), conn.GetState())
	p.connPool[runtimeClass] = conn

	return conn, nil
}

// Load TLS credentials
func loadTLSConfig(tlsConfig struct{ Cert, Key, CA string }) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(tlsConfig.Cert, tlsConfig.Key)
	if err != nil {
		return nil, err
	}
	caCert, err := ioutil.ReadFile(tlsConfig.CA)
	if err != nil {
		return nil, err
	}
	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM(caCert)

	return &tls.Config{Certificates: []tls.Certificate{cert}, RootCAs: caPool}, nil
}


func (p *CRIProxy) SetPodSandboxMapping(sandboxId string, runtimeClass string, uid string) {
	c := new(PodSandboxInfo)
	c.Id = sandboxId
	c.RuntimeClass = runtimeClass
	p.sandboxes[sandboxId] = c
	p.sandboxconfigs[uid] = runtimeClass
	logrus.Debugf("SetPodSandboxMapping: ID:%s  Runtimeclass: %s", sandboxId, runtimeClass)
}


func (p *CRIProxy) GetPodSandboxMapping(sandboxId string) ( string) {
	sc, exists := p.sandboxes[sandboxId]
	if exists {
		return sc.RuntimeClass
	} else {
		return "default"
	}
}


func (p *CRIProxy) RemovePodSandboxMapping(sandboxId string) {
	delete(p.sandboxes, sandboxId)
}


func (p *CRIProxy) SetContainerMapping(containerId string, runtimeClass string) {
	p.containers[containerId] = runtimeClass
	logrus.Debugf("SetContainerMapping: ID:%s  Runtimeclass: %s", containerId, runtimeClass)
}


func (p *CRIProxy) RemoveContainerMapping(containerId string) {
	delete(p.containers, containerId)
}


func (p *CRIProxy) GetContainerMapping(containerId string) ( string) {
	rc, exists := p.containers[containerId]
	if exists {
		return rc
	} else {
		return "default"
	}
}


func (p *CRIProxy) GetSandboxConfigMapping(config *cri.PodSandboxConfig) (string) {
	rc, exists := p.sandboxconfigs[config.Metadata.Uid]
	if exists {
		return rc
	} else {
		return "default"
	}
}

func (p *CRIProxy) RemoveSandboxConfigMapping(config *cri.PodSandboxConfig) {
	delete(p.sandboxconfigs, config.Metadata.Uid)
}



func (p *CRIProxy) getRuntimeClient(runtimeClass string) (cri.RuntimeServiceClient) {
	if runtimeClass == "" {
		runtimeClass = "default"
	}
	conn, err := p.getGRPCConn(runtimeClass) 
	if err != nil {
		return nil
	}
	client := cri.NewRuntimeServiceClient(conn)

	if isClientReady(conn) {
		return client
	} else {
		logrus.Debugf("Runtime Client for runtime %s not ready", runtimeClass)				
		return nil
	}
}



func (p *CRIProxy) getImageClient(runtimeClass string) (cri.ImageServiceClient) {
	if runtimeClass == "" {
		runtimeClass = "default"
	}
	conn, err := p.getGRPCConn(runtimeClass) 
	if err != nil {
		return nil
	}
	client := cri.NewImageServiceClient(conn)

	if isClientReady(conn) {
		return client
	} else {
		logrus.Debugf("Image Client for runtime %s not ready", runtimeClass)						
		return nil
	}
}


// Proxy function for RuntimeService
func (p *CRIProxy) proxyRuntime(ctx context.Context, req interface{}, runtimeClass string, config *cri.PodSandboxConfig) (interface{}, error) {
	//	logrus.Debug("proxyRuntime called")
	switch request := req.(type) {
        case *cri.RunPodSandboxRequest:
		logrus.Infof("RunPodSandboxRequest called: %s", runtimeClass)
		client := p.getRuntimeClient(runtimeClass)
		if client == nil {
			return &cri.RunPodSandboxResponse{}, nil
		}
		res, err := client.RunPodSandbox(ctx, request)
		p.SetPodSandboxMapping(res.GetPodSandboxId(), runtimeClass, config.Metadata.Uid)
		//		printPodSandboxConfig(config)
		return res, err
          
        case *cri.StopPodSandboxRequest:
		runtimeClass = p.GetPodSandboxMapping(request.GetPodSandboxId())
		logrus.Infof("StopPodSandboxRequest called: SandboxID: %s   runtimeClass: %s", request.GetPodSandboxId(), runtimeClass)		
		client := p.getRuntimeClient(runtimeClass)
		if client == nil {
			return &cri.StopPodSandboxResponse{}, fmt.Errorf("Cannot connect to runtime: %s", runtimeClass)
		}
		return client.StopPodSandbox(ctx, request)
          
        case *cri.RemovePodSandboxRequest:
		runtimeClass = p.GetPodSandboxMapping(request.GetPodSandboxId())		
		client := p.getRuntimeClient(runtimeClass)
		if client == nil {
			return &cri.RemovePodSandboxResponse{}, fmt.Errorf("Cannot connect to runtime: %s", runtimeClass)
		}
		logrus.Infof("RemovePodSandboxRequest called: SandboxID: %s   runtimeClass: %s", request.GetPodSandboxId(), runtimeClass)				
		res, err := client.RemovePodSandbox(ctx, request)
		if err == nil {
			p.RemovePodSandboxMapping(request.GetPodSandboxId())
		}
		return res, err
          
        case *cri.PodSandboxStatusRequest:
		runtimeClass = p.GetPodSandboxMapping(request.GetPodSandboxId())
		client := p.getRuntimeClient(runtimeClass)
		if client == nil {
			return &cri.PodSandboxStatusResponse{}, fmt.Errorf("Cannot connect to runtime: %s", runtimeClass)
		}
		//		logrus.Debugf("PodSandboxStatusRequest called: SandboxID: %s   runtimeClass: %s", request.GetPodSandboxId(), runtimeClass)
		return client.PodSandboxStatus(ctx, request)
          
        case *cri.ListPodSandboxRequest:
		//		logrus.Debug("ListPodSandboxRequest called")
  		var PodSandboxResponses []*cri.ListPodSandboxResponse
		// Collect all the PodSandbox responses
         	for runtimeClass, _ := range p.config.Runtimes {
			//logrus.Debugf("Getting PodSandboxes for runtime: %s", runtimeClass)
			client := p.getRuntimeClient(runtimeClass)
			if client != nil {
				res, err := client.ListPodSandbox(ctx, request)
				if err != nil {
					return nil, err
				}    
				PodSandboxResponses = append(PodSandboxResponses, res)
			}
                }
		// Merge the PodSandbox responses into a single response
		merged := &cri.ListPodSandboxResponse{}
		for _, resp := range PodSandboxResponses {
  		  if resp != nil {
		    merged.Items = append(merged.Items, resp.Items...)
		  }
		}
         	return merged, nil
          
        case *cri.CreateContainerRequest:
		logrus.Debug("CreateContainerRequest called")
		runtimeClass = p.GetPodSandboxMapping(request.GetPodSandboxId())
		client := p.getRuntimeClient(runtimeClass)
		if client == nil {
			return &cri.CreateContainerResponse{}, fmt.Errorf("Cannot connect to runtime: %s", runtimeClass)
		}

    		if runtimeClass == ""  || runtimeClass == "default" {
	 	} else {
      		  // Lets see what the image is
		  logrus.Debugf("Checking the status of image: %s", request.GetConfig().GetImage())			  
  		  image := request.GetConfig().GetImage();
		  // Check if image is not present 
		  if !p.isImagePresent(ctx, image, runtimeClass) {
		     logrus.Debugf("image is NOT PRESENT")				
		     iclient := p.getImageClient(runtimeClass)
		     userimage := &cri.ImageSpec{Image: image.GetUserSpecifiedImage(),}
		      _, err := iclient.PullImage(ctx, &cri.PullImageRequest{Image: userimage,})
		      if err != nil {
		          logrus.Infof("failed to pull image: %w", err)				      
			  return nil, err
		      }		  
		  }
                }
		res, err := client.CreateContainer(ctx, request)
		p.SetContainerMapping(res.GetContainerId(), runtimeClass)
		//		printContainerConfig(request.GetConfig())
		return res, err
          
        case *cri.StartContainerRequest:
		logrus.Debugf("StartContainerRequest called for container id: %s", request.GetContainerId())
		runtimeClass = p.GetContainerMapping(request.GetContainerId())
		client := p.getRuntimeClient(runtimeClass)
		if client == nil {
			return &cri.StartContainerResponse{}, fmt.Errorf("Cannot connect to runtime: %s", runtimeClass)
		}
		return client.StartContainer(ctx, request)
          
        case *cri.StopContainerRequest:
		logrus.Debugf("StopContainerRequest called for container id: %s", request.GetContainerId())
		runtimeClass = p.GetContainerMapping(request.GetContainerId())
		client := p.getRuntimeClient(runtimeClass)
		if client == nil {
			return &cri.StopContainerResponse{}, fmt.Errorf("Cannot connect to runtime: %s", runtimeClass)
		}
		return client.StopContainer(ctx, request)
          
        case *cri.RemoveContainerRequest:
		logrus.Debugf("RemoveContainerRequest called container id: %s", request.GetContainerId())
		runtimeClass = p.GetContainerMapping(request.GetContainerId())
		client := p.getRuntimeClient(runtimeClass)
		if client == nil {
			return  &cri.RemoveContainerResponse{}, fmt.Errorf("Cannot connect to runtime: %s", runtimeClass)
		}
		res, err := client.RemoveContainer(ctx, request)
		if err == nil {
			p.RemoveContainerMapping(request.GetContainerId())
		}
		return res, err
          
        case *cri.ListContainersRequest:
		//		logrus.Debug("ListContainersRequest called")
		var containerResponses []*cri.ListContainersResponse
		// Collect all the container responses
         	for runtimeClass, _ := range p.config.Runtimes {
			//			logrus.Debugf("Getting containers for runtime: %s", runtimeClass)
			client := p.getRuntimeClient(runtimeClass)
			if client != nil {
				res, err := client.ListContainers(ctx, request)
				if err != nil {
					return nil, err
				}    
				containerResponses = append(containerResponses, res)
			}
                }
		// Merge the container responses into a single response
		merged := &cri.ListContainersResponse{}
		for _, resp := range containerResponses {
			if resp != nil {
				merged.Containers = append(merged.Containers, resp.GetContainers()...)
			}
		}
         	return merged, nil
		
        case *cri.ContainerStatusRequest:
		logrus.Debugf("ContainerStatusRequest called for container id: %s", request.GetContainerId())
		runtimeClass = p.GetContainerMapping(request.GetContainerId())
		client := p.getRuntimeClient(runtimeClass)
		if client == nil {
			return &cri.ContainerStatusResponse{}, fmt.Errorf("Cannot connect to runtime: %s", runtimeClass)
		}
		return client.ContainerStatus(ctx, request)
          
        case *cri.UpdateContainerResourcesRequest:
		logrus.Debugf("UpdateContainerResourcesRequest called for container id: %s", request.GetContainerId())
		runtimeClass = p.GetContainerMapping(request.GetContainerId())
		client := p.getRuntimeClient(runtimeClass)
		if client == nil {
			return &cri.UpdateContainerResourcesResponse{}, fmt.Errorf("Cannot connect to runtime: %s", runtimeClass)
		}	
		return client.UpdateContainerResources(ctx, request)

        case *cri.ReopenContainerLogRequest:
		logrus.Debug("ReopenContainerLogRequest called")
		runtimeClass = p.GetContainerMapping(request.GetContainerId())
		client := p.getRuntimeClient(runtimeClass)
		if client == nil {
			return &cri.ReopenContainerLogResponse{}, fmt.Errorf("Cannot connect to runtime: %s", runtimeClass)
		}
		return client.ReopenContainerLog(ctx, request)
          
        case *cri.ExecSyncRequest:
		logrus.Debug("ExecSyncRequest called")
		runtimeClass = p.GetContainerMapping(request.GetContainerId())
		client := p.getRuntimeClient(runtimeClass)
		if client == nil {
			return &cri.ExecSyncResponse{}, fmt.Errorf("Cannot connect to runtime: %s", runtimeClass)
		}
		return client.ExecSync(ctx, request)
          
        case *cri.ExecRequest:
		logrus.Debug("ExecRequest called")
		runtimeClass = p.GetContainerMapping(request.GetContainerId())
		client := p.getRuntimeClient(runtimeClass)
		if client == nil {
			return &cri.ExecResponse{}, fmt.Errorf("Cannot connect to runtime: %s", runtimeClass)
		}
		return client.Exec(ctx, request)
          	
        case *cri.AttachRequest:
		logrus.Debug("AttachRequest called")
		runtimeClass = p.GetContainerMapping(request.GetContainerId())
		client := p.getRuntimeClient(runtimeClass)
		if client == nil {
			return &cri.AttachResponse{}, fmt.Errorf("Cannot connect to runtime: %s", runtimeClass)
		}
		return client.Attach(ctx, request)
          
        case *cri.PortForwardRequest:
		logrus.Debug("PortForwardRequest called")
		runtimeClass = p.GetPodSandboxMapping(request.GetPodSandboxId())
		client := p.getRuntimeClient(runtimeClass)
		if client == nil {
			return &cri.PortForwardResponse{}, fmt.Errorf("Cannot connect to runtime: %s", runtimeClass)
		}
		return client.PortForward(ctx, request)
          
        case *cri.ContainerStatsRequest:
		logrus.Debugf("ContainerStatsRequest called container id: %s", request.GetContainerId())
		runtimeClass = p.GetContainerMapping(request.GetContainerId())
		client := p.getRuntimeClient(runtimeClass)
		if client == nil {
			return &cri.ContainerStatsResponse{}, fmt.Errorf("Cannot connect to runtime: %s", runtimeClass)
		}
		return client.ContainerStats(ctx, request)
          
        case *cri.ListContainerStatsRequest:
		logrus.Debug("ListContainerStatsRequest called")
		var listContainerStatsResponses []*cri.ListContainerStatsResponse
		// Collect all the container responses
         	for runtimeClass, _ := range p.config.Runtimes {
			logrus.Debugf("Getting container stats for runtime: %s", runtimeClass)
			client := p.getRuntimeClient(runtimeClass)
			if client != nil {
				res, err := client.ListContainerStats(ctx, request)
				if err != nil {
					return nil, err
				}    
				listContainerStatsResponses = append(listContainerStatsResponses, res)
			}
                }
		// Merge the listcontainerstats responses into a single response
		merged := &cri.ListContainerStatsResponse{}
		for _, resp := range listContainerStatsResponses {
			if resp != nil {
				merged.Stats = append(merged.Stats, resp.GetStats()...)
			}
		}
         	return merged, nil

        case *cri.PodSandboxStatsRequest:
		logrus.Debug("PodSandboxStatsRequest called")
		runtimeClass = p.GetPodSandboxMapping(request.GetPodSandboxId())
		client := p.getRuntimeClient(runtimeClass)
		if client == nil {
			return &cri.PodSandboxStatsResponse{}, fmt.Errorf("Cannot connect to runtime: %s", runtimeClass)
		}		
		return client.PodSandboxStats(ctx, request)
          
        case *cri.ListPodSandboxStatsRequest:
		logrus.Debug("ListPodSandboxStatsRequest called")
		var listPodSandboxStatsResponses []*cri.ListPodSandboxStatsResponse
		// Collect all the container responses
         	for runtimeClass, _ := range p.config.Runtimes {
			logrus.Debugf("Getting container stats for runtime: %s", runtimeClass)
			client := p.getRuntimeClient(runtimeClass)
			if  client != nil {
				res, err := client.ListPodSandboxStats(ctx, request)
				if err != nil {
					return nil, err
				}    
				listPodSandboxStatsResponses = append(listPodSandboxStatsResponses, res)
			}
                }
		// Merge the listcontainerstats responses into a single response
		merged := &cri.ListPodSandboxStatsResponse{}
		for _, resp := range listPodSandboxStatsResponses {
			if resp != nil {
				merged.Stats = append(merged.Stats, resp.GetStats()...)
			}
		}
         	return merged, nil
		//          return client.ListPodSandboxStats(ctx, request)
          
        case *cri.UpdateRuntimeConfigRequest:
		client := p.getRuntimeClient(runtimeClass)
		if client == nil {
			return &cri.UpdateRuntimeConfigResponse{}, nil
		}
		logrus.Debug("UpdateRuntimeConfigRequest called")
          return client.UpdateRuntimeConfig(ctx, request)
          
        case *cri.StatusRequest:
		logrus.Debug("StatusRequest called")
		client := p.getRuntimeClient(runtimeClass)
		if client == nil {
			return &cri.StatusResponse{}, nil
		}
		return client.Status(ctx, request)
          
        case *cri.CheckpointContainerRequest:
		logrus.Debug("CheckpointContainerRequest called")
		client := p.getRuntimeClient(runtimeClass)
		if client == nil {
			return &cri.CheckpointContainerResponse{}, nil
		}
		
          return client.CheckpointContainer(ctx, request)
          
        case *cri.GetEventsRequest:
		logrus.Debug("GetEventsRequest called")
		client := p.getRuntimeClient(runtimeClass)
		if client == nil {
			return nil, nil
		}		
		return client.GetContainerEvents(ctx, request)
		
        case *cri.ListMetricDescriptorsRequest:
		logrus.Debug("ListMetricDescriptorsRequest called")
		client := p.getRuntimeClient(runtimeClass)
		if client == nil {
			return &cri.ListMetricDescriptorsResponse{}, nil
		}
		return client.ListMetricDescriptors(ctx, request)
          
        case *cri.ListPodSandboxMetricsRequest:
		logrus.Debug("ListPodSandboxMetricsRequest called")
				client := p.getRuntimeClient(runtimeClass)
		if client == nil {
			return &cri.ListPodSandboxMetricsResponse{}, nil
		}
		return client.ListPodSandboxMetrics(ctx, request)
          
        case *cri.RuntimeConfigRequest:
		logrus.Debug("RuntimeConfigRequest called")
				client := p.getRuntimeClient(runtimeClass)
		if client == nil {
			return &cri.RuntimeConfigResponse{}, nil
		}
		return client.RuntimeConfig(ctx, request)
          
	default:
		return nil, fmt.Errorf("unsupported runtime request")
	}
}



// Proxy function for ImageService
func (p *CRIProxy) proxyImage(ctx context.Context, req interface{}, runtimeClass string, config *cri.PodSandboxConfig) (interface{}, error) {
//	logrus.Debug("proxyImage called")
	switch request := req.(type) {
	case *cri.PullImageRequest:
	        logrus.Debug("PullImageRequest called")
		runtimeClass = p.GetSandboxConfigMapping(config)
		client := p.getImageClient(runtimeClass)
		if client == nil {
			return &cri.PullImageResponse{}, nil
		}
		return client.PullImage(ctx, request)
	case *cri.ListImagesRequest:
	        logrus.Debug("ListImagesRequest called")
		var imageResponses []*cri.ListImagesResponse
		// Collect all the image responses
		runtimeClass = "default"
		logrus.Debugf("Getting images for runtime: %s", runtimeClass)
		client := p.getImageClient(runtimeClass)			
		if client != nil {
		  res, err := client.ListImages(ctx, request)
		  if err != nil {
			return nil, err
		  }    
		  return res, nil
		} else {
		  return imageResponses, nil
		}
	case *cri.ImageStatusRequest:
	        logrus.Debug("ImageStatusRequest called")
		client := p.getImageClient(runtimeClass)
		if client == nil {
			return &cri.ImageStatusResponse{}, nil
		}
		return client.ImageStatus(ctx, request)
	case *cri.RemoveImageRequest:
	        logrus.Debug("RemoveImageRequest called")
		client := p.getImageClient(runtimeClass)
		if client == nil {
			return &cri.RemoveImageResponse{}, nil
		}
		return client.RemoveImage(ctx, request)
         case *cri.ImageFsInfoRequest:
		logrus.Debug("ImageFsInfoRequest called")
		client := p.getImageClient(runtimeClass)
		if client == nil {
			return 	&cri.ImageFsInfoResponse{}, nil
		}
		return client.ImageFsInfo(ctx, request)
	default:
		return nil, fmt.Errorf("unsupported image request")
	}
}

// Implement RuntimeService
func (p *CRIProxy) RunPodSandbox(ctx context.Context, req *cri.RunPodSandboxRequest) (*cri.RunPodSandboxResponse, error) {
	res, err := p.proxyRuntime(ctx, req, req.RuntimeHandler, req.Config)
	return res.(*cri.RunPodSandboxResponse), err
}

func (p *CRIProxy) CreateContainer(ctx context.Context, req *cri.CreateContainerRequest) (*cri.CreateContainerResponse, error) {
	res, err := p.proxyRuntime(ctx, req, "", nil)
	return res.(*cri.CreateContainerResponse), err
}

func (p *CRIProxy) ListContainers(ctx context.Context, req *cri.ListContainersRequest) (*cri.ListContainersResponse, error) {
	res, err := p.proxyRuntime(ctx, req, "", nil)
	return res.(*cri.ListContainersResponse), err
}

func (p *CRIProxy) StopPodSandbox(ctx context.Context, req *cri.StopPodSandboxRequest) (*cri.StopPodSandboxResponse, error) {
  	res, err := p.proxyRuntime(ctx, req, "", nil)
  return res.(*cri.StopPodSandboxResponse), err
}
  
func (p *CRIProxy) RemovePodSandbox(ctx context.Context, req *cri.RemovePodSandboxRequest) (*cri.RemovePodSandboxResponse, error) {
  	res, err := p.proxyRuntime(ctx, req, "", nil)
  return res.(*cri.RemovePodSandboxResponse), err
  }
  
func (p *CRIProxy) PodSandboxStatus(ctx context.Context, req *cri.PodSandboxStatusRequest) (*cri.PodSandboxStatusResponse, error) {
  	res, err := p.proxyRuntime(ctx, req, "", nil)
  return res.(*cri.PodSandboxStatusResponse), err
  }
  
func (p *CRIProxy) ListPodSandbox(ctx context.Context, req *cri.ListPodSandboxRequest) (*cri.ListPodSandboxResponse, error) {
  	res, err := p.proxyRuntime(ctx, req, "", nil)
	return res.(*cri.ListPodSandboxResponse), err
}
  
func (p *CRIProxy) StartContainer(ctx context.Context, req *cri.StartContainerRequest) (*cri.StartContainerResponse, error) {
  	res, err := p.proxyRuntime(ctx, req, "", nil)
  return res.(*cri.StartContainerResponse), err
  }
  
func (p *CRIProxy) StopContainer(ctx context.Context, req *cri.StopContainerRequest) (*cri.StopContainerResponse, error) {
  	res, err := p.proxyRuntime(ctx, req, "", nil)
  return res.(*cri.StopContainerResponse), err
  }
  
func (p *CRIProxy) RemoveContainer(ctx context.Context, req *cri.RemoveContainerRequest) (*cri.RemoveContainerResponse, error) {
  	res, err := p.proxyRuntime(ctx, req, "", nil)
  return res.(*cri.RemoveContainerResponse), err
  }
  
  
func (p *CRIProxy) ContainerStatus(ctx context.Context, req *cri.ContainerStatusRequest) (*cri.ContainerStatusResponse, error) {
  	res, err := p.proxyRuntime(ctx, req, "", nil)
  return res.(*cri.ContainerStatusResponse), err
  }
  
func (p *CRIProxy) UpdateContainerResources(ctx context.Context, req *cri.UpdateContainerResourcesRequest) (*cri.UpdateContainerResourcesResponse, error) {
  	res, err := p.proxyRuntime(ctx, req, "", nil)
  return res.(*cri.UpdateContainerResourcesResponse), err
  }
  
func (p *CRIProxy) ReopenContainerLog(ctx context.Context, req *cri.ReopenContainerLogRequest) (*cri.ReopenContainerLogResponse, error) {
  	res, err := p.proxyRuntime(ctx, req, "", nil)
  return res.(*cri.ReopenContainerLogResponse), err
  }
  
func (p *CRIProxy) ExecSync(ctx context.Context, req *cri.ExecSyncRequest) (*cri.ExecSyncResponse, error) {
  	res, err := p.proxyRuntime(ctx, req, "", nil)
  return res.(*cri.ExecSyncResponse), err
  }
  
func (p *CRIProxy) Exec(ctx context.Context, req *cri.ExecRequest) (*cri.ExecResponse, error) {
  	res, err := p.proxyRuntime(ctx, req, "", nil)
  return res.(*cri.ExecResponse), err
  }
  
func (p *CRIProxy) Attach(ctx context.Context, req *cri.AttachRequest) (*cri.AttachResponse, error) {
  	res, err := p.proxyRuntime(ctx, req, "", nil)
  return res.(*cri.AttachResponse), err
  }
  
func (p *CRIProxy) PortForward(ctx context.Context, req *cri.PortForwardRequest) (*cri.PortForwardResponse, error) {
  	res, err := p.proxyRuntime(ctx, req, "", nil)
  return res.(*cri.PortForwardResponse), err
  }
  
func (p *CRIProxy) ContainerStats(ctx context.Context, req *cri.ContainerStatsRequest) (*cri.ContainerStatsResponse, error) {
  	res, err := p.proxyRuntime(ctx, req, "", nil)
  return res.(*cri.ContainerStatsResponse), err
  }
  
func (p *CRIProxy) ListContainerStats(ctx context.Context, req *cri.ListContainerStatsRequest) (*cri.ListContainerStatsResponse, error) {
  	res, err := p.proxyRuntime(ctx, req, "", nil)
  return res.(*cri.ListContainerStatsResponse), err
  }
  
func (p *CRIProxy) PodSandboxStats(ctx context.Context, req *cri.PodSandboxStatsRequest) (*cri.PodSandboxStatsResponse, error) {
  	res, err := p.proxyRuntime(ctx, req, "", nil)
  return res.(*cri.PodSandboxStatsResponse), err
  }
  
func (p *CRIProxy) ListPodSandboxStats(ctx context.Context, req *cri.ListPodSandboxStatsRequest) (*cri.ListPodSandboxStatsResponse, error) {
  	res, err := p.proxyRuntime(ctx, req, "", nil)
  return res.(*cri.ListPodSandboxStatsResponse), err
  }
  
func (p *CRIProxy) UpdateRuntimeConfig(ctx context.Context, req *cri.UpdateRuntimeConfigRequest) (*cri.UpdateRuntimeConfigResponse, error) {
  	res, err := p.proxyRuntime(ctx, req, "", nil)
  return res.(*cri.UpdateRuntimeConfigResponse), err
  }
  
func (p *CRIProxy) Status(ctx context.Context, req *cri.StatusRequest) (*cri.StatusResponse, error) {
  	res, err := p.proxyRuntime(ctx, req, "", nil)
  return res.(*cri.StatusResponse), err
  }
  
func (p *CRIProxy) CheckpointContainer(ctx context.Context, req *cri.CheckpointContainerRequest) (*cri.CheckpointContainerResponse, error) {
  	res, err := p.proxyRuntime(ctx, req, "", nil)
  return res.(*cri.CheckpointContainerResponse), err
  }


// This maybe requires a streaming solution
func (p *CRIProxy) GetContainerEvents(req *cri.GetEventsRequest, srv cri.RuntimeService_GetContainerEventsServer) (error) {
 _, err := p.proxyRuntime( nil, req, "", nil)
 return err
}
  
func (p *CRIProxy) ListMetricDescriptors(ctx context.Context, req *cri.ListMetricDescriptorsRequest) (*cri.ListMetricDescriptorsResponse, error) {
  	res, err := p.proxyRuntime(ctx, req, "", nil)
  return res.(*cri.ListMetricDescriptorsResponse), err
  }
  
func (p *CRIProxy) ListPodSandboxMetrics(ctx context.Context, req *cri.ListPodSandboxMetricsRequest) (*cri.ListPodSandboxMetricsResponse, error) {
  	res, err := p.proxyRuntime(ctx, req, "", nil)
  return res.(*cri.ListPodSandboxMetricsResponse), err
  }
  
func (p *CRIProxy) RuntimeConfig(ctx context.Context, req *cri.RuntimeConfigRequest) (*cri.RuntimeConfigResponse, error) {
  	res, err := p.proxyRuntime(ctx, req, "", nil)
  return res.(*cri.RuntimeConfigResponse), err
}



// Implement ImageService
func (p *CRIProxy) PullImage(ctx context.Context, req *cri.PullImageRequest) (*cri.PullImageResponse, error) {
        logrus.Debug("PullImage called")
//        printPullImageRequest(req)		
	res, err := p.proxyImage(ctx, req, req.Image.Image, req.SandboxConfig)
	return res.(*cri.PullImageResponse), err
}

func (p *CRIProxy) RemoveImage(ctx context.Context, req *cri.RemoveImageRequest) (*cri.RemoveImageResponse, error) {
        logrus.Debug("RemoveImage called")	
	res, err := p.proxyImage(ctx, req, req.Image.Image, nil)
	return res.(*cri.RemoveImageResponse), err
}

func (p *CRIProxy) ImageStatus(ctx context.Context, req *cri.ImageStatusRequest) (*cri.ImageStatusResponse, error) {
        logrus.Debug("ImageStatus called")	
	res, err := p.proxyImage(ctx, req, req.Image.Image, nil)
	return res.(*cri.ImageStatusResponse), err
}


func (p *CRIProxy) ImageFsInfo(ctx context.Context, req *cri.ImageFsInfoRequest) (*cri.ImageFsInfoResponse, error) {
        logrus.Debug("ImageFsInfo called")	
	res, err := p.proxyImage(ctx, req, "", nil)
	return res.(*cri.ImageFsInfoResponse), err
}


func (p *CRIProxy) ListImages(ctx context.Context, req *cri.ListImagesRequest) (*cri.ListImagesResponse, error) {
        logrus.Debug("ListImages called")	
	res, err := p.proxyImage(ctx, req, "", nil)
	return res.(*cri.ListImagesResponse), err
}


// Version returns the runtime version
func (p *CRIProxy) Version(ctx context.Context, req *cri.VersionRequest) (*cri.VersionResponse, error) {
	return &cri.VersionResponse{
		Version:           FakeVersion,
		RuntimeName:       FakeRuntimeName,
		RuntimeVersion:    FakeVersion,
		RuntimeApiVersion: FakeVersion,
	} , nil
}


// Close all gRPC connections on shutdown
func (p *CRIProxy) CloseConnections() {
	p.connMutex.Lock()
	defer p.connMutex.Unlock()
	for _, conn := range p.connPool {
		conn.Close()
	}
}


func isClientReady(conn *grpc.ClientConn) bool {
	conn.Connect()
	state := conn.GetState()

	// Optionally wait for it to be READY
	if state != connectivity.Ready {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()
		// Wait for the state to change to READY
		if conn.WaitForStateChange(ctx, state) {
			return conn.GetState() == connectivity.Ready 
		}
		return false
	}
	return true
}

func (p *CRIProxy) initialise() {
	req := &cri.ListPodSandboxRequest{}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	
	// Collect all the PodSandbox responses
	for runtimeClass, _ := range p.config.Runtimes {
		logrus.Infof("Getting PodSandboxes for runtime: %s", runtimeClass)
		client := p.getRuntimeClient(runtimeClass)
		if client != nil {
			res, err := client.ListPodSandbox(ctx, req)
			if err == nil {
				for _ , sb := range res.Items  {
					p.SetPodSandboxMapping(sb.GetId(), runtimeClass, sb.GetMetadata().GetUid())						
				}
			}
		}
	}
	
	// Collect all the container responses
	req2 := &cri.ListContainersRequest{}	
	for runtimeClass, _ := range p.config.Runtimes {
		logrus.Infof("Getting Containers for runtime: %s", runtimeClass)		
		client := p.getRuntimeClient(runtimeClass)
		if client != nil {
			res, err := client.ListContainers(ctx, req2)
			if err == nil {
				for _, c := range res.GetContainers() {
					p.SetContainerMapping(c.GetId(), runtimeClass)
				}
			}
		}
	}
	defer cancel() // Ensure context is cancelled
}


// Main function
func main() {

	logrus.SetLevel(logrus.InfoLevel) // Change to DebugLevel, WarnLevel, etc.
	
	// Define flags with default values
	configPath := flag.String("config", "config.yaml", "path to config file")
	logLevel := flag.String("log-level", "info", "Set the log level (debug, info, warn, error, fatal, panic)")
	socketPath := flag.String("socket", "/var/run/crismux.sock", "path to crismux socket") 

	// Parse command-line flags
	flag.Parse()


	// Load config
	config, err := loadConfig(*configPath)
	if err != nil {
		logrus.Fatalf("Failed to load config: %v", err)
	} else {
		logrus.Infof("Loaded config from: %s", *configPath)
	}


	// Set log level
	var log *string	
	if config.LogLevel != "" {
	  log = &config.LogLevel

	} else {
  	   log = logLevel
	}
	level, err := logrus.ParseLevel(strings.ToLower(*log))

	if err != nil {
		logrus.Debugf("Invalid log level: %s\n", *log)			
	}
        logrus.Infof("Setting log level: %s\n", level)				
	logrus.SetLevel(level)

	// Check socket path
	_, err = os.Stat(*socketPath)
	if err == nil || !os.IsNotExist(err) {
		// socket already exists
		// lets try deleting
		err := os.Remove(*socketPath)
		if err != nil {
			logrus.Fatalf("Failed to delete file: %v\n", err)
			return
		}
	}



	
	proxy := &CRIProxy{
		config:   config,
		connPool: make(map[string]*grpc.ClientConn),
		sandboxes: make(map[string]*PodSandboxInfo),
		containers: make(map[string]string),
		sandboxconfigs: make(map[string]string),				
	}


	// Initialise db	
	proxy.initialise()
	proxy.print()

	grpcServer := grpc.NewServer()
	cri.RegisterRuntimeServiceServer(grpcServer, proxy)
	cri.RegisterImageServiceServer(grpcServer, proxy)


	
		
	listener, err := net.Listen("unix", *socketPath)
	if err != nil {
		logrus.Fatalf("Failed to listen on socket: %v", err)
	}

	logrus.Infof("CRI Proxy started on %s", *socketPath)
	defer proxy.CloseConnections() // Ensure cleanup

	if err := grpcServer.Serve(listener); err != nil {
		logrus.Fatalf("Failed to start gRPC server: %v", err)
	}
}
