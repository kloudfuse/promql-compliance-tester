package output

import (
	"encoding/csv"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/promlabs/promql-compliance-tester/comparer"
	"github.com/promlabs/promql-compliance-tester/config"
)

// Text produces text-based output for a number of query results.
func Text(results []*comparer.Result, includePassing bool, tweaks []*config.QueryTweak) {
	successes := 0
	unsupported := 0
	var impl1Duration, impl2Duration time.Duration
	type Comparison struct {
		query   string
		isAggr  bool
		outcome string
		start   time.Time
		end     time.Time
		stepMs  int
		time1   float64
		time2   float64
	}
	resultsFile, _ := os.Create(fmt.Sprintf("comparison-%s.csv", time.Now()))
	defer resultsFile.Close()
	w := csv.NewWriter(resultsFile)
	defer w.Flush()
	w.Write([]string{"query", "isAggr", "outcome", "start", "end", "stepMs", "time1", "time2"})
	for _, res := range results {
		var status string
		impl1Duration += res.Durations[0]
		impl2Duration += res.Durations[1]
		if res.Success() {
			successes++
			if !includePassing {
				continue
			}
		}
		if res.Unsupported {
			unsupported++
		}

		fmt.Println(strings.Repeat("-", 80))
		fmt.Printf("QUERY: %v\n", res.TestCase.Query)
		fmt.Printf("START: %v, STOP: %v, STEP: %v\n", res.TestCase.Start, res.TestCase.End, res.TestCase.Resolution)
		fmt.Printf("RESULT: ")
		if res.Success() {
			fmt.Println("PASSED")
			status = "passed"
		} else if res.Unsupported {
			fmt.Println("UNSUPPORTED: ")
			fmt.Printf("Query is unsupported: %v\n", res.UnexpectedFailure)
			status = "unsupported"
		} else {
			fmt.Printf("FAILED: ")
			if res.UnexpectedFailure != "" {
				fmt.Printf("Query failed unexpectedly: %v\n", res.UnexpectedFailure)
			}
			if res.UnexpectedSuccess {
				fmt.Println("Query succeeded, but should have failed.")
			}
			if res.Diff != "" {
				fmt.Println("Query returned different results:")
				fmt.Println(res.Diff)
			}
			status = "failed"
		}
		comparison := []string{
			res.TestCase.Query,
			fmt.Sprintf("%v", containsAggrFn(res.TestCase.Query)),
			status,
			fmt.Sprintf("%v", res.Range.Start.UnixNano()/1000000),
			fmt.Sprintf("%v", res.Range.End.UnixNano()/1000000),
			fmt.Sprintf("%v", res.Range.Step.Milliseconds()),
			fmt.Sprintf("%v", res.Durations[0].Milliseconds()),
			fmt.Sprintf("%v", res.Durations[1].Milliseconds()),
		}
		_ = w.Write(comparison)
	}

	fmt.Println(strings.Repeat("=", 80))
	fmt.Println("General query tweaks:")
	if len(tweaks) == 0 {
		fmt.Println("None.")
	}
	for _, t := range tweaks {
		fmt.Println("* ", t.Note)
	}
	fmt.Println(strings.Repeat("=", 80))
	fmt.Printf("Total: %d / %d (%.2f%%) passed, %d unsupported\n", successes, len(results), 100*float64(successes)/float64(len(results)), unsupported)

	fmt.Printf("Duration prom: %v, ref: %v\n", impl1Duration, impl2Duration)
}

func containsAggrFn(query string) bool {
	if strings.Contains(query, "min") || strings.Contains(query, "max") ||
		strings.Contains(query, "avg") || strings.Contains(query, "rate") || strings.Contains(query, "sum") {
		return true
	}
	return false
}
