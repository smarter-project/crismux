package main

import (
	"context"
	"log"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
	cri "k8s.io/cri-api/pkg/apis/runtime/v1"
)

const bufSize = 1024 * 1024

var lis *bufconn.Listener

func init() {
	lis = bufconn.Listen(bufSize)
	s := grpc.NewServer()
	cri.RegisterRuntimeServiceServer(s, &CRIProxy{})
	cri.RegisterImageServiceServer(s, &CRIProxy{})
	go func() {
		if err := s.Serve(lis); err != nil {
			log.Fatalf("Server exited with error: %v", err)
		}
	}()
}

func bufDialer(context.Context, string) (net.Conn, error) {
	return lis.Dial()
}

func TestRunPodSandbox(t *testing.T) {
	ctx := context.Background()
	conn, err := grpc.DialContext(ctx, "bufnet", grpc.WithContextDialer(bufDialer), grpc.WithInsecure())
	if err != nil {
		t.Fatalf("Failed to dial bufnet: %v", err)
	}
	defer conn.Close()
	client := cri.NewRuntimeServiceClient(conn)

	req := &cri.RunPodSandboxRequest{
		Config: &cri.PodSandboxConfig{
			Metadata: &cri.PodSandboxMetadata{
				Name: "test-sandbox",
			},
		},
		RuntimeHandler: "default",
	}
	resp, err := client.RunPodSandbox(ctx, req)
	if err != nil {
		t.Fatalf("RunPodSandbox failed: %v", err)
	}
	if resp == nil {
		t.Fatalf("Expected non-nil response")
	}
}

func TestCreateContainer(t *testing.T) {
	ctx := context.Background()
	conn, err := grpc.DialContext(ctx, "bufnet", grpc.WithContextDialer(bufDialer), grpc.WithInsecure())
	if err != nil {
		t.Fatalf("Failed to dial bufnet: %v", err)
	}
	defer conn.Close()
	client := cri.NewRuntimeServiceClient(conn)

	req := &cri.CreateContainerRequest{
		PodSandboxId: "test-sandbox",
		Config: &cri.ContainerConfig{
			Metadata: &cri.ContainerMetadata{
				Name: "test-container",
			},
		},
		SandboxConfig: &cri.PodSandboxConfig{
			Metadata: &cri.PodSandboxMetadata{
				Name: "test-sandbox",
			},
		},
	}
	resp, err := client.CreateContainer(ctx, req)
	if err != nil {
		t.Fatalf("CreateContainer failed: %v", err)
	}
	if resp == nil {
		t.Fatalf("Expected non-nil response")
	}
}

func TestPullImage(t *testing.T) {
	ctx := context.Background()
	conn, err := grpc.DialContext(ctx, "bufnet", grpc.WithContextDialer(bufDialer), grpc.WithInsecure())
	if err != nil {
		t.Fatalf("Failed to dial bufnet: %v", err)
	}
	defer conn.Close()
	client := cri.NewImageServiceClient(conn)

	req := &cri.PullImageRequest{
		Image: &cri.ImageSpec{
			Image: "test-image",
		},
	}
	resp, err := client.PullImage(ctx, req)
	if err != nil {
		t.Fatalf("PullImage failed: %v", err)
	}
	if resp == nil {
		t.Fatalf("Expected non-nil response")
	}
}

func TestRemoveImage(t *testing.T) {
	ctx := context.Background()
	conn, err := grpc.DialContext(ctx, "bufnet", grpc.WithContextDialer(bufDialer), grpc.WithInsecure())
	if err != nil {
		t.Fatalf("Failed to dial bufnet: %v", err)
	}
	defer conn.Close()
	client := cri.NewImageServiceClient(conn)

	req := &cri.RemoveImageRequest{
		Image: &cri.ImageSpec{
			Image: "test-image",
		},
	}
	resp, err := client.RemoveImage(ctx, req)
	if err != nil {
		t.Fatalf("RemoveImage failed: %v", err)
	}
	if resp == nil {
		t.Fatalf("Expected non-nil response")
	}
}
