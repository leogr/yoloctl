package agentbox

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	taskapi "github.com/containerd/containerd/api/services/tasks/v1"
	taskpb "github.com/containerd/containerd/api/types/task"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/leogr/yoloctl/internal/container"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Direct typed Task API stubs, not a shim, daemon, kernel/cgroup/projection
// census or runtime qualification. The production interface has only these
// two reads and cannot call aggregate task listing or mutations.
type nativeTaskAPIFixture struct {
	get  func(context.Context, *taskapi.GetRequest, ...grpc.CallOption) (*taskapi.GetResponse, error)
	pids func(context.Context, *taskapi.ListPidsRequest, ...grpc.CallOption) (*taskapi.ListPidsResponse, error)
}

func (f nativeTaskAPIFixture) Get(ctx context.Context, r *taskapi.GetRequest, o ...grpc.CallOption) (*taskapi.GetResponse, error) {
	return f.get(ctx, r, o...)
}
func (f nativeTaskAPIFixture) ListPids(ctx context.Context, r *taskapi.ListPidsRequest, o ...grpc.CallOption) (*taskapi.ListPidsResponse, error) {
	return f.pids(ctx, r, o...)
}

func taskFactResponse(id string, pid uint32, state taskpb.Status) *taskapi.GetResponse {
	return &taskapi.GetResponse{Process: &taskpb.Process{ID: id, Pid: pid, Status: state, Stdin: "fixture-secret-stdin", Stdout: "fixture-private-stdout"}}
}

