// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

// Command benchgate turns "universally faster" from a claim into a check.
//
// It reads two `go test -bench` result files produced from the same benchmark
// sources -- one built against hashicorp/go-memdb (-tags upstream), one against
// go-maemmidb -- and fails unless, for EVERY benchmark:
//
//   - time/op is not slower: a slowdown counts only if it is statistically
//     significant (Mann-Whitney U, the test benchstat uses) AND larger than a
//     small tolerance that absorbs measurement noise on code paths that are
//     identical in both implementations;
//   - allocs/op is not higher;
//   - B/op (and the heap footprint metrics) are not higher, except for
//     benchmarks explicitly allowed to trade bytes, which must be justified in
//     docs/benchmarks.md.
//
// The same check guards against regressions relative to our own history: give
// it the results of an older revision as -old (see `make bench-self`).
//
// Usage:
//
//	go run ./cmd/benchgate -old results/upstream.txt -new results/new.txt
//	go run ./cmd/benchgate -old results/self-base.txt -new results/self-new.txt -old-name origin/main
package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/perf/benchfmt"
	"golang.org/x/perf/benchmath"
)

// lowerIsBetter lists the units the gate compares.
var gatedUnits = []string{"sec/op", "B/op", "allocs/op", "heapB/row", "heapobjs/row"}

type samples map[string]map[string][]float64 // benchmark -> unit -> values

func read(path string) (samples, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	out := samples{}
	r := benchfmt.NewReader(f, path)
	for r.Scan() {
		res, ok := r.Result().(*benchfmt.Result)
		if !ok {
			continue
		}
		name := string(res.Name.Full())
		if out[name] == nil {
			out[name] = map[string][]float64{}
		}
		for _, v := range res.Values {
			out[name][v.Unit] = append(out[name][v.Unit], v.Value)
		}
	}
	return out, r.Err()
}

// family is the benchmark's top-level name.
func family(name string) string {
	if i := strings.IndexByte(name, '/'); i >= 0 {
		name = name[:i]
	}
	if i := strings.LastIndexByte(name, '-'); i >= 0 && strings.Trim(name[i+1:], "0123456789") == "" {
		name = name[:i] // GOMAXPROCS suffix
	}
	return name
}

// printSummary prints, per benchmark family, how much faster the new
// implementation is (geometric mean, worst and best case) and how its
// allocations compare.
func printSummary(names []string, oldS, newS samples) {
	type agg struct {
		n                    int
		logSpeed             float64
		worst, best          float64
		oldAllocs, newAllocs float64
		oldBytes, newBytes   float64
	}
	fams := map[string]*agg{}
	var order []string
	for _, name := range names {
		o, n := oldS[name]["sec/op"], newS[name]["sec/op"]
		if len(o) == 0 || len(n) == 0 {
			continue
		}
		f := family(name)
		a := fams[f]
		if a == nil {
			a = &agg{worst: math.Inf(1), best: 0}
			fams[f] = a
			order = append(order, f)
		}
		speed := median(o) / median(n)
		a.n++
		a.logSpeed += math.Log(speed)
		a.worst = math.Min(a.worst, speed)
		a.best = math.Max(a.best, speed)
		a.oldAllocs += median(oldS[name]["allocs/op"])
		a.newAllocs += median(newS[name]["allocs/op"])
		a.oldBytes += median(oldS[name]["B/op"])
		a.newBytes += median(newS[name]["B/op"])
	}
	sort.Strings(order)
	pct := func(newV, oldV float64) string {
		if oldV == 0 {
			if newV == 0 {
				return "0 → 0"
			}
			return "n/a"
		}
		return fmt.Sprintf("%+.0f%%", (newV/oldV-1)*100)
	}
	fmt.Println("| Benchmark family | cases | speed-up (geomean) | worst case | best case | allocs/op | B/op |")
	fmt.Println("|---|---:|---:|---:|---:|---:|---:|")
	total, totalLog := 0, 0.0
	for _, f := range order {
		a := fams[f]
		fmt.Printf("| %s | %d | **%.2fx** | %.2fx | %.2fx | %s | %s |\n", strings.TrimPrefix(f, "Benchmark"), a.n,
			math.Exp(a.logSpeed/float64(a.n)), a.worst, a.best, pct(a.newAllocs, a.oldAllocs), pct(a.newBytes, a.oldBytes))
		total += a.n
		totalLog += a.logSpeed
	}
	fmt.Printf("| **all** | %d | **%.2fx** | | | | |\n", total, math.Exp(totalLog/float64(max(total, 1))))
}

