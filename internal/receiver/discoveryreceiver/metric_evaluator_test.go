// Copyright  Splunk, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package discoveryreceiver

import (
	"context"
	"testing"
	"time"

	"github.com/open-telemetry/opentelemetry-collector-contrib/extension/observer"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.uber.org/zap"

	"github.com/signalfx/splunk-otel-collector/internal/common/discovery"
)

func TestMetricEvaluatorBaseMetricConsumer(t *testing.T) {
	logger := zap.NewNop()
	cfg := &Config{}
	plogs := make(chan plog.Logs)
	cStore := newCorrelationStore(logger, time.Hour)

	me := newMetricEvaluator(logger, cfg, plogs, cStore)
	require.Equal(t, consumer.Capabilities{}, me.Capabilities())

	md := pmetric.NewMetrics()
	require.NoError(t, me.ConsumeMetrics(context.Background(), md))
}

func TestMetricEvaluation(t *testing.T) {
	// If debugging tests, replace the Nop Logger with a test instance to see
	// all statements. Not in regular use to avoid spamming output.
	// logger := zaptest.NewLogger(t)
	logger := zap.NewNop()
	for _, tc := range []struct {
		name  string
		match Match
	}{
		{name: "strict", match: Match{Strict: "desired.name"}},
		{name: "regexp", match: Match{Regexp: "^d[esired]{6}.name$"}},
		{name: "expr", match: Match{Expr: "name == 'desired.name'"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			match := tc.match
			match.Record = &LogRecord{
				Body: "desired body content",
				Attributes: map[string]string{
					"one": "one.value", "two": "two.value",
				},
			}
			for _, status := range discovery.StatusTypes {
				match.Status = status
				t.Run(string(status), func(t *testing.T) {
					observerID := component.MustNewIDWithName("an_observer", "observer.name")
					receiverID := component.MustNewIDWithName("a_receiver", "receiver.name")
					cfg := &Config{
						Receivers: map[component.ID]ReceiverEntry{
							receiverID: {
								Rule:   Rule{text: "a.rule", program: nil},
								Status: &Status{Metrics: []Match{match}},
							},
						},
						WatchObservers: []component.ID{observerID},
					}
					require.NoError(t, cfg.Validate())

					plogs := make(chan plog.Logs)
					cStore := newCorrelationStore(logger, time.Hour)
					cStore.UpdateEndpoint(observer.Endpoint{ID: "endpoint.id"}, receiverID, addedState, observerID)

					me := newMetricEvaluator(logger, cfg, plogs, cStore)

					md := pmetric.NewMetrics()
					rm := md.ResourceMetrics().AppendEmpty()

					rAttrs := rm.Resource().Attributes()
					rAttrs.PutStr("discovery.receiver.type", "a_receiver")
					rAttrs.PutStr("discovery.receiver.name", "receiver.name")
					rAttrs.PutStr("discovery.endpoint.id", "endpoint.id")

					sm := rm.ScopeMetrics().AppendEmpty()
					sms := sm.Metrics()
					sms.AppendEmpty().SetName("undesired.name")
					sms.AppendEmpty().SetName("another.undesired.name")
					sms.AppendEmpty().SetName("desired.name")
					sms.AppendEmpty().SetName("desired.name")
					sms.AppendEmpty().SetName("desired.name")

					emitted := me.evaluateMetrics(md)

					require.Equal(t, 1, emitted.LogRecordCount())

					rl := emitted.ResourceLogs().At(0)
					require.Equal(t, 0, rl.Resource().Attributes().Len())

					sLogs := rl.ScopeLogs()
					require.Equal(t, 1, sLogs.Len())
					sl := sLogs.At(0)
					lrs := sl.LogRecords()
					require.Equal(t, 1, lrs.Len())
					lr := sl.LogRecords().At(0)

					lrAttrs := lr.Attributes()
					require.Equal(t, map[string]any{
						discovery.OtelEntityIDAttr: map[string]any{
							"discovery.endpoint.id": "endpoint.id",
						},
						discovery.OtelEntityEventTypeAttr: discovery.OtelEntityEventTypeState,
						discovery.OtelEntityAttributesAttr: map[string]any{
							"discovery.event.type":    "metric.match",
							"discovery.observer.id":   "an_observer/observer.name",
							"discovery.receiver.name": "receiver.name",
							"discovery.receiver.rule": "a.rule",
							"discovery.receiver.type": "a_receiver",
							"discovery.status":        string(status),
							"discovery.message":       "desired body content",
							"metric.name":             "desired.name",
							"one":                     "one.value",
							"two":                     "two.value",
						},
					}, lrAttrs.AsRaw())
				})
			}
		})
	}
}

func TestTimestampFromMetric(t *testing.T) {
	expectedTime := pcommon.NewTimestampFromTime(time.Now())
	for _, test := range []struct {
		metricFunc func(pmetric.Metric) (shouldBeNil bool)
		name       string
	}{
		{name: "MetricTypeGauge", metricFunc: func(md pmetric.Metric) bool {
			md.SetEmptyGauge()
			md.Gauge().DataPoints().AppendEmpty().SetTimestamp(expectedTime)
			return false
		}},
		{name: "empty MetricTypeGauge", metricFunc: func(md pmetric.Metric) bool {
			md.SetEmptyGauge()
			return true
		}},
		{name: "MetricTypeSum", metricFunc: func(md pmetric.Metric) bool {
			md.SetEmptySum()
			md.Sum().DataPoints().AppendEmpty().SetTimestamp(expectedTime)
			return false
		}},
		{name: "empty MetricTypeSum", metricFunc: func(md pmetric.Metric) bool {
			md.SetEmptySum()
			return true
		}},
		{name: "MetricTypeHistogram", metricFunc: func(md pmetric.Metric) bool {
			md.SetEmptyHistogram()
			md.Histogram().DataPoints().AppendEmpty().SetTimestamp(expectedTime)
			return false
		}},
		{name: "empty MetricTypeHistogram", metricFunc: func(md pmetric.Metric) bool {
			md.SetEmptyHistogram()
			return true
		}},
		{name: "MetricTypeExponentialHistogram", metricFunc: func(md pmetric.Metric) bool {
			md.SetEmptyExponentialHistogram()
			md.ExponentialHistogram().DataPoints().AppendEmpty().SetTimestamp(expectedTime)
			return false
		}},
		{name: "empty MetricTypeExponentialHistogram", metricFunc: func(md pmetric.Metric) bool {
			md.SetEmptyExponentialHistogram()
			return true
		}},
		{name: "MetricTypeSummary", metricFunc: func(md pmetric.Metric) bool {
			md.SetEmptySummary()
			md.Summary().DataPoints().AppendEmpty().SetTimestamp(expectedTime)
			return false
		}},
		{name: "empty MetricTypeSummary", metricFunc: func(md pmetric.Metric) bool {
			md.SetEmptySummary()
			return true
		}},
		{name: "MetricTypeNone", metricFunc: func(_ pmetric.Metric) bool { return true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			me := newMetricEvaluator(zap.NewNop(), &Config{}, make(chan plog.Logs), nil)
			md := pmetric.NewMetrics().ResourceMetrics().AppendEmpty().ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
			shouldBeNil := test.metricFunc(md)
			actual := me.timestampFromMetric(md)
			if shouldBeNil {
				require.Nil(t, actual)
			} else {
				require.NotNil(t, actual)
				require.Equal(t, expectedTime, *actual)
			}
		})
	}
}