func taskFactCallScope(t *testing.T, ctx context.Context, namespace string, options []grpc.CallOption) {
	t.Helper()
	observed, ok := namespaces.Namespace(ctx)
	if !ok || observed != namespace {
		t.Fatal("task API namespace was inferred/changed", observed)
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 20*time.Second {
		t.Fatal("task observation lacks its finite inherited deadline")
	}
	bounded := false
	for _, option := range options {
		if receive, ok := option.(grpc.MaxRecvMsgSizeCallOption); ok && receive.MaxRecvMsgSize == 1<<20 {
			bounded = true
		}
	}
	if !bounded {
		t.Fatal("task API response lacks byte bound")
	}
}

func TestNativeTaskFactsKeepTypedNamespaceIDsAndReportedPIDsWithoutAuthority(t *testing.T) {
	id := strings.Repeat("a", 64)
	gets, pids := 0, 0
	api := nativeTaskAPIFixture{
		get: func(ctx context.Context, r *taskapi.GetRequest, o ...grpc.CallOption) (*taskapi.GetResponse, error) {
			taskFactCallScope(t, ctx, "other-namespace", o)
			gets++
			if r.ContainerID != id || r.ExecID != "" {
				t.Fatal("task API selected another init/exec", r.ContainerID, r.ExecID)
			}
			return taskFactResponse(id, 1234, taskpb.Status_RUNNING), nil
		},
		pids: func(ctx context.Context, r *taskapi.ListPidsRequest, o ...grpc.CallOption) (*taskapi.ListPidsResponse, error) {
			taskFactCallScope(t, ctx, "other-namespace", o)
			pids++
			if r.ContainerID != id {
				t.Fatal("PID read changed container")
			}
			return &taskapi.ListPidsResponse{Processes: []*taskpb.ProcessInfo{{Pid: 1236}, {Pid: 1234}, {Pid: 1235}}}, nil
		},
	}
	reader := &nativeTaskReader{api: api}
	facts, err := reader.Observe(t.Context(), "other-namespace", []string{id})
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 || facts[0].lookup != nativeTaskLookupFound || !facts[0].pidsObserved || facts[0].initPID != 1234 || !reflect.DeepEqual(facts[0].pids, []uint32{1234, 1235, 1236}) || gets != 2 || pids != 1 {
		t.Fatal("task facts lost explicit requested scope/process data", facts, gets, pids)
	}
	if text := fmt.Sprintf("%v %#v", facts, facts); strings.Contains(text, "fixture-secret") || strings.Contains(text, "fixture-private") {
		t.Fatal("task facts exposed private process channels")
	}
}

func TestNativeTaskFactsNotFoundAndStoppedNeverInventEmptyPIDProof(t *testing.T) {
	for _, kind := range []string{"not-found", "stopped-zero", "stopped-stale", "created-zero"} {
		t.Run(kind, func(t *testing.T) {
			gets := 0
			api := nativeTaskAPIFixture{get: func(context.Context, *taskapi.GetRequest, ...grpc.CallOption) (*taskapi.GetResponse, error) {
				gets++
				switch kind {
				case "not-found":
					return nil, status.Error(codes.NotFound, "typed lookup outcome, no absence authority")
				case "stopped-zero":
					return taskFactResponse("id", 0, taskpb.Status_STOPPED), nil
				case "stopped-stale":
					return taskFactResponse("id", 4567, taskpb.Status_STOPPED), nil
				default:
					return taskFactResponse("id", 0, taskpb.Status_CREATED), nil
				}
			}, pids: func(context.Context, *taskapi.ListPidsRequest, ...grpc.CallOption) (*taskapi.ListPidsResponse, error) {
				t.Fatal("unknown/stopped coverage was replaced with a PID query/default")
				return nil, nil
			}}
			facts, err := (&nativeTaskReader{api: api}).Observe(t.Context(), "default", []string{"id"})
			if err != nil || len(facts) != 1 || gets != 2 {
				t.Fatal("typed task outcome was omitted", err, gets)
			}
			if facts[0].lookup == nativeTaskLookupUnknown || facts[0].pidsObserved || len(facts[0].pids) != 0 {
				t.Fatal("missing/stopped task became an empty-process proof", facts)
			}
			if kind == "not-found" && facts[0].lookup != nativeTaskLookupNotFound || kind != "not-found" && facts[0].lookup != nativeTaskLookupFound {
				t.Fatal("task outcome classification changed", facts)
			}
			if kind == "stopped-stale" && facts[0].initPID != 4567 {
				t.Fatal("stopped task PID was silently rewritten to zero")
			}
		})
	}
}

func TestNativeTaskFactsRefuseFailedPartialOrChangingReadsWithoutRetry(t *testing.T) {
	for _, change := range []string{"get-failed", "pids-failed", "nil-get", "nil-process", "foreign-init", "foreign-container", "unknown-state", "running-zero", "pid-one", "nil-pids", "empty-pids", "nil-pid", "pid-zero", "duplicate-pid", "missing-init", "pid-bound", "changed-init", "disappeared", "cancelled", "cancelled-after-get"} {
		t.Run(change, func(t *testing.T) {
			gets, pids := 0, 0
			var cancelAfter context.CancelFunc
			failure := status.Error(codes.Unavailable, "fixture actual read failure")
			api := nativeTaskAPIFixture{
				get: func(context.Context, *taskapi.GetRequest, ...grpc.CallOption) (*taskapi.GetResponse, error) {
					gets++
					if change == "get-failed" {
						return nil, failure
					}
					if change == "nil-get" {
						return nil, nil
					}
					if change == "nil-process" {
						return &taskapi.GetResponse{}, nil
					}
					if change == "disappeared" && gets == 2 {
						return nil, status.Error(codes.NotFound, "disappeared after first read")
					}
					root := taskFactResponse("id", 1234, taskpb.Status_RUNNING)
					switch change {
					case "foreign-init":
						root.Process.ID = "foreign"
					case "foreign-container":
						root.Process.ContainerID = "foreign"
					case "unknown-state":
						root.Process.Status = taskpb.Status_UNKNOWN
					case "running-zero":
						root.Process.Pid = 0
					case "pid-one":
						root.Process.Pid = 1
					case "changed-init":
						if gets == 2 {
							root.Process.Pid = 5678
						}
					}
					if change == "cancelled-after-get" && gets == 2 {
						cancelAfter()
					}
					return root, nil
				},
				pids: func(context.Context, *taskapi.ListPidsRequest, ...grpc.CallOption) (*taskapi.ListPidsResponse, error) {
					pids++
					if change == "pids-failed" {
						return nil, failure
					}
					if change == "nil-pids" {
						return nil, nil
					}
					result := &taskapi.ListPidsResponse{Processes: []*taskpb.ProcessInfo{{Pid: 1234}}}
					switch change {
					case "empty-pids":
						result.Processes = nil
					case "nil-pid":
						result.Processes = []*taskpb.ProcessInfo{nil}
					case "pid-zero":
						result.Processes = append(result.Processes, &taskpb.ProcessInfo{})
					case "duplicate-pid":
						result.Processes = append(result.Processes, &taskpb.ProcessInfo{Pid: 1234})
					case "missing-init":
						result.Processes = []*taskpb.ProcessInfo{{Pid: 5678}}
					case "pid-bound":
						result.Processes = make([]*taskpb.ProcessInfo, 4097)
					}
					return result, nil
				},
			}
			ctx := t.Context()
			if change == "cancelled-after-get" {
				ctx, cancelAfter = context.WithCancel(ctx)
				defer cancelAfter()
			}
			if change == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			facts, err := (&nativeTaskReader{api: api}).Observe(ctx, "default", []string{"id"})
			if err == nil || facts != nil {
				t.Fatal("failed/partial/changing task observation became successful evidence", change, err, facts)
			}
			if change == "get-failed" || change == "pids-failed" {
				if !errors.Is(err, failure) {
					t.Fatal("actual task API failure was suppressed/substituted", err)
				}
			}
			if gets > 2 || pids > 1 {
				t.Fatal("task observer retried failed/changed reads", gets, pids)
			}
			if change == "cancelled" && (gets != 0 || pids != 0 || !errors.Is(err, context.Canceled)) {
				t.Fatal("cancelled task observation called API", gets, pids, err)
			}
		})
	}
}

func TestNativeTaskFactsSelectionAndCloseCannotBecomeSafeEmptyEvidence(t *testing.T) {
	calls, closes := 0, 0
	api := nativeTaskAPIFixture{get: func(context.Context, *taskapi.GetRequest, ...grpc.CallOption) (*taskapi.GetResponse, error) {
		calls++
		return nil, status.Error(codes.NotFound, "lookup outcome only")
	}, pids: func(context.Context, *taskapi.ListPidsRequest, ...grpc.CallOption) (*taskapi.ListPidsResponse, error) {
		calls++
		return nil, nil
	}}
	reader := &nativeTaskReader{api: api, close: func() error { closes++; return nil }}
	for _, selection := range []struct {
		namespace string
		ids       []string
	}{{"", []string{"id"}}, {"../foreign", []string{"id"}}, {"default", []string{"id", "id"}}, {"default", []string{""}}, {"default", make([]string, 1025)}} {
		if facts, err := reader.Observe(t.Context(), selection.namespace, selection.ids); err == nil || facts != nil || calls != 0 {
			t.Fatal("invalid task selection reached API", err, calls)
		}
	}
	if facts, err := reader.Observe(t.Context(), "default", nil); err != nil || len(facts) != 0 || calls != 0 {
		t.Fatal("explicit empty selection became invented API data", err, calls)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil || closes != 1 {
		t.Fatal("reader close replayed", err, closes)
	}
	if facts, err := reader.Observe(t.Context(), "default", []string{"id"}); err == nil || facts != nil || calls != 0 {
		t.Fatal("closed task reader became safe empty evidence", err, calls)
	}
	var absent *nativeTaskReader
	if facts, err := absent.Observe(t.Context(), "default", []string{"id"}); err == nil || facts != nil {
		t.Fatal("missing task reader became safe empty evidence")
	}
	if reader, err := newNativeTaskReader(t.Context(), container.New("fixture")); err == nil || reader != nil {
		t.Fatal("ordinary executor opened native system task reader", err)
	}
}