func median(vs []float64) float64 {
	s := append([]float64(nil), vs...)
	sort.Float64s(s)
	n := len(s)
	if n == 0 {
		return math.NaN()
	}
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func main() {
	oldPath := flag.String("old", "", "results of the baseline (the upstream implementation, or an older revision)")
	oldName := flag.String("old-name", "upstream", "what to call the baseline in the verdict")
	newPath := flag.String("new", "", "results of the new implementation")
	tolerance := flag.Float64("tolerance", 2, "time/op slowdown, in percent, below which a difference is treated as noise")
	alpha := flag.Float64("alpha", 0.05, "significance level")
	allowBytes := flag.String("allow-bytes", "", "regexp of benchmarks allowed to use more B/op than upstream")
	allocSlack := flag.Float64("allocs-slack", 0, "allocs/op increase, in percent, below which a difference is treated as noise")
	verbose := flag.Bool("v", false, "list every benchmark, not just the failures")
	summary := flag.Bool("summary", false, "print a Markdown summary per benchmark family instead of gating")
	flag.Parse()
	if *oldPath == "" || *newPath == "" {
		flag.Usage()
		os.Exit(2)
	}
	var allow *regexp.Regexp
	if *allowBytes != "" {
		allow = regexp.MustCompile(*allowBytes)
	}

	oldS, err := read(*oldPath)
	if err == nil && len(oldS) == 0 {
		err = fmt.Errorf("%s: no benchmark results", *oldPath)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "benchgate:", err)
		os.Exit(2)
	}
	newS, err := read(*newPath)
	if err == nil && len(newS) == 0 {
		err = fmt.Errorf("%s: no benchmark results", *newPath)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "benchgate:", err)
		os.Exit(2)
	}

	var names []string
	for name := range oldS {
		if _, ok := newS[name]; ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	if *summary {
		printSummary(names, oldS, newS)
		return
	}

	var failures []string
	faster, same, logSum := 0, 0, 0.0
	thresholds := benchmath.DefaultThresholds
	for _, name := range names {
		for _, unit := range gatedUnits {
			o, n := oldS[name][unit], newS[name][unit]
			if len(o) == 0 || len(n) == 0 {
				continue
			}
			om, nm := median(o), median(n)

			switch unit {
			case "sec/op":
				cmp := benchmath.AssumeNothing.Compare(benchmath.NewSample(o, &thresholds), benchmath.NewSample(n, &thresholds))
				significant := cmp.P < *alpha
				delta := (nm/om - 1) * 100
				logSum += math.Log(om / nm)
				switch {
				case significant && delta > *tolerance:
					failures = append(failures, fmt.Sprintf("SLOWER  %-70s %12.4g -> %-12.4g sec/op  %+.1f%% (p=%.3f)", name, om, nm, delta, cmp.P))
				case significant && delta < 0:
					faster++
				default:
					same++
				}
				if *verbose {
					fmt.Printf("%-72s %12.4g -> %-12.4g sec/op  %+7.1f%% (p=%.3f)\n", name, om, nm, delta, cmp.P)
				}
			case "allocs/op":
				// Exact by default. A write transaction takes its scratch
				// state from a sync.Pool, which every collection empties, so
				// the average allocs/op of a write benchmark moves by a
				// fraction with the timing of the collector; comparing two
				// builds of nearly the same code needs a little slack for it.
				slack := om * *allocSlack / 100
				if *allocSlack > 0 {
					// (Half an allocation: what the median of integer samples
					// moves by when the samples straddle two counts.)
					slack += 0.5
				}
				if nm > om+slack {
					failures = append(failures, fmt.Sprintf("ALLOCS  %-70s %12.4g -> %-12.4g allocs/op", name, om, nm))
				}
			default:
				// Bytes are compared with a whisker of slack: size-class
				// rounding makes the last few bytes meaningless.
				if nm > om*1.01+1 && (unit != "B/op" || allow == nil || !allow.MatchString(name)) {
					failures = append(failures, fmt.Sprintf("BYTES   %-70s %12.4g -> %-12.4g %s", name, om, nm, unit))
				}
			}
		}
	}

	var onlyOld, onlyNew []string
	for name := range oldS {
		if _, ok := newS[name]; !ok {
			onlyOld = append(onlyOld, name)
		}
	}
	for name := range newS {
		if _, ok := oldS[name]; !ok {
			onlyNew = append(onlyNew, name)
		}
	}
	sort.Strings(onlyOld)
	sort.Strings(onlyNew)

	fmt.Printf("benchgate: %d benchmarks compared: %d faster, %d statistically equal, geomean speed-up %.2fx\n",
		len(names), faster, same, math.Exp(logSum/float64(max(len(names), 1))))
	if len(onlyOld) > 0 {
		failures = append(failures, "MISSING benchmarks present for "+*oldName+" only: "+strings.Join(onlyOld, ", "))
	}
	if len(onlyNew) > 0 {
		fmt.Printf("benchgate: %d benchmarks exist only for the new implementation (extensions), not gated\n", len(onlyNew))
	}
	if len(failures) > 0 {
		fmt.Printf("benchgate: FAIL -- %d violations of \"never slower than %s\":\n", len(failures), *oldName)
		for _, f := range failures {
			fmt.Println("  " + f)
		}
		os.Exit(1)
	}
	fmt.Printf("benchgate: PASS -- no benchmark is slower, allocates more often, or uses more memory than %s\n", *oldName)
}
