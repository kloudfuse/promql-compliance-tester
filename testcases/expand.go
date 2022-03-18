// Some of this code has been taken and adapted from InfluxData:
// https://github.com/influxdata/influxdb/blob/26fdb792ffd74f773c253df5d9bebf64ef2b3214/query/promql/internal/promqltests/tests.go
//
// The original copyright notice and license of that code is reproduced here:
//
// -------------------------------------------------------------------------------
//
// MIT License

// Copyright (c) 2018 InfluxData

// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:

// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.

// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.
//
// -------------------------------------------------------------------------------

package testcases

import (
	"bytes"
	"fmt"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/pkg/errors"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/promlabs/promql-compliance-tester/comparer"
	"github.com/promlabs/promql-compliance-tester/config"
)

var testVariantArgs = map[string][]string{
	"range":  {"1s", "15s", "1m", "5m", "15m", "1h"},
	"offset": {"1m", "5m", "10m"},
	// TODO: Add "group" aggregator and new duration formats, but it is so new that vendor implementations need time to catch up first.
	"simpleAggrOp": {"sum", "avg", "max", "min", "count", "stddev", "stdvar"},
	"topBottomOp":  {"topk", "bottomk"},
	"quantile": {
		"-0.5",
		"0.1",
		"0.5",
		"0.75",
		"0.95",
		"0.90",
		"0.99",
		"1",
		"1.5",
	},
	"arithBinOp":           {"+", "-", "*", "/", "%", "^"},
	"compBinOp":            {"==", "!=", "<", ">", "<=", ">="},
	"binOp":                {"+", "-", "*", "/", "%", "^", "==", "!=", "<", ">", "<=", ">="},
	"simpleMathFunc":       {"abs", "ceil", "floor", "exp", "sqrt", "ln", "log2", "log10", "round"},
	"extrapolatedRateFunc": {"delta", "rate", "increase"},
	"clampFunc":            {"clamp_min", "clamp_max"},
	"instantRateFunc":      {"idelta", "irate"},
	"dateFunc":             {"day_of_month", "day_of_week", "days_in_month", "hour", "minute", "month", "year"},
	"smoothingFactor":      {"0.1", "0.5", "0.8"},
	"trendFactor":          {"0.1", "0.5", "0.8"},
}

// tprintf replaces template arguments in a string with their instantiations from the provided map.
func tprintf(tmpl string, data map[string]string) string {
	t := template.Must(template.New("Query").Parse(tmpl))
	buf := &bytes.Buffer{}
	if err := t.Execute(buf, data); err != nil {
		panic(err)
	}
	return buf.String()
}

// getVariants returns every possible combinations (variants) of a template query.
func getVariants(query string, remainingVariantArgs []string, args map[string]string) []string {
	// Either this Query had no variants defined to begin with or they have
	// been fully filled out in "args" from recursive parent calls.
	if len(remainingVariantArgs) == 0 {
		return []string{tprintf(query, args)}
	}

	// Recursively iterate through the values for each variant arg dimension,
	// selecting one dimension (arg) to vary per recursion level and let the
	// other recursion levels iterate through the remaining dimensions until
	// all args are defined.
	var queries []string
	vArg := remainingVariantArgs[0]
	filteredVArgs := make([]string, 0, len(remainingVariantArgs)-1)
	for _, va := range remainingVariantArgs {
		if va != vArg {
			filteredVArgs = append(filteredVArgs, va)
		}
	}

	vals := testVariantArgs[vArg]
	if len(vals) == 0 {
		panic(fmt.Errorf("unknown variant arg %q", vArg))
	}
	for _, variantVal := range vals {
		args[vArg] = variantVal
		qs := getVariants(query, filteredVArgs, args)
		queries = append(queries, qs...)
	}
	return queries
}

func applyQueryTweaks(tc *comparer.TestCase, tweaks []*config.QueryTweak) *comparer.TestCase {
	resTC := *tc
	for _, t := range tweaks {
		if d := time.Duration(t.TruncateTimestampsToMS) * time.Millisecond; d != 0 {
			resTC.Start = resTC.Start.Truncate(d)
			resTC.End = resTC.End.Truncate(d)
		}
		if t.AlignTimestampsToStep {
			resTC.Start = resTC.Start.Truncate(resTC.Resolution)
			resTC.End = resTC.End.Truncate(resTC.Resolution)
		}
	}
	return &resTC
}

