package agentbox

import (
	"context"
	"errors"
	"os"
	"reflect"
	"slices"
	"sync"
	"time"

	taskapi "github.com/containerd/containerd/api/services/tasks/v1"
	taskpb "github.com/containerd/containerd/api/types/task"
	cd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/identifiers"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type nativeTaskLookup uint8

const (
	nativeTaskLookupUnknown  nativeTaskLookup = iota
	nativeTaskLookupNotFound                  // typed RPC outcome only, NOT kernel absence/inactivity
	nativeTaskLookupFound
)

// These facts cover only the explicitly requested namespace/IDs. They are not
// a complete task/consumer census, current kernel liveness or Stop/rm ACKs.
// In particular, NotFound or STOPPED with a stale/zero init PID does NOT prove
// no surviving descendants/execs/different views. Independent kernel cgroup,
// pinned-process/root/mount projection and complete namespace/container input
// verification remain mandatory before negative-consumer authority.
type nativeTaskFact struct {
	namespace, cid string
	lookup         nativeTaskLookup
	initPID        uint32 // observed field; may be stale for STOPPED
	state          taskpb.Status
	pidsObserved   bool // false is UNKNOWN coverage, never an empty-process proof
	pids           []uint32
}

func (nativeTaskFact) String() string     { return "native task API observation [not authority]" }
func (f nativeTaskFact) GoString() string { return f.String() }

// A reader belongs to one admitted infrastructure call. It uses the same fixed
// system-containerd address/client dependency as the existing image backend;
// no socket/endpoint is accepted from workload metadata. It never lists tasks:
// the server's aggregate List may silently omit failed per-task state reads.
type nativeTaskReader struct {
	mu     sync.Mutex
	api    nativeTaskAPI
	close  func() error
	closed bool
}

type nativeTaskAPI interface {
	Get(context.Context, *taskapi.GetRequest, ...grpc.CallOption) (*taskapi.GetResponse, error)
	ListPids(context.Context, *taskapi.ListPidsRequest, ...grpc.CallOption) (*taskapi.ListPidsResponse, error)
}

func newNativeTaskReader(ctx context.Context, client *container.Client) (*nativeTaskReader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if client == nil || !spaceenv.Native() || os.Geteuid() != 0 || os.Getegid() != 0 || os.Getpid() == 1 {
		return nil, errors.New("native task reader requires admitted VM-root infrastructure")
	}
	if err := spaceenv.RequireSpace(client.Env()); err != nil {
		return nil, err
	}
	connection, err := cd.New("/run/containerd/containerd.sock", cd.WithDefaultNamespace("default"))
	if err != nil {
		return nil, err
	}
	return &nativeTaskReader{api: connection.TaskService(), close: connection.Close}, nil
}

func (r *nativeTaskReader) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	r.api = nil
	if r.close == nil {
		return nil
	}
	close := r.close
	r.close = nil
	return close()
}

func nativeTaskSelection(namespace string, ids []string) ([]string, error) {
	if identifiers.Validate(namespace) != nil || len(ids) > 1024 {
		return nil, errors.New("invalid/beyond-bound native task selection")
	}
	result := slices.Clone(ids)
	slices.Sort(result)
	for n, id := range result {
		if identifiers.Validate(id) != nil || n > 0 && result[n-1] == id {
			return nil, errors.New("invalid/duplicate native task container ID")
		}
	}
	return result, nil
}

func (r *nativeTaskReader) Observe(ctx context.Context, namespace string, ids []string) ([]nativeTaskFact, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errors.New("native task reader is unavailable")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.api == nil {
		return nil, errors.New("native task reader is closed/unavailable")
	}
	selected, err := nativeTaskSelection(namespace, ids)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ctx = namespaces.WithNamespace(ctx, namespace)
	result := make([]nativeTaskFact, 0, len(selected))
	total := 0
	for _, id := range selected {
		before, err := readNativeTaskRoot(ctx, r.api, namespace, id)
		if err != nil {
			return nil, err
		}
		fact := before
		// A stopped/zero PID is deliberately retained without inventing an
		// empty Pids response. Its kernel completeness is a separate obligation.
		if before.lookup == nativeTaskLookupFound && before.state != taskpb.Status_STOPPED && before.initPID > 1 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			response, err := r.api.ListPids(ctx, &taskapi.ListPidsRequest{ContainerID: id}, grpc.MaxCallRecvMsgSize(1<<20))
			if err != nil {
				return nil, err
			}
			if response == nil || len(response.Processes) == 0 || len(response.Processes) > 4096 {
				return nil, errors.New("native task PID read is missing/beyond bound")
			}
			seen := map[uint32]bool{}
			for _, process := range response.Processes {
				if process == nil || process.Pid <= 1 || seen[process.Pid] {
					return nil, errors.New("native task PID read has unknown/duplicate identity")
				}
				seen[process.Pid] = true
				fact.pids = append(fact.pids, process.Pid)
			}
			if !seen[before.initPID] {
				return nil, errors.New("native task PID read omits its observed init")
			}
			slices.Sort(fact.pids)
			fact.pidsObserved = true
			total += len(fact.pids)
			if total > 4096 {
				return nil, errors.New("native task observations exceed total PID bound")
			}
		}
		after, err := readNativeTaskRoot(ctx, r.api, namespace, id)
		if err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(before, after) {
			return nil, errors.New("native task lookup/init fields changed around observation")
		}
		result = append(result, fact)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func readNativeTaskRoot(ctx context.Context, api nativeTaskAPI, namespace, id string) (nativeTaskFact, error) {
	if err := ctx.Err(); err != nil {
		return nativeTaskFact{}, err
	}
	response, err := api.Get(ctx, &taskapi.GetRequest{ContainerID: id}, grpc.MaxCallRecvMsgSize(1<<20))
	if cancelled := ctx.Err(); cancelled != nil {
		return nativeTaskFact{}, cancelled
	}
	if status.Code(err) == codes.NotFound {
		return nativeTaskFact{namespace: namespace, cid: id, lookup: nativeTaskLookupNotFound}, nil
	}
	if err != nil {
		return nativeTaskFact{}, err
	}
	if response == nil || response.Process == nil {
		return nativeTaskFact{}, errors.New("native task lookup lacks explicit process fields")
	}
	p := response.Process
	if p.ID != id || p.ContainerID != "" && p.ContainerID != id || p.Pid == 1 || p.Status < taskpb.Status_CREATED || p.Status > taskpb.Status_PAUSING || p.Pid == 0 && p.Status != taskpb.Status_CREATED && p.Status != taskpb.Status_STOPPED {
		return nativeTaskFact{}, errors.New("native task lookup has foreign/unknown init fields")
	}
	return nativeTaskFact{namespace: namespace, cid: id, lookup: nativeTaskLookupFound, initPID: p.Pid, state: p.Status}, nil
}
