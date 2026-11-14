package store

import (
	"database/sql/driver"
	"errors"
	"math"
	"math/big"
	"modernc.org/sqlite"
)

// MetricLimit is reserved as an overflow sentinel. Stored buckets must stay below it.
// Keeping it below MaxInt64/2 also makes SQL ranking by rx+tx safe.
const MetricLimit int64 = 1<<62 - 1

func AddMetric(a, b int64) int64 {
	if a < 0 || b < 0 || a >= MetricLimit || b >= MetricLimit-a {
		return MetricLimit
	}
	return a + b
}

var errMetricOverflow = errors.New("统计值超出整数安全范围")

type metricSum struct {
	sum     big.Int
	invalid int64
}

func (s *metricSum) change(v driver.Value, sign int64) {
	if v == nil {
		return
	}
	n, ok := v.(int64)
	if !ok || n < 0 {
		s.invalid += sign
		return
	}
	var x big.Int
	x.SetInt64(n)
	if sign > 0 {
		s.sum.Add(&s.sum, &x)
	} else {
		s.sum.Sub(&s.sum, &x)
	}
}
func (s *metricSum) Step(_ *sqlite.FunctionContext, args []driver.Value) error {
	s.change(args[0], 1)
	return nil
}
func (s *metricSum) WindowInverse(_ *sqlite.FunctionContext, args []driver.Value) error {
	s.change(args[0], -1)
	return nil
}
func (s *metricSum) WindowValue(_ *sqlite.FunctionContext) (driver.Value, error) {
	if s.invalid > 0 || !s.sum.IsInt64() || s.sum.Int64() >= MetricLimit {
		return MetricLimit, nil
	}
	return s.sum.Int64(), nil
}
func (s *metricSum) Final(_ *sqlite.FunctionContext) {}
func init() {
	sqlite.MustRegisterFunction("metric_sum", &sqlite.FunctionImpl{NArgs: 1, MakeAggregate: func(sqlite.FunctionContext) (sqlite.AggregateFunction, error) { return &metricSum{}, nil }})
}

func EstimateMetric(n float64) int64 {
	if math.IsNaN(n) || math.IsInf(n, 0) || n >= float64(MetricLimit) || n < 0 {
		return MetricLimit
	}
	return int64(n)
}
