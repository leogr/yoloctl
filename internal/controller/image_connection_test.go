package controller

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	cd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	controlapi "github.com/moby/buildkit/api/services/control"
	bk "github.com/moby/buildkit/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	health "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type namespaceReader struct {
	health.UnimplementedHealthServer
	missing atomic.Int32
}

func (s *namespaceReader) Check(ctx context.Context, _ *health.HealthCheckRequest) (*health.HealthCheckResponse, error) {
	ns, err := namespaces.NamespaceRequired(ctx)
	if err != nil {
		s.missing.Add(1)
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	if ns != "default" {
		return nil, status.Error(codes.InvalidArgument, "unexpected namespace")
	}
	return &health.HealthCheckResponse{Status: health.HealthCheckResponse_SERVING}, nil
}

type workerNamespaceProbe struct {
	controlapi.UnimplementedControlServer
	content health.HealthClient
}

func (s *workerNamespaceProbe) Info(ctx context.Context, _ *controlapi.InfoRequest) (*controlapi.InfoResponse, error) {
	// As in BuildKit's containerd worker: use the incoming RPC context with a
	// real containerd client configured WithDefaultNamespace("default").
	if _, err := s.content.Check(ctx, &health.HealthCheckRequest{}); err != nil {
		return nil, err
	}
	return &controlapi.InfoResponse{}, nil
}
func TestBuildkitRPCDoesNotBreakWorkerContainerdNamespace(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	contentListener := bufconn.Listen(1 << 20)
	contentServer := grpc.NewServer()
	reader := &namespaceReader{}
	health.RegisterHealthServer(contentServer, reader)
	go func() { _ = contentServer.Serve(contentListener) }()
	t.Cleanup(func() { contentServer.Stop(); _ = contentListener.Close() })
	containerd, err := cd.New("/namespace-test", cd.WithDefaultNamespace("default"), cd.WithDialOpts([]grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return contentListener.DialContext(ctx) })}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = containerd.Close() })
	connection, ok := containerd.Conn().(*grpc.ClientConn)
	if !ok {
		t.Fatal("expected grpc containerd connection")
	}
	workerListener := bufconn.Listen(1 << 20)
	workerServer := grpc.NewServer()
	controlapi.RegisterControlServer(workerServer, &workerNamespaceProbe{content: health.NewHealthClient(connection)})
	go func() { _ = workerServer.Serve(workerListener) }()
	t.Cleanup(func() { workerServer.Stop(); _ = workerListener.Close() })
	dial := bk.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return workerListener.DialContext(ctx) })
	raw, err := bk.New(ctx, "unix:///namespace-test", dial)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	withNamespace := imageContext(metadata.AppendToOutgoingContext(ctx, "yolo-test", "retain"))
	// Reproduce D1 with the pinned real containerd default-ns interceptor:
	// incoming namespace suppresses outgoing namespace on its child RPC.
	if _, err := raw.ControlClient().Info(withNamespace, &controlapi.InfoRequest{}); err == nil || !strings.Contains(err.Error(), "namespace is required") {
		t.Fatal("transport-shaped namespace repro missing", err)
	}
	ensured, created := 0, 0
	backend := &NativeImageBackend{EnsureBuilder: func(context.Context) error { ensured++; return nil }}
	backend.newBuilder = func(ctx context.Context) (*bk.Client, error) {
		if ensured == 0 {
			t.Error("connected before EnsureBuilder")
		}
		created++
		return newBuildkitConnection(ctx, "unix:///namespace-test", dial)
	}
	t.Cleanup(backend.Close)
	for range 2 {
		client, err := backend.readyBuilder(withNamespace)
		if err != nil || client == nil {
			t.Fatal("isolated namespace/readiness failed", err)
		}
	}
	if created != 1 || ensured != 2 || reader.missing.Load() != 1 {
		t.Fatal("lazy connection or namespace fallback incorrect", created, ensured, reader.missing.Load())
	}
	original, _ := metadata.FromOutgoingContext(withNamespace)
	if original.Get(namespaces.GRPCHeader)[0] != "default" || original.Get("yolo-test")[0] != "retain" {
		t.Fatal("mutated containerd caller metadata", original)
	}
}
func TestPreSolveConnectionFailureIsNotStarted(t *testing.T) {
	ensured := false
	backend := &NativeImageBackend{EnsureBuilder: func(context.Context) error { ensured = true; return nil }, newBuilder: func(context.Context) (*bk.Client, error) {
		if !ensured {
			t.Error("connection attempted before service start")
		}
		return nil, status.Error(codes.Unavailable, "dial unix: no such file")
	}}
	_, err := backend.Build(t.Context(), recipe("not-started"), ImagePhase{})
	var failed *ImageBuildFailure
	if !errors.As(err, &failed) || !strings.Contains(err.Error(), "not started") || !strings.Contains(err.Error(), "no such file") {
		t.Fatal("pre-solve dial failure treated as interruption", err)
	}
	// A caller cancellation must keep its cancellation semantics.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = backend.Build(ctx, recipe("cancelled"), ImagePhase{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation changed: %v", err)
	}
}
