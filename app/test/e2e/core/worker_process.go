//go:build e2e

package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
)

var (
	workerBinaryOnce sync.Once
	workerBinary     string
	workerBinaryErr  error
)

// buildWorker compiles cmd/worker once for the whole run: the real binary, so that what receives a
// signal is the real process and not a stand-in for it.
func buildWorker() (string, error) {
	workerBinaryOnce.Do(func() {
		dir, err := os.MkdirTemp("", "e2e-worker-")
		if err != nil {
			workerBinaryErr = err
			return
		}
		root, err := repoFile(".")
		if err != nil {
			workerBinaryErr = err
			return
		}
		workerBinary = filepath.Join(dir, "worker")
		build := exec.CommandContext(context.Background(), "go", "build", "-o", workerBinary, "./app/cmd/worker")
		build.Dir = root
		if out, err := build.CombinedOutput(); err != nil {
			workerBinaryErr = fmt.Errorf("build the worker: %w\n%s", err, out)
		}
	})
	return workerBinary, workerBinaryErr
}

// lockedBuffer is written by the process's output copier and read by the test, at the same time.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// WorkerProcess is the worker binary running as an operating system process, consuming a queue of
// its own so that no other consumer of the suite can take its messages.
type WorkerProcess struct {
	stack    *Stack
	cmd      *exec.Cmd
	output   *lockedBuffer
	exited   chan struct{}
	exitErr  error
	queueURL string
	probeURL string
}

// StartWorkerProcess creates a dedicated FIFO queue, starts the worker binary on it and waits until
// the worker answers its probe. The process is killed when the test ends if it is still running.
func (s *Stack) StartWorkerProcess(t *testing.T) *WorkerProcess {
	t.Helper()

	binary, err := buildWorker()
	if err != nil {
		t.Fatal(err)
	}

	client := s.sqs(t)
	created, err := client.CreateQueue(context.Background(), &sqs.CreateQueueInput{
		QueueName: awssdk.String("e2e-" + uuid.NewString() + ".fifo"),
		Attributes: map[string]string{
			"FifoQueue":         "true",
			"VisibilityTimeout": "60",
		},
	})
	if err != nil {
		t.Fatalf("create the dedicated queue: %v", err)
	}

	port, err := freePort()
	if err != nil {
		t.Fatalf("pick a port: %v", err)
	}

	proc := &WorkerProcess{
		stack:    s,
		output:   &lockedBuffer{},
		exited:   make(chan struct{}),
		queueURL: awssdk.ToString(created.QueueUrl),
		probeURL: "http://127.0.0.1:" + port,
	}
	proc.cmd = exec.CommandContext(context.Background(), binary)
	// The environment of the suite already points at the containers; only what is particular to this
	// process is overridden, and the last value of a variable wins.
	proc.cmd.Env = append(os.Environ(),
		"PORT="+port,
		"SQS_QUEUE_URL="+proc.queueURL,
		"WORKER_POLL_TIMEOUT=1s",
		"WORKER_VISIBILITY_TIMEOUT=60",
		"WORKER_CONCURRENCY=2",
	)
	proc.cmd.Stdout = proc.output
	proc.cmd.Stderr = proc.output
	if err := proc.cmd.Start(); err != nil {
		t.Fatalf("start the worker process: %v", err)
	}
	go func() {
		proc.exitErr = proc.cmd.Wait()
		close(proc.exited)
	}()
	t.Cleanup(func() {
		select {
		case <-proc.exited:
		default:
			_ = proc.cmd.Process.Kill()
			<-proc.exited
		}
	})

	proc.waitUntilAnswering(t)
	return proc
}

func (p *WorkerProcess) waitUntilAnswering(t *testing.T) {
	t.Helper()

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-p.exited:
			t.Fatalf("the worker process exited during boot: %v\n%s", p.exitErr, p.output.String())
		default:
		}
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, p.probeURL+"/health/live", nil)
		if err != nil {
			t.Fatal(err)
		}
		if res, err := httpClient.Do(req); err == nil {
			_ = res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("the worker process never answered its probe\n%s", p.output.String())
}

// Send publishes a message to the queue of this process.
func (p *WorkerProcess) Send(t *testing.T, groupID, body string) {
	t.Helper()

	_, err := p.stack.sqs(t).SendMessage(context.Background(), &sqs.SendMessageInput{
		QueueUrl:               awssdk.String(p.queueURL),
		MessageBody:            awssdk.String(body),
		MessageGroupId:         awssdk.String(groupID),
		MessageDeduplicationId: awssdk.String("dedup-" + uuid.NewString()),
	})
	if err != nil {
		t.Fatalf("publish to the dedicated queue: %v", err)
	}
}

// QueueState is how many messages of this process's queue are waiting and how many are in flight.
func (p *WorkerProcess) QueueState(t *testing.T) (int, int) {
	t.Helper()

	out, err := p.stack.sqs(t).GetQueueAttributes(context.Background(), &sqs.GetQueueAttributesInput{
		QueueUrl: awssdk.String(p.queueURL),
		AttributeNames: []types.QueueAttributeName{
			types.QueueAttributeNameApproximateNumberOfMessages,
			types.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
		},
	})
	if err != nil {
		t.Fatalf("read queue attributes: %v", err)
	}
	visible, _ := strconv.Atoi(out.Attributes["ApproximateNumberOfMessages"])
	inFlight, _ := strconv.Atoi(out.Attributes["ApproximateNumberOfMessagesNotVisible"])
	return visible, inFlight
}

// Terminate sends SIGTERM, the signal an orchestrator sends on a deploy.
func (p *WorkerProcess) Terminate(t *testing.T) {
	t.Helper()

	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal the worker process: %v", err)
	}
}

// Running reports whether the process has not exited yet.
func (p *WorkerProcess) Running() bool {
	select {
	case <-p.exited:
		return false
	default:
		return true
	}
}

// WaitForExit waits for the process to exit and returns its exit code.
func (p *WorkerProcess) WaitForExit(t *testing.T, timeout time.Duration) int {
	t.Helper()

	select {
	case <-p.exited:
	case <-time.After(timeout):
		t.Fatalf("the worker process did not exit within %s\n%s", timeout, p.output.String())
	}
	if p.exitErr == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(p.exitErr, &exit) {
		return exit.ExitCode()
	}
	t.Fatalf("waiting for the worker process: %v", p.exitErr)
	return -1
}

// Output is what the process wrote to its standard output and error.
func (p *WorkerProcess) Output() string { return p.output.String() }
