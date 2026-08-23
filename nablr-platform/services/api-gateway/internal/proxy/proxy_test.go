package proxy

import (
	"sync"
	"testing"
)

func TestLoadBalancerNextConcurrentAccess(t *testing.T) {
	lb := &LoadBalancer{
		proxies: []*ServiceProxy{{}, {}, {}},
	}

	const calls = 1000
	var wg sync.WaitGroup
	wg.Add(calls)

	for i := 0; i < calls; i++ {
		go func() {
			defer wg.Done()
			if lb.Next() == nil {
				t.Error("expected non-nil service proxy")
			}
		}()
	}

	wg.Wait()

	want := calls % len(lb.proxies)
	if lb.current != want {
		t.Fatalf("current=%d want=%d", lb.current, want)
	}
}
