// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"reflect"
	"sync"
	"testing"
)

func TestTransientResultQueueBoundsOrdersAndIsolatesStores(t *testing.T) {
	if transientPoolResults.limit != 32 || transientStorageDiscoveries.limit != 128 || transientStorageClusters.limit != 128 {
		t.Fatalf("queue limits = %d, %d, %d", transientPoolResults.limit, transientStorageDiscoveries.limit, transientStorageClusters.limit)
	}
	q := newTransientResultQueue[int](2)
	first, second := &Store{}, &Store{}
	q.append(first, []int{1, 2, 3})
	q.append(second, []int{4})
	if got := q.drain(first); !reflect.DeepEqual(got, []int{1, 2}) {
		t.Fatalf("first drain = %v", got)
	}
	if got := q.drain(second); !reflect.DeepEqual(got, []int{4}) {
		t.Fatalf("second drain = %v", got)
	}
	if got := q.drain(first); len(got) != 0 {
		t.Fatalf("drain retained results: %v", got)
	}
}

func TestTransientResultQueueConcurrentAppend(t *testing.T) {
	q := newTransientResultQueue[int](128)
	s := &Store{}
	var wg sync.WaitGroup
	for i := 0; i < 128; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			q.append(s, []int{i})
		}(i)
	}
	wg.Wait()
	if got := q.drain(s); len(got) != 128 {
		t.Fatalf("results = %d, want 128", len(got))
	}
}
