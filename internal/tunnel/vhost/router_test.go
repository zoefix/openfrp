package vhost

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func mustAdd(t *testing.T, r *Router, proxyName string, patterns ...string) {
	t.Helper()
	if err := r.Add(patterns, Route{RunID: "run-1", ProxyName: proxyName}); err != nil {
		t.Fatalf("Add(%v): %v", patterns, err)
	}
}

// TestWildcardMatchesExactlyOneLabel is the defining behaviour of this package
// and the point where it deliberately diverges from frp.
//
// frp rewrites successive leading labels to "*", so *.aaa.com there also
// matches x.bb.aaa.com. Here it must not: our rule mirrors DNS and TLS
// certificate scope, so a route can never resolve to a tunnel whose
// certificate does not cover the name.
func TestWildcardMatchesExactlyOneLabel(t *testing.T) {
	r := NewRouter()
	mustAdd(t, r, "one-level", "*.aaa.com")

	matches := []string{"www.aaa.com", "bb.aaa.com", "anything.aaa.com"}
	for _, host := range matches {
		if _, ok := r.Lookup(host); !ok {
			t.Errorf("%s should match *.aaa.com", host)
		}
	}

	// The whole point: a wildcard consumes one label and no more.
	rejects := []string{
		"x.bb.aaa.com",
		"a.b.c.aaa.com",
		"aaa.com",          // the bare domain is not a subdomain
		"aaa.com.evil.com", // suffix confusion
		"notaaa.com",
	}
	for _, host := range rejects {
		if route, ok := r.Lookup(host); ok {
			t.Errorf("%s must NOT match *.aaa.com, but resolved to %q", host, route.Pattern)
		}
	}
}

// TestMatchPriority pins the whole ordering with every pattern registered at
// once, which is the configuration most likely to expose an ordering bug.
func TestMatchPriority(t *testing.T) {
	r := NewRouter()

	mustAdd(t, r, "bare", "aaa.com")
	mustAdd(t, r, "exact-www", "www.aaa.com")
	mustAdd(t, r, "star-1", "*.aaa.com")
	mustAdd(t, r, "star-2", "*.bb.aaa.com")
	mustAdd(t, r, "star-3", "*.cc.bb.aaa.com")
	mustAdd(t, r, "exact-deep", "keep.cc.bb.aaa.com")
	mustAdd(t, r, "fallback", CatchAll)

	tests := []struct {
		host string
		want string
	}{
		{"aaa.com", "bare"},
		{"AAA.COM", "bare"},          // case-insensitive
		{"aaa.com.", "bare"},         // trailing dot
		{"aaa.com:8080", "bare"},     // port stripped
		{"www.aaa.com", "exact-www"}, // exact beats wildcard
		{"other.aaa.com", "star-1"},
		{"bb.aaa.com", "star-1"},              // one label deep
		{"x.bb.aaa.com", "star-2"},            // deeper wildcard wins
		{"www.bb.aaa.com", "star-2"},          // not exact-www: different depth
		{"x.cc.bb.aaa.com", "star-3"},         // deepest wildcard wins
		{"keep.cc.bb.aaa.com", "exact-deep"},  // exact beats the deepest wildcard
		{"y.x.cc.bb.aaa.com", "fallback"},     // too deep for any wildcard
		{"unrelated.example.org", "fallback"}, // nothing matches
	}

	for _, tc := range tests {
		t.Run(tc.host, func(t *testing.T) {
			route, ok := r.Lookup(tc.host)
			if !ok {
				t.Fatalf("no route for %s, want %s", tc.host, tc.want)
			}
			if route.ProxyName != tc.want {
				t.Errorf("%s resolved to %q (pattern %q), want %q",
					tc.host, route.ProxyName, route.Pattern, tc.want)
			}
		})
	}
}

// TestNoCatchAllMeansNoMatch confirms the fallback is opt-in rather than
// implicit, so an unrouted host is a clean miss the caller can 404.
func TestNoCatchAllMeansNoMatch(t *testing.T) {
	r := NewRouter()
	mustAdd(t, r, "only", "*.aaa.com")

	if route, ok := r.Lookup("unrelated.example.org"); ok {
		t.Errorf("unexpected match on %q with no catch-all registered", route.Pattern)
	}
}

