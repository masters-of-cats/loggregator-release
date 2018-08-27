package egress

import (
	"errors"
	"fmt"
	"io"
	"log"
	"sync/atomic"
	"time"

	"code.cloudfoundry.org/loggregator/metricemitter"
	"code.cloudfoundry.org/loggregator/plumbing/batching"
	v2 "code.cloudfoundry.org/loggregator/plumbing/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	"golang.org/x/net/context"
)

const (
	envelopeBufferSize = 10000
)

// HealthRegistrar provides an interface to record various counters.
type HealthRegistrar interface {
	Inc(name string)
	Dec(name string)
}

// Receiver creates a function which will receive envelopes on a stream.
type Receiver interface {
	Receive(ctx context.Context, req *v2.EgressRequest) (rx func() (*v2.Envelope, error), err error)
}

// MetricClient creates new CounterMetrics to be emitted periodically.
type MetricClient interface {
	NewCounter(name string, opts ...metricemitter.MetricOption) *metricemitter.Counter
	NewGauge(name, unit string, opts ...metricemitter.MetricOption) *metricemitter.Gauge
}

// Server represents a bridge between inbound data from the Receiver and
// outbound data on a gRPC stream.
type Server struct {
	receiver           Receiver
	egressMetric       *metricemitter.Counter
	slowConsumerMetric *metricemitter.Counter
	rejectedMetric     *metricemitter.Counter

	health        HealthRegistrar
	ctx           context.Context
	batchSize     int
	batchInterval time.Duration
	maxStreams    int64
	subscriptions int64
}

// NewServer is the preferred way to create a new Server.
func NewServer(
	r Receiver,
	m MetricClient,
	h HealthRegistrar,
	c context.Context,
	batchSize int,
	batchInterval time.Duration,
	maxStreams int64,
) *Server {
	egressMetric := m.NewCounter("egress",
		metricemitter.WithVersion(2, 0),
	)

	rejectedMetric := m.NewCounter("rejected_streams",
		metricemitter.WithVersion(2, 0),
	)

	slowConsumerMetric := m.NewCounter("slow_consumers",
		metricemitter.WithVersion(2, 0),
	)

	s := &Server{
		receiver:           r,
		egressMetric:       egressMetric,
		rejectedMetric:     rejectedMetric,
		slowConsumerMetric: slowConsumerMetric,
		health:             h,
		ctx:                c,
		batchSize:          batchSize,
		batchInterval:      batchInterval,
		maxStreams:         maxStreams,
	}

	go func() {
		subscriptionsMetric := m.NewGauge("subscriptions", "count",
			metricemitter.WithVersion(2, 0),
		)

		ticker := time.NewTicker(time.Second)
		for range ticker.C {
			subCount := atomic.LoadInt64(&s.subscriptions)
			subscriptionsMetric.Set(float64(subCount))
		}
	}()

	return s
}

// Receiver implements the loggregator-api V2 gRPC interface for receiving
// envelopes from upstream connections.
func (s *Server) Receiver(r *v2.EgressRequest, srv v2.Egress_ReceiverServer) error {
	s.health.Inc("subscriptionCount")
	defer s.health.Dec("subscriptionCount")

	subCount := atomic.AddInt64(&s.subscriptions, 1)
	defer atomic.AddInt64(&s.subscriptions, -1)

	if subCount > s.maxStreams {
		s.rejectedMetric.Increment(1)
		return grpc.Errorf(codes.ResourceExhausted, "unable to create stream, max egress streams reached: %d", s.maxStreams)
	}

	if r.GetFilter() != nil &&
		r.GetFilter().SourceId == "" &&
		r.GetFilter().Message != nil {
		return errors.New("invalid request: cannot have type filter without source id")
	}

	ctx, cancel := context.WithCancel(srv.Context())
	defer cancel()

	buffer := make(chan *v2.Envelope, envelopeBufferSize)

	go func() {
		select {
		case <-s.ctx.Done():
			cancel()
		case <-ctx.Done():
			cancel()
		}
	}()

	rx, err := s.receiver.Receive(ctx, r)
	if err != nil {
		log.Printf("Unable to setup subscription: %s", err)
		return fmt.Errorf("unable to setup subscription")
	}

	go s.consumeReceiver(buffer, rx, cancel)

	for data := range buffer {
		if err := srv.Send(data); err != nil {
			log.Printf("Send error: %s", err)
			return io.ErrUnexpectedEOF
		}

		// metric-documentation-v2: (loggregator.rlp.egress) Number of v2
		// envelopes sent to RLP consumers.
		s.egressMetric.Increment(1)
	}

	return nil
}

