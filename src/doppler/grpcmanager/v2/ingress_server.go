package v2

import (
	"log"
	"metric"
	"plumbing/conversion"
	plumbing "plumbing/v2"
	"time"

	"github.com/cloudfoundry/sonde-go/events"
)

type DopplerIngress_SenderServer interface {
	plumbing.DopplerIngress_SenderServer
}

type DataSetter interface {
	Set(data *events.Envelope)
}

type IngressServer struct {
	envelopeBuffer DataSetter
}

func NewIngressServer(envelopeBuffer DataSetter) *IngressServer {
	return &IngressServer{
		envelopeBuffer: envelopeBuffer,
	}
}

func (i IngressServer) Sender(s plumbing.DopplerIngress_SenderServer) error {
	var count uint64
	lastEmitted := time.Now()
	for {
		v2e, err := s.Recv()
		if err != nil {
			return err
		}

		v1e := conversion.ToV1(v2e)
		if v1e == nil || v1e.EventType == nil {
			continue
		}

		count++
		if count >= 1000 || time.Since(lastEmitted) > 5*time.Second {
			// metric:v2 (loggregator.doppler.ingress) Number of received
			// envelopes from Metron on Doppler's v2 gRPC server
			metric.IncCounter("ingress",
				metric.WithIncrement(count),
				metric.WithVersion(2, 0),
			)
			log.Printf("Ingressed (v2) %d envelopes", count)
			lastEmitted = time.Now()
			count = 0
		}
		i.envelopeBuffer.Set(v1e)
	}
}