func TestPatternValidation(t *testing.T) {
	tests := []struct {
		pattern string
		wantErr string
	}{
		{"aaa.com", ""},
		{"*.aaa.com", ""},
		{"*.bb.aaa.com", ""},
		{"a-b.aaa.com", ""},
		{"*", ""}, // the catch-all

		{"*.com", "too broad"},
		{"a.*.com", "leftmost label"},
		{"*x.aaa.com", "whole label"},
		{"x*.aaa.com", "whole label"},
		{"aaa..com", "empty label"},
		{"com", "at least two labels"},
		{"", "empty"},
		{"   ", "empty"},
	}

	for _, tc := range tests {
		t.Run(tc.pattern, func(t *testing.T) {
			_, err := ParsePattern(tc.pattern)

			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error mentioning %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// TestInternationalisedDomains checks a native-script config and a punycode
// request agree, since browsers always send the latter.
func TestInternationalisedDomains(t *testing.T) {
	r := NewRouter()

	if err := r.Add([]string{"*.例子.测试"}, Route{RunID: "run-1", ProxyName: "idn"}); err != nil {
		t.Fatalf("Add IDN pattern: %v", err)
	}

	route, ok := r.Lookup("www.xn--fsqu00a.xn--0zwm56d")
	if !ok {
		t.Fatal("punycode host did not match its native-script pattern")
	}
	if route.ProxyName != "idn" {
		t.Errorf("resolved to %q, want idn", route.ProxyName)
	}

	// The native-script form must resolve identically.
	if _, ok := r.Lookup("www.例子.测试"); !ok {
		t.Error("native-script host did not match")
	}
}

func TestDomainCollisionIsRejected(t *testing.T) {
	r := NewRouter()
	mustAdd(t, r, "first", "aaa.com", "*.aaa.com")

	err := r.Add([]string{"bbb.com", "*.aaa.com"}, Route{RunID: "run-2", ProxyName: "second"})
	if err == nil {
		t.Fatal("expected a collision error for *.aaa.com")
	}
	if !strings.Contains(err.Error(), "already routed") {
		t.Errorf("error = %q, want it to mention the existing route", err)
	}

	// The rejected publish must not have claimed bbb.com either — a failed
	// registration has to be all-or-nothing.
	if route, ok := r.Lookup("bbb.com"); ok {
		t.Errorf("bbb.com was claimed by a failed publish (route %q)", route.Pattern)
	}
	if r.Len() != 2 {
		t.Errorf("router holds %d routes, want the original 2", r.Len())
	}
}

func TestRemoveWithdrawsRoutes(t *testing.T) {
	r := NewRouter()
	mustAdd(t, r, "web", "aaa.com", "*.aaa.com")
	mustAdd(t, r, "api", "api.bbb.com")

	r.Remove("run-1", "web")

	if _, ok := r.Lookup("aaa.com"); ok {
		t.Error("aaa.com is still routed after Remove")
	}
	if _, ok := r.Lookup("x.aaa.com"); ok {
		t.Error("x.aaa.com is still routed after Remove")
	}
	if _, ok := r.Lookup("api.bbb.com"); !ok {
		t.Error("Remove took out an unrelated tunnel's route")
	}

	// The freed pattern must be claimable again.
	if err := r.Add([]string{"aaa.com"}, Route{RunID: "run-9", ProxyName: "new"}); err != nil {
		t.Errorf("re-adding a withdrawn pattern failed: %v", err)
	}
}

func TestRemoveClientWithdrawsEverythingItOwned(t *testing.T) {
	r := NewRouter()

	if err := r.Add([]string{"a.com", "*.a.com"}, Route{RunID: "run-1", ProxyName: "p1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := r.Add([]string{"b.com"}, Route{RunID: "run-1", ProxyName: "p2"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := r.Add([]string{"c.com"}, Route{RunID: "run-2", ProxyName: "p3"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	r.RemoveClient("run-1")

	for _, host := range []string{"a.com", "x.a.com", "b.com"} {
		if _, ok := r.Lookup(host); ok {
			t.Errorf("%s survived RemoveClient", host)
		}
	}
	if _, ok := r.Lookup("c.com"); !ok {
		t.Error("RemoveClient removed another client's route")
	}
}

// TestConcurrentLookupsDuringRebuild is the reason the table is swapped
// atomically rather than mutated. Run with -race.
func TestConcurrentLookupsDuringRebuild(t *testing.T) {
	r := NewRouter()
	mustAdd(t, r, "stable", "stable.com", "*.stable.com")

	var wg sync.WaitGroup
	stop := make(chan struct{})

	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				// A route that is never touched must always resolve, even
				// while the table is being rebuilt around it.
				if _, ok := r.Lookup("x.stable.com"); !ok {
					t.Error("a stable route vanished during a rebuild")
					return
				}
			}
		}()
	}

	for i := range 200 {
		proxyName := fmt.Sprintf("churn-%d", i)
		pattern := fmt.Sprintf("host%d.churn.com", i)
		if err := r.Add([]string{pattern}, Route{RunID: "run-churn", ProxyName: proxyName}); err != nil {
			t.Errorf("Add: %v", err)
			break
		}
		r.Remove("run-churn", proxyName)
	}

	close(stop)
	wg.Wait()
}

func TestRoutesSnapshotIsSorted(t *testing.T) {
	r := NewRouter()
	mustAdd(t, r, "p", "z.com", "a.com", "*.m.com")

	routes := r.Routes()
	if len(routes) != 3 {
		t.Fatalf("got %d routes, want 3", len(routes))
	}
	for i := 1; i < len(routes); i++ {
		if routes[i-1].Pattern > routes[i].Pattern {
			t.Errorf("routes are not sorted: %q before %q",
				routes[i-1].Pattern, routes[i].Pattern)
		}
	}
}

func BenchmarkLookup(b *testing.B) {
	r := NewRouter()
	for i := range 1000 {
		r.Add([]string{fmt.Sprintf("host%d.example.com", i)},
			Route{RunID: "run", ProxyName: fmt.Sprintf("p%d", i)})
	}
	r.Add([]string{"*.wild.example.com"}, Route{RunID: "run", ProxyName: "wild"})

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			r.Lookup("host500.example.com")
			r.Lookup("anything.wild.example.com")
		}
	})
}
