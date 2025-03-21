package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"sync"
	"time"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"gopkg.in/yaml.v2"
	cri "k8s.io/cri-api/pkg/apis/runtime/v1"
)

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
}

type PodSandboxInfo struct {
	RuntimeClass string 
	Id string
	//	Config *cri.PodSandboxConfig
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
	logrus.Infof("***** Internal DB START *****")
	logrus.Infof("PodSandBoxes:")
	for k, sb := range p.sandboxes {
		logrus.Infof("ID: %s %s", k, sb.RuntimeClass)
	}

	logrus.Infof("Containers:")	
	for k, rt := range p.containers {
		logrus.Infof("ID: %s %s", k, rt)
	}
		
	logrus.Infof("***** Internal DB END *****")	
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
//	logrus.Debug("getGPRCConn called")
	// Use cached connection if available
	if conn, exists := p.connPool[runtimeClass]; exists {
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
	case endpoint[:6] == "tcp://":
		myConfig := struct {
			Cert string
			Key  string
			CA   string
		}{
			Cert: p.config.TLS.Cert,
			Key:  p.config.TLS.Key,
			CA:   p.config.TLS.CA,
		}
		tlsConfig, err := loadTLSConfig(myConfig)
		if err != nil {
			return nil, err
		}
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
	default:
		return nil, fmt.Errorf("unsupported endpoint type for runtime class %s: %s", runtimeClass, endpoint)
	}

	// Create and cache connection
	conn, err := grpc.Dial(endpoint, opts...)
	if err != nil {
		return nil, err
	}
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
	logrus.Debugf("SetPodSandboxMapping: ID:%S  Runtimeclass: %s", sandboxId, runtimeClass)
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
	return client
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
	return client
}






