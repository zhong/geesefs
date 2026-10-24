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
	"os"
	"path/filepath"
	"time"

	"github.com/yandex-cloud/geesefs/core/cfg"
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

func (s *FDQueueTest) TestInsertFDTouchesSizeLRU(t *C) {
	// Reopening a cache file whose FD was closed (to read it back) is a use
	q := NewFDQueue(0)
	q.SetMaxBytes(5)
	a, b := &Inode{Name: "a"}, &Inode{Name: "b"}
	q.InsertFD(a)
	q.AddBytes(a, 3)
	q.DeleteFD(a)
	q.InsertFD(b)
	q.AddBytes(b, 3)
	q.InsertFD(a)

	var evicted []string
	runExtra(q, evictAll(q, &evicted))
	t.Assert(evicted, DeepEquals, []string{"b"})
}

func (s *FDQueueTest) TestRetryAfterBecomingEligible(t *C) {
	// Nothing is evictable at first, then it becomes evictable without any
	// new cache write. OverSize must still report it, so the retrier wakes
	// up the closer again.
	q := NewFDQueue(0)
	q.SetMaxBytes(1)
	a := &Inode{Name: "a", Pinned: true}
	q.AddBytes(a, 10)

	var evicted []string
	runExtra(q, evictAll(q, &evicted))
	t.Assert(evicted, IsNil)
	t.Assert(q.OverSize(), Equals, true)

	a.Pinned = false
	runExtra(q, evictAll(q, &evicted))
	t.Assert(evicted, DeepEquals, []string{"a"})
	t.Assert(q.OverSize(), Equals, false)
	t.Assert(q.CurBytes(), Equals, int64(0))
}

func (s *FDQueueTest) TestClearDiskCache(t *C) {
	dir := t.MkDir()
	t.Assert(os.MkdirAll(filepath.Join(dir, "a", "b"), 0755), IsNil)
	t.Assert(os.WriteFile(filepath.Join(dir, "a", "b", "f"), []byte("x"), 0644), IsNil)
	t.Assert(os.WriteFile(filepath.Join(dir, "g"), []byte("y"), 0644), IsNil)

	t.Assert(clearDiskCache(dir), IsNil)
	entries, err := os.ReadDir(dir)
	t.Assert(err, IsNil)
	t.Assert(entries, HasLen, 0)
	// The cache directory itself is kept
	_, err = os.Stat(dir)
	t.Assert(err, IsNil)
	// A missing cache directory is fine
	t.Assert(clearDiskCache(filepath.Join(dir, "missing")), IsNil)
}

func (s *FDQueueTest) TestOpenCacheFDTruncatesStaleFile(t *C) {
	dir := t.MkDir()
	fs := &Goofys{
		flags:       &cfg.FlagStorage{CachePath: dir, CacheFileMode: 0644},
		diskFdQueue: NewFDQueue(0),
	}
	// Left by a previous run, not accounted anywhere
	stale := filepath.Join(dir, "f")
	t.Assert(os.WriteFile(stale, make([]byte, 1000), 0644), IsNil)

	inode := NewInode(fs, nil, "f")
	t.Assert(inode.OpenCacheFD(), IsNil)
	st, err := os.Stat(stale)
	t.Assert(err, IsNil)
	t.Assert(st.Size(), Equals, int64(0))
	t.Assert(inode.OnDisk, Equals, true)

	// Once something of this file is cached, reopening must keep it
	_, err = inode.DiskCacheFD.WriteAt([]byte("data"), 0)
	t.Assert(err, IsNil)
	inode.DiskCacheFD.Close()
	inode.DiskCacheFD = nil
	fs.diskFdQueue.DeleteFD(inode)
	t.Assert(inode.OpenCacheFD(), IsNil)
	st, err = os.Stat(stale)
	t.Assert(err, IsNil)
	t.Assert(st.Size(), Equals, int64(4))
	inode.DiskCacheFD.Close()
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