// BatchedReceiver implements the loggregator-api V2 gRPC interface for
// receiving batches of envelopes. Envelopes will be written to the egress
// batched receiver server whenever the configured interval or configured
// batch size is exceeded.
func (s *Server) BatchedReceiver(r *v2.EgressBatchRequest, srv v2.Egress_BatchedReceiverServer) error {
	s.health.Inc("subscriptionCount")
	defer s.health.Dec("subscriptionCount")

	subCount := atomic.AddInt64(&s.subscriptions, 1)
	defer atomic.AddInt64(&s.subscriptions, -1)

	if subCount > s.maxStreams {
		s.rejectedMetric.Increment(1)
		return grpc.Errorf(codes.ResourceExhausted, "unable to create stream, max egress streams reached: %d", s.maxStreams)
	}

	if r.GetFilter() != nil &&
		r.GetFilter().SourceId == "" &&
		r.GetFilter().Message != nil {
		return errors.New("invalid request: cannot have type filter without source id")
	}

	ctx, cancel := context.WithCancel(srv.Context())
	defer cancel()

	buffer := make(chan *v2.Envelope, envelopeBufferSize)

	go func() {
		select {
		case <-s.ctx.Done():
			cancel()
		case <-ctx.Done():
			cancel()
		}
	}()

	rx, err := s.receiver.Receive(ctx, &v2.EgressRequest{
		ShardId:          r.GetShardId(),
		Filter:           r.GetFilter(),
		UsePreferredTags: r.GetUsePreferredTags(),
	})
	// TODO Add coverage for this error case
	if err != nil {
		log.Printf("Unable to setup subscription: %s", err)
		return fmt.Errorf("unable to setup subscription")
	}

	receiveErrorStream := make(chan error, 1)
	go s.consumeBatchReceiver(buffer, receiveErrorStream, rx, cancel)

	senderErrorStream := make(chan error, 1)
	batcher := batching.NewV2EnvelopeBatcher(
		s.batchSize,
		s.batchInterval,
		&batchWriter{
			srv:          srv,
			errStream:    senderErrorStream,
			egressMetric: s.egressMetric,
		},
	)

	for {
		select {
		case data := <-buffer:
			batcher.Write(data)
		case <-senderErrorStream:
			return io.ErrUnexpectedEOF
		case <-receiveErrorStream:
			for len(buffer) > 0 {
				data := <-buffer
				batcher.Write(data)
			}
			batcher.ForcedFlush()

			return nil
		default:
			batcher.Flush()
			time.Sleep(10 * time.Millisecond)
		}
	}

	return nil
}

type batchWriter struct {
	srv          v2.Egress_BatchedReceiverServer
	errStream    chan<- error
	egressMetric *metricemitter.Counter
}

func (b *batchWriter) Write(batch []*v2.Envelope) {
	err := b.srv.Send(&v2.EnvelopeBatch{Batch: batch})
	if err != nil {
		select {
		case b.errStream <- err:
		default:
		}
		return
	}

	// metric-documentation-v2: (loggregator.rlp.egress) Number of v2
	// envelopes sent to RLP consumers.
	b.egressMetric.Increment(uint64(len(batch)))
}

func (s *Server) consumeBatchReceiver(
	buffer chan<- *v2.Envelope,
	errorStream chan<- error,
	rx func() (*v2.Envelope, error),
	cancel func(),
) {

	defer cancel()

	for {
		e, err := rx()
		if err == io.EOF {
			errorStream <- err
			break
		}

		if err != nil {
			log.Printf("Subscribe error: %s", err)
			errorStream <- err
			break
		}

		select {
		case buffer <- e:
		default:
			s.slowConsumerMetric.Increment(1)
			log.Println("slow consumer detected in batch receiver, closing stream.")
			return
		}
	}
}

func (s *Server) consumeReceiver(
	buffer chan<- *v2.Envelope,
	rx func() (*v2.Envelope, error),
	cancel func(),
) {

	defer cancel()
	defer close(buffer)

	for {
		e, err := rx()
		if err == io.EOF {
			break
		}

		if err != nil {
			log.Printf("Subscribe error: %s", err)
			break
		}

		select {
		case buffer <- e:
		default:
			s.slowConsumerMetric.Increment(1)
			log.Println("slow consumer detected in receiver, closing stream.")
			return
		}
	}
}