// Proxy function for RuntimeService
func (p *CRIProxy) proxyRuntime(ctx context.Context, req interface{}, runtimeClass string, config *cri.PodSandboxConfig) (interface{}, error) {
	//	logrus.Debug("proxyRuntime called")
	client := p.getRuntimeClient(runtimeClass)
	switch request := req.(type) {
        case *cri.RunPodSandboxRequest:
		logrus.Infof("RunPodSandboxRequest called: %s", runtimeClass)
		res, err := client.RunPodSandbox(ctx, request)
		p.SetPodSandboxMapping(res.GetPodSandboxId(), runtimeClass, config.Metadata.Uid)
		return res, err
          
        case *cri.StopPodSandboxRequest:
		runtimeClass = p.GetPodSandboxMapping(request.GetPodSandboxId())
		logrus.Infof("StopPodSandboxRequest called: SandboxID: %s   runtimeClass: %s", request.GetPodSandboxId(), runtimeClass)		
		client := p.getRuntimeClient(runtimeClass)
		return client.StopPodSandbox(ctx, request)
          
        case *cri.RemovePodSandboxRequest:
		runtimeClass = p.GetPodSandboxMapping(request.GetPodSandboxId())		
		client := p.getRuntimeClient(runtimeClass)
		logrus.Infof("RemovePodSandboxRequest called: SandboxID: %s   runtimeClass: %s", request.GetPodSandboxId(), runtimeClass)				
		res, err := client.RemovePodSandbox(ctx, request)
		if err == nil {
			p.RemovePodSandboxMapping(request.GetPodSandboxId())
		}
		return res, err
          
        case *cri.PodSandboxStatusRequest:
		runtimeClass = p.GetPodSandboxMapping(request.GetPodSandboxId())
		client := p.getRuntimeClient(runtimeClass)
		//		logrus.Debugf("PodSandboxStatusRequest called: SandboxID: %s   runtimeClass: %s", request.GetPodSandboxId(), runtimeClass)
		return client.PodSandboxStatus(ctx, request)
          
        case *cri.ListPodSandboxRequest:
		//		logrus.Debug("ListPodSandboxRequest called")
  		var PodSandboxResponses []*cri.ListPodSandboxResponse
		// Collect all the PodSandbox responses
         	for runtimeClass, _ := range p.config.Runtimes {
			//			logrus.Debugf("Getting PodSandboxes for runtime: %s", runtimeClass)
			client := p.getRuntimeClient(runtimeClass)			
			res, err := client.ListPodSandbox(ctx, request)
			if err != nil {
				return nil, err
			}    
			PodSandboxResponses = append(PodSandboxResponses, res)
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
		logrus.Info("CreateContainerRequest called")
		runtimeClass = p.GetPodSandboxMapping(request.GetPodSandboxId())
		client := p.getRuntimeClient(runtimeClass)
		res, err := client.CreateContainer(ctx, request)
		p.SetContainerMapping(res.GetContainerId(), runtimeClass)
		return res, err
          
        case *cri.StartContainerRequest:
		logrus.Infof("StartContainerRequest called for container id: %s", request.GetContainerId())
		runtimeClass = p.GetContainerMapping(request.GetContainerId())
		client := p.getRuntimeClient(runtimeClass)
		return client.StartContainer(ctx, request)
          
        case *cri.StopContainerRequest:
		logrus.Infof("StopContainerRequest called for container id: %s", request.GetContainerId())
		runtimeClass = p.GetContainerMapping(request.GetContainerId())
		client := p.getRuntimeClient(runtimeClass)
		return client.StopContainer(ctx, request)
          
        case *cri.RemoveContainerRequest:
		logrus.Infof("RemoveContainerRequest called container id: %s", request.GetContainerId())
		runtimeClass = p.GetContainerMapping(request.GetContainerId())
		client := p.getRuntimeClient(runtimeClass)
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
			res, err := client.ListContainers(ctx, request)
			if err != nil {
				return nil, err
			}    
			containerResponses = append(containerResponses, res)
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
		return client.ContainerStatus(ctx, request)
          
        case *cri.UpdateContainerResourcesRequest:
		logrus.Debugf("UpdateContainerResourcesRequest called for container id: %s", request.GetContainerId())
		runtimeClass = p.GetContainerMapping(request.GetContainerId())
		client := p.getRuntimeClient(runtimeClass)
		return client.UpdateContainerResources(ctx, request)

        case *cri.ReopenContainerLogRequest:
		logrus.Debug("ReopenContainerLogRequest called")
		runtimeClass = p.GetContainerMapping(request.GetContainerId())
		client := p.getRuntimeClient(runtimeClass)
		return client.ReopenContainerLog(ctx, request)
          
        case *cri.ExecSyncRequest:
		logrus.Debug("ExecSyncRequest called")
		runtimeClass = p.GetContainerMapping(request.GetContainerId())
		client := p.getRuntimeClient(runtimeClass)
		return client.ExecSync(ctx, request)
          
        case *cri.ExecRequest:
		logrus.Debug("ExecRequest called")
		runtimeClass = p.GetContainerMapping(request.GetContainerId())
		client := p.getRuntimeClient(runtimeClass)
		return client.Exec(ctx, request)
          	
        case *cri.AttachRequest:
		logrus.Debug("AttachRequest called")
		runtimeClass = p.GetContainerMapping(request.GetContainerId())
		client := p.getRuntimeClient(runtimeClass)
		return client.Attach(ctx, request)
          
        case *cri.PortForwardRequest:
		logrus.Debug("PortForwardRequest called")
		runtimeClass = p.GetPodSandboxMapping(request.GetPodSandboxId())
		client := p.getRuntimeClient(runtimeClass)		
		return client.PortForward(ctx, request)
          
        case *cri.ContainerStatsRequest:
		logrus.Debugf("ContainerStatsRequest called container id: %s", request.GetContainerId())
		runtimeClass = p.GetContainerMapping(request.GetContainerId())
		client := p.getRuntimeClient(runtimeClass)
		return client.ContainerStats(ctx, request)
          
        case *cri.ListContainerStatsRequest:
		logrus.Debug("ListContainerStatsRequest called")
		var listContainerStatsResponses []*cri.ListContainerStatsResponse
		// Collect all the container responses
         	for runtimeClass, _ := range p.config.Runtimes {
			logrus.Debugf("Getting container stats for runtime: %s", runtimeClass)
			client := p.getRuntimeClient(runtimeClass)						
			res, err := client.ListContainerStats(ctx, request)
			if err != nil {
				return nil, err
			}    
			listContainerStatsResponses = append(listContainerStatsResponses, res)
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
		return client.PodSandboxStats(ctx, request)
          
        case *cri.ListPodSandboxStatsRequest:
		logrus.Debug("ListPodSandboxStatsRequest called")
		var listPodSandboxStatsResponses []*cri.ListPodSandboxStatsResponse
		// Collect all the container responses
         	for runtimeClass, _ := range p.config.Runtimes {
			logrus.Debugf("Getting container stats for runtime: %s", runtimeClass)
			client := p.getRuntimeClient(runtimeClass)						
			res, err := client.ListPodSandboxStats(ctx, request)
			if err != nil {
				return nil, err
			}    
			listPodSandboxStatsResponses = append(listPodSandboxStatsResponses, res)
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
		logrus.Debug("UpdateRuntimeConfigRequest called")
          return client.UpdateRuntimeConfig(ctx, request)
          
        case *cri.StatusRequest:
		logrus.Debug("StatusRequest called")
		return client.Status(ctx, request)
          
        case *cri.CheckpointContainerRequest:
		logrus.Debug("CheckpointContainerRequest called")
          return client.CheckpointContainer(ctx, request)
          
        case *cri.GetEventsRequest:
		logrus.Debug("GetEventsRequest called")
		return client.GetContainerEvents(ctx, request)
          
        case *cri.ListMetricDescriptorsRequest:
		     logrus.Debug("ListMetricDescriptorsRequest called")
          return client.ListMetricDescriptors(ctx, request)
          
        case *cri.ListPodSandboxMetricsRequest:
		     logrus.Debug("ListPodSandboxMetricsRequest called")
          return client.ListPodSandboxMetrics(ctx, request)
          
        case *cri.RuntimeConfigRequest:
		     logrus.Debug("RuntimeConfigRequest called")
          return client.RuntimeConfig(ctx, request)
          
	default:
		return nil, fmt.Errorf("unsupported runtime request")
	}
}



// Proxy function for ImageService
func (p *CRIProxy) proxyImage(ctx context.Context, req interface{}, runtimeClass string, config *cri.PodSandboxConfig) (interface{}, error) {
//	logrus.Debug("proxyImage called")
	conn, err := p.getGRPCConn(runtimeClass)
	if err != nil {
		return nil, err
	}
	client := cri.NewImageServiceClient(conn)
	switch request := req.(type) {
	case *cri.PullImageRequest:
	        logrus.Debug("PullImageRequest called")
		runtimeClass = p.GetSandboxConfigMapping(config)
		client := p.getImageClient(runtimeClass)
		return client.PullImage(ctx, request)
	case *cri.ListImagesRequest:
	        logrus.Debug("ListImagesRequest called")
		var imageResponses []*cri.ListImagesResponse
		// Collect all the image responses
         	for runtimeClass, _ := range p.config.Runtimes {
                  logrus.Debugf("Getting images for runtime: %s", runtimeClass)
		  conn, err := p.getGRPCConn(runtimeClass)
		  if err != nil {
                    return nil, err
                  }    
		  client := cri.NewImageServiceClient(conn)
		  res, err := client.ListImages(ctx, request)
  		  if err != nil {
                    return nil, err
                  }    
                  imageResponses = append(imageResponses, res)
                }
		// Merge the image responses into a single response
		merged := &cri.ListImagesResponse{}
		for _, resp := range imageResponses {
  		  if resp != nil {
		    merged.Images = append(merged.Images, resp.GetImages()...)
		  }
		}
         	return merged, nil
	case *cri.ImageStatusRequest:
	        logrus.Debug("ImageStatusRequest called")		
		return client.ImageStatus(ctx, request)
	case *cri.RemoveImageRequest:
	        logrus.Debug("RemoveImageRequest called")		
		return client.RemoveImage(ctx, request)
         case *cri.ImageFsInfoRequest:
//	        logrus.Debug("ImageFsInfoRequest called")		 
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
//        logrus.Debug("ImageFsInfo called")	
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


func (p *CRIProxy) initialise() {
	req := &cri.ListPodSandboxRequest{}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	
	// Collect all the PodSandbox responses
	for runtimeClass, _ := range p.config.Runtimes {
		logrus.Infof("Getting PodSandboxes for runtime: %s", runtimeClass)
		client := p.getRuntimeClient(runtimeClass)			
		res, err := client.ListPodSandbox(ctx, req)
		if err == nil {
			for _ , sb := range res.Items  {
				p.SetPodSandboxMapping(sb.GetId(), runtimeClass, sb.GetMetadata().GetUid())						
			}
		}
	}
	
	// Collect all the container responses
	req2 := &cri.ListContainersRequest{}	
	for runtimeClass, _ := range p.config.Runtimes {
		logrus.Infof("Getting Containers for runtime: %s", runtimeClass)		
		client := p.getRuntimeClient(runtimeClass)						
		res, err := client.ListContainers(ctx, req2)
		if err == nil {
			for _, c := range res.GetContainers() {
				p.SetContainerMapping(c.GetId(), runtimeClass)
			}
		}    
	}
	defer cancel() // Ensure context is cancelled
}


// Main function
func main() {
	configPath := "config.yaml"
	config, err := loadConfig(configPath)

	// Set log level
	logrus.SetLevel(logrus.InfoLevel) // Change to DebugLevel, WarnLevel, etc.
	
	if err != nil {
		logrus.Fatalf("Failed to load config: %v", err)
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


		
	listener, err := net.Listen("unix", "/var/run/crismux.sock")
	if err != nil {
		log.Fatalf("Failed to listen on socket: %v", err)
	}

	logrus.Debug("CRI Proxy started on /var/run/crismux.sock")
	defer proxy.CloseConnections() // Ensure cleanup

	if err := grpcServer.Serve(listener); err != nil {
		log.Fatalf("Failed to start gRPC server: %v", err)
	}
}
