package testhelper

import (
	"sync"

	"code.cloudfoundry.org/loggregator/metricemitter"
	v2 "code.cloudfoundry.org/loggregator/plumbing/v2"
)

type counterMetric struct {
	metricName string
	metric     *metricemitter.Counter
}

type gaugeMetric struct {
	metricName string
	metric     *metricemitter.Gauge
}

type SpyMetricClient struct {
	mu             sync.Mutex
	counterMetrics []counterMetric
	gaugeMetrics   []gaugeMetric
}

func NewMetricClient() *SpyMetricClient {
	return &SpyMetricClient{}
}

func (s *SpyMetricClient) NewCounter(name string, opts ...metricemitter.MetricOption) *metricemitter.Counter {
	s.mu.Lock()
	defer s.mu.Unlock()

	m := metricemitter.NewCounter(name, "", opts...)

	s.counterMetrics = append(s.counterMetrics, counterMetric{
		metricName: name,
		metric:     m,
	})

	return m
}

func (s *SpyMetricClient) NewGauge(name, unit string, opts ...metricemitter.MetricOption) *metricemitter.Gauge {
	s.mu.Lock()
	defer s.mu.Unlock()

	m := metricemitter.NewGauge(name, unit, "", opts...)

	s.gaugeMetrics = append(s.gaugeMetrics, gaugeMetric{
		metricName: name,
		metric:     m,
	})

	return m
}

func (s *SpyMetricClient) GetDelta(name string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, m := range s.counterMetrics {
		if m.metricName == name {
			return m.metric.GetDelta()
		}
	}

	return 0
}

func (s *SpyMetricClient) GetEnvelopes(name string) []*v2.Envelope {
	s.mu.Lock()
	defer s.mu.Unlock()

	var envs []*v2.Envelope

	for _, m := range s.counterMetrics {
		if m.metricName == name {
			var env *v2.Envelope
			_ = m.metric.WithEnvelope(func(e *v2.Envelope) error {
				env = e
				return nil
			})

			envs = append(envs, env)
		}
	}

	for _, m := range s.gaugeMetrics {
		if m.metricName == name {
			var env *v2.Envelope
			_ = m.metric.WithEnvelope(func(e *v2.Envelope) error {
				env = e
				return nil
			})

			envs = append(envs, env)
		}
	}

	return envs
}

func (s *SpyMetricClient) GetValue(name string) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, m := range s.gaugeMetrics {
		if m.metricName == name {
			return m.metric.GetValue()
		}
	}

	return 0
}
