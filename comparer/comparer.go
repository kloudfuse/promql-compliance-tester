package comparer

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/pkg/errors"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	"github.com/promlabs/promql-compliance-tester/config"
)

const (
	defaultFraction = 0.00001
	defaultMargin   = 0.0
)

// PromAPI allows running instant and range queries against a Prometheus-compatible API.
type PromAPI interface {
	// Query performs a query for the given time.
	Query(ctx context.Context, query string, ts time.Time) (model.Value, v1.Warnings, error)
	// QueryRange performs a query for the given range.
	QueryRange(ctx context.Context, query string, r v1.Range) (model.Value, v1.Warnings, error)
}

// TestCase represents a fully expanded query to be tested.
type TestCase struct {
	Query          string        `json:"query"`
	SkipComparison bool          `json:"skipComparison"`
	ShouldFail     bool          `json:"shouldFail"`
	Start          time.Time     `json:"start"`
	End            time.Time     `json:"end"`
	Resolution     time.Duration `json:"resolution"`
}

// A Comparer allows comparing query results for test cases between a reference API and a test API.
type Comparer struct {
	refAPI         PromAPI
	testAPI        PromAPI
	queryTweaks    []*config.QueryTweak
	compareOptions cmp.Options
}

// New returns a new Comparer.
func New(refAPI, testAPI PromAPI, queryTweaks []*config.QueryTweak) *Comparer {
	var options cmp.Options
	addFloatCompareOptions(queryTweaks, &options)
	addDropResultLabelsOptions(queryTweaks, &options)
	return &Comparer{
		refAPI:         refAPI,
		testAPI:        testAPI,
		queryTweaks:    queryTweaks,
		compareOptions: options,
	}
}

// Result tracks a single test case's query comparison result.
type Result struct {
	TestCase          *TestCase       `json:"testCase"`
	Diff              string          `json:"diff"`
	UnexpectedFailure string          `json:"unexpectedFailure"`
	UnexpectedSuccess bool            `json:"unexpectedSuccess"`
	Unsupported       bool            `json:"unsupported"`
	Durations         []time.Duration `json:"durations"`
	Range             v1.Range
	RefResult         model.Value
	TestResult        model.Value
}

// Success returns true if the comparison result was successful.
func (r *Result) Success() bool {
	return r.Diff == "" && !r.UnexpectedSuccess && r.UnexpectedFailure == ""
}

// Compare runs a test case query against the reference API and the test API and compares the results.
func (c *Comparer) Compare(tc *TestCase) (*Result, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	r := v1.Range{
		Start: tc.Start,
		End:   tc.End,
		Step:  tc.Resolution,
	}

	// TODO: Handle warnings (second, ignored return value).
	start := time.Now()
	refResult, _, refErr := c.refAPI.QueryRange(ctx, tc.Query, r)
	duration1 := time.Since(start)
	start = time.Now()
	testResult, _, testErr := c.testAPI.QueryRange(ctx, tc.Query, r)
	duration2 := time.Since(start)

	if (refErr != nil) != tc.ShouldFail {
		if refErr != nil {
			return nil, errors.Wrapf(refErr, "querying reference API for %q", tc.Query)
		}
		return nil, fmt.Errorf("expected reference API query %q to fail, but succeeded", tc.Query)
	}

	if (testErr != nil) != tc.ShouldFail {
		if testErr != nil {
			return &Result{RefResult: refResult, TestResult: testResult, Range: r, Durations: []time.Duration{duration1, duration2}, TestCase: tc, UnexpectedFailure: testErr.Error(), Unsupported: strings.Contains(testErr.Error(), "501")}, nil
		}
		return &Result{RefResult: refResult, TestResult: testResult, Range: r, Durations: []time.Duration{duration1, duration2}, TestCase: tc, UnexpectedSuccess: true}, nil
	}

	if tc.SkipComparison || tc.ShouldFail {
		return &Result{RefResult: refResult, TestResult: testResult, TestCase: tc, Durations: []time.Duration{0, 0}, Range: r}, nil
	}

	sort.Sort(testResult.(model.Matrix))
	for _, qt := range c.queryTweaks {
		if qt.IgnoreFirstStep {
			for _, r := range refResult.(model.Matrix) {
				if len(r.Values) > 0 && r.Values[0].Timestamp.Time().Sub(tc.Start) <= 2*time.Millisecond {
					r.Values = r.Values[1:]
				}
			}
		}
	}
	return &Result{
		TestCase:   tc,
		Diff:       cmp.Diff(refResult, testResult, c.compareOptions),
		Durations:  []time.Duration{duration1, duration2},
		Range:      r,
		RefResult:  refResult,
		TestResult: testResult,
	}, nil
}

func addFloatCompareOptions(queryTweaks []*config.QueryTweak, options *cmp.Options) {
	fraction := defaultFraction
	margin := defaultMargin
	for _, rt := range queryTweaks {
		if rt.AdjustValueTolerance != nil {
			if rt.AdjustValueTolerance.Fraction != nil {
				fraction = *rt.AdjustValueTolerance.Fraction
			}
			if rt.AdjustValueTolerance.Margin != nil {
				margin = *rt.AdjustValueTolerance.Margin
			}
		}
	}
	*options = append(
		*options,
		// Translate sample values into float64 so that cmpopts.EquateApprox() works.
		cmp.Transformer("TranslateFloat64", func(in model.SampleValue) float64 {
			return float64(in)
		}),
		cmpopts.EquateApprox(fraction, margin),
		// A NaN is usually not treated as equal to another NaN, but we want to treat it as such here.
		cmpopts.EquateNaNs(),
	)
}

func addDropResultLabelsOptions(queryTweaks []*config.QueryTweak, options *cmp.Options) {
	for _, rt := range queryTweaks {
		if len(rt.DropResultLabels) != 0 {
			localRt := rt
			*options = append(
				*options,
				cmp.Transformer(
					"DropResultLabels",
					func(in model.Metric) model.Metric {
						m := in.Clone()
						for _, ln := range localRt.DropResultLabels {
							delete(m, ln)
						}
						return m
					},
				),
			)
		}
	}
}
