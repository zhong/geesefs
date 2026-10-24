// Copyright 2021 Yandex LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package core

import (
	"time"

	. "gopkg.in/check.v1"
)

type FDQueueTest struct{}

var _ = Suite(&FDQueueTest{})

// evictAll is an evictCacheFile callback that drops everything not pinned
func evictAll(q *FDQueue, evicted *[]string) func(inode *Inode) int64 {
	return func(inode *Inode) int64 {
		if inode.Pinned {
			return 0
		}
		*evicted = append(*evicted, inode.Name)
		freed := inode.DiskCacheBytes
		q.AddBytes(inode, -freed)
		return freed
	}
}

func runExtra(q *FDQueue, evict func(inode *Inode) int64) {
	q.mu.Lock()
	q.closeAndEvictExtraLocked(evict)
	q.mu.Unlock()
}

func (s *FDQueueTest) TestEvictsLeastRecentlyUsedFirst(t *C) {
	q := NewFDQueue(0)
	q.SetMaxBytes(10)
	a, b, c := &Inode{Name: "a"}, &Inode{Name: "b"}, &Inode{Name: "c"}
	q.AddBytes(a, 4)
	q.AddBytes(b, 4)
	q.AddBytes(c, 4)
	// Touch a, so b becomes the least recently used one
	q.AddBytes(a, 1)
	t.Assert(q.curBytes, Equals, int64(13))

	var evicted []string
	runExtra(q, evictAll(q, &evicted))
	t.Assert(evicted, DeepEquals, []string{"b"})
	t.Assert(q.curBytes, Equals, int64(9))
	t.Assert(b.DiskCacheQueueID, Equals, uint64(0))
	t.Assert(q.sizeQ.Len(), Equals, 2)
}

func (s *FDQueueTest) TestUseFDTouchesSizeLRU(t *C) {
	q := NewFDQueue(0)
	q.SetMaxBytes(5)
	a, b := &Inode{Name: "a"}, &Inode{Name: "b"}
	q.InsertFD(a)
	q.AddBytes(a, 3)
	q.InsertFD(b)
	q.AddBytes(b, 3)
	q.UseFD(a)

	var evicted []string
	runExtra(q, evictAll(q, &evicted))
	t.Assert(evicted, DeepEquals, []string{"b"})
}

func (s *FDQueueTest) TestEvictsInodesWithClosedFD(t *C) {
	// Size eviction must not depend on the inode still having an open FD
	q := NewFDQueue(1)
	q.SetMaxBytes(5)
	a, b := &Inode{Name: "a"}, &Inode{Name: "b"}
	q.InsertFD(a)
	q.AddBytes(a, 3)
	q.DeleteFD(a)
	q.InsertFD(b)
	q.AddBytes(b, 3)

	var evicted []string
	runExtra(q, evictAll(q, &evicted))
	t.Assert(evicted, DeepEquals, []string{"a"})
	t.Assert(q.curBytes, Equals, int64(3))
}

func (s *FDQueueTest) TestStopsWhenNothingEligible(t *C) {
	q := NewFDQueue(0)
	q.SetMaxBytes(1)
	a := &Inode{Name: "a", Pinned: true}
	q.AddBytes(a, 100)

	calls := 0
	runExtra(q, func(inode *Inode) int64 {
		calls++
		return 0
	})
	// One full lap without progress, then give up instead of spinning
	t.Assert(calls, Equals, 1)
	t.Assert(q.curBytes, Equals, int64(100))
}

func (s *FDQueueTest) TestSkipsIneligibleThenEvictsEligible(t *C) {
	q := NewFDQueue(0)
	q.SetMaxBytes(1)
	pinned := &Inode{Name: "pinned", Pinned: true}
	evictable := &Inode{Name: "evictable"}
	q.AddBytes(pinned, 50)
	q.AddBytes(evictable, 50)

	var evicted []string
	runExtra(q, evictAll(q, &evicted))
	t.Assert(evicted, DeepEquals, []string{"evictable"})
	t.Assert(q.curBytes, Equals, int64(50))
}

func (s *FDQueueTest) TestUnlimited(t *C) {
	q := NewFDQueue(0)
	a := &Inode{Name: "a"}
	q.AddBytes(a, 1000000)
	t.Assert(q.wakeup, Equals, false)

	var evicted []string
	runExtra(q, evictAll(q, &evicted))
	t.Assert(evicted, IsNil)
}

func (s *FDQueueTest) TestWakeupIsNotLost(t *C) {
	// A wakeup sent before CloseExtra starts waiting must not be lost
	q := NewFDQueue(0)
	q.SetMaxBytes(1)
	a := &Inode{Name: "a"}
	q.AddBytes(a, 10)

	var evicted []string
	done := make(chan struct{})
	go func() {
		q.CloseExtra(evictAll(q, &evicted))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("CloseExtra didn't return after a pending wakeup")
	}
	t.Assert(evicted, DeepEquals, []string{"a"})
	t.Assert(q.wakeup, Equals, false)
}