func parseTime(s string) (time.Time, error) {
	if t, err := strconv.ParseFloat(s, 64); err == nil {
		s, ns := math.Modf(t)
		return time.Unix(int64(s), int64(ns*float64(time.Second))).UTC(), nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Time{}, errors.Errorf("cannot parse %q to a valid timestamp", s)
}

// QueryTimeParameters contain endTime, Range (start), and Resolution (Step). If this is given,
// Then use it to generate v1.Range.
// Otherwise, we use a hardcoded various values defined in this function.
func generateQueryTimeRangeParameters(queryTimeParameters config.QueryTimeParameters) []v1.Range {
	ranges := make([]v1.Range, 0)

	endTimes := make([]time.Time, 0)
	if queryTimeParameters.EndTime != "" {
		endTime, err := parseTime(queryTimeParameters.EndTime)
		if err == nil {
			endTimes = append(endTimes, endTime)
		}
		panic(fmt.Errorf("Invalid config QueryTimeParameters.EndTime received"))
	} else {
		// Add an end timestamp that ensures at least some data is on a consuming segment.
		endTimes = append(endTimes, time.Now().UTC().Add(-2*time.Minute))
		// Add an end time that ensures at least some data is an offline segment
		endTimes = append(endTimes, time.Now().UTC().Add(-12*time.Hour))
	}

	rangesInSeconds := make([]float64, 0)
	steps := make([]float64, 0)
	if queryTimeParameters.RangeInSeconds != 0 {
		rangesInSeconds = append(rangesInSeconds, queryTimeParameters.RangeInSeconds)
		if queryTimeParameters.ResolutionInSeconds != 0 {
			steps = append(steps, queryTimeParameters.ResolutionInSeconds)
		} else {
			steps = append(steps, 20)
		}
	} else {
		// 5 minutes, 15 minutes, 30 minutes, 1 hour, 3 hours, 6 hours, 12 hours, 24 hours
		rangesInSeconds = append(rangesInSeconds, []float64{300, 900, 1800, 3600, 10800, 21600, 43200, 86400}...)
		if queryTimeParameters.ResolutionInSeconds != 0 {
			for range rangesInSeconds {
				steps = append(steps, queryTimeParameters.ResolutionInSeconds)
			}
		} else {
			steps = append(steps, []float64{20, 20, 20, 20, 20, 60, 60, 300}...)
		}
	}

	if queryTimeParameters.ResolutionInSeconds != 0 {
		steps = append(steps, queryTimeParameters.ResolutionInSeconds)
	} else {
		steps = append(steps, []float64{20, 20, 60, 300}...)
	}

	for _, endTime := range endTimes {
		for i, rangeInSecond := range rangesInSeconds {
			startTime := endTime.Add(-time.Duration(rangeInSecond * float64(time.Second)))
			step := time.Duration(steps[i] * float64(time.Second))
			ranges = append(ranges, v1.Range{Start: startTime, End: endTime, Step: step})
		}
	}
	return ranges
}

// XXX: Remove this hack once histogram_quantile push down is supported.
// Pin histogram quantile query to 15 minutes range
func adjustRangeParameter(rangeParameter v1.Range, query string) v1.Range {
	adjustedRange := v1.Range{End: rangeParameter.End, Start: rangeParameter.Start, Step: rangeParameter.Step}
	if strings.Contains(query, "histogram_quantile") && strings.Contains(query, "rate") {
		adjustedRange.Step = time.Duration(20 * float64(time.Second))
		adjustedRange.Start = adjustedRange.End.Add(-time.Duration(900 * float64(time.Second)))
	}
	return adjustedRange
}

// ExpandTestCases returns the fully expanded test cases for a given set of templates test cases.
func ExpandTestCases(cases []*config.TestCase, tweaks []*config.QueryTweak, queryTimeParameters config.QueryTimeParameters) []*comparer.TestCase {
	ranges := generateQueryTimeRangeParameters(queryTimeParameters)

	tcs := make([]*comparer.TestCase, 0)
	for _, q := range cases {
		vs := getVariants(q.Query, q.VariantArgs, make(map[string]string))
		for _, v := range vs {
			rangeParameter := adjustRangeParameter(ranges[rand.Intn(len(ranges))], v)
			tc := &comparer.TestCase{
				Query:          v,
				SkipComparison: q.SkipComparison,
				ShouldFail:     q.ShouldFail,
				Start:          rangeParameter.Start,
				End:            rangeParameter.End,
				Resolution:     rangeParameter.Step,
			}
			tcs = append(tcs, applyQueryTweaks(tc, tweaks))
		}
	}
	return tcs
}
