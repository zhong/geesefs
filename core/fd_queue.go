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
	"sync"

	"github.com/tidwall/btree"
)

type FDQueue struct {
	mu         sync.Mutex
	cond       *sync.Cond
	q          btree.Map[uint64, *Inode]
	maxCount   int
	curQueueID uint64

	// Total size in bytes currently occupied by the on-disk cache (--cache),
	// and the configured limit (0 = unlimited). sizeQ is a separate LRU of
	// all inodes that have data in the disk cache, because q only holds
	// inodes with an open FD, and FDs are closed independently of the size limit.
	maxBytes  int64
	curBytes  int64
	sizeQ     btree.Map[uint64, *Inode]
	curSizeID uint64
	// Set when the closer goroutine should run, so that wakeups sent while
	// it's busy aren't lost
	wakeup bool
}

func NewFDQueue(maxCount int) *FDQueue {
	q := &FDQueue{maxCount: maxCount}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (l *FDQueue) SetMaxBytes(maxBytes int64) {
	l.mu.Lock()
	l.maxBytes = maxBytes
	l.mu.Unlock()
}

// AddBytes records that `delta` more bytes (possibly negative) are now occupied
// by inode's on-disk cache file, and wakes up the eviction goroutine if the
// total size now exceeds the configured limit.
func (l *FDQueue) AddBytes(inode *Inode, delta int64) {
	l.mu.Lock()
	inode.DiskCacheBytes += delta
	l.curBytes += delta
	if delta > 0 || inode.DiskCacheBytes <= 0 {
		l.touchSizeLocked(inode)
	}
	if l.overSizeLocked() {
		l.wakeupLocked()
	}
	l.mu.Unlock()
}

// MoveBytesToOrphans removes inode from the size LRU, but keeps its bytes in
// the total, because its cache file is still on disk (see orphanCacheFile)
func (l *FDQueue) MoveBytesToOrphans(inode *Inode) {
	l.mu.Lock()
	inode.DiskCacheBytes = 0
	l.touchSizeLocked(inode)
	l.mu.Unlock()
}

// AddOrphanBytes changes the total by delta bytes not owned by any inode
func (l *FDQueue) AddOrphanBytes(delta int64) {
	l.mu.Lock()
	l.curBytes += delta
	if l.overSizeLocked() {
		l.wakeupLocked()
	}
	l.mu.Unlock()
}

func (l *FDQueue) wakeupLocked() {
	l.wakeup = true
	l.cond.Broadcast()
}

// Wakeup unconditionally wakes up the goroutine blocked in CloseExtra
func (l *FDQueue) Wakeup() {
	l.mu.Lock()
	l.wakeupLocked()
	l.mu.Unlock()
}

// OverSize reports whether the disk cache currently exceeds its size limit
func (l *FDQueue) OverSize() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.overSizeLocked()
}

// CurBytes returns the current accounted size of the disk cache
func (l *FDQueue) CurBytes() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.curBytes
}

func (l *FDQueue) overSizeLocked() bool {
	return l.maxBytes > 0 && l.curBytes > l.maxBytes
}

func (l *FDQueue) overCountLocked() bool {
	return l.maxCount > 0 && l.q.Len() > l.maxCount
}

// Move inode to the most recently used end of sizeQ, or remove it from sizeQ
// if it doesn't have anything in the disk cache anymore. l.mu must be held.
func (l *FDQueue) touchSizeLocked(inode *Inode) {
	if inode.DiskCacheQueueID > 0 {
		l.sizeQ.Delete(inode.DiskCacheQueueID)
		inode.DiskCacheQueueID = 0
	}
	if inode.DiskCacheBytes > 0 {
		l.curSizeID++
		inode.DiskCacheQueueID = l.curSizeID
		l.sizeQ.Set(inode.DiskCacheQueueID, inode)
	}
}

func (l *FDQueue) InsertFD(inode *Inode) {
	l.mu.Lock()
	l.curQueueID++
	inode.DiskFDQueueID = l.curQueueID
	l.q.Set(inode.DiskFDQueueID, inode)
	if inode.DiskCacheBytes > 0 {
		// Reopening the cache file to read it back is a use, too
		l.touchSizeLocked(inode)
	}
	if l.overCountLocked() || l.overSizeLocked() {
		// Wakeup the coroutine that closes old FDs / evicts old cache files
		// This way we don't guarantee that we never exceed MaxDiskCacheFD/MaxDiskCacheSize
		// But we assume it's not a problem if we leave some safety margin for the limit
		// The same thing happens with our memory usage anyway :D
		l.wakeupLocked()
	}
	l.mu.Unlock()
}

func (l *FDQueue) UseFD(inode *Inode) {
	l.mu.Lock()
	if inode.DiskFDQueueID > 0 {
		l.q.Delete(inode.DiskFDQueueID)
	}
	l.curQueueID++
	inode.DiskFDQueueID = l.curQueueID
	l.q.Set(inode.DiskFDQueueID, inode)
	if inode.DiskCacheBytes > 0 {
		l.touchSizeLocked(inode)
	}
	l.mu.Unlock()
}

func (l *FDQueue) DeleteFD(inode *Inode) {
	l.mu.Lock()
	l.q.Delete(inode.DiskFDQueueID)
	inode.DiskFDQueueID = 0
	l.mu.Unlock()
}

func (l *FDQueue) NextFD(minQueueID uint64) (inode *Inode, nextQueueId uint64) {
	return
}

// Close unneeded cache FDs and, if over the size limit, evict (delete) the
// on-disk cache files of the least recently used eligible inodes.
// evictCacheFile is called with inode.mu held and should return the number of
// bytes it actually freed (0 if the inode wasn't eligible for eviction, e.g.
// it's open, dirty, flushing or pinned).
func (l *FDQueue) CloseExtra(evictCacheFile func(inode *Inode) (freed int64)) {
	l.mu.Lock()
	for !l.wakeup {
		l.cond.Wait()
	}
	l.wakeup = false
	l.closeAndEvictExtraLocked(evictCacheFile)
	l.mu.Unlock()
}

// closeAndEvictExtraLocked does the actual work of CloseExtra. Split out so
// it can be driven directly (without going through cond.Wait()) in tests.
// l.mu must be held on entry, and is held again on return, but is released
// and re-acquired in between while per-inode work is done.
func (l *FDQueue) closeAndEvictExtraLocked(evictCacheFile func(inode *Inode) (freed int64)) {
	var nextQueueId uint64
	var inode *Inode
	for l.overCountLocked() {
		l.q.Ascend(nextQueueId, func(queueId uint64, i *Inode) bool {
			inode = i
			nextQueueId = queueId + 1
			return false
		})
		if inode == nil {
			break
		}
		l.mu.Unlock()
		inode.mu.Lock()
		if inode.DiskCacheFD != nil {
			inode.DiskCacheFD.Close()
			inode.DiskCacheFD = nil
			l.DeleteFD(inode)
			inode.mu.Unlock()
			l.mu.Lock()
		} else {
			inode.mu.Unlock()
			l.mu.Lock()
		}
	}
	// Evict least recently used on-disk cache files until we're back under the
	// size limit, or until we've gone through the whole LRU order without
	// freeing anything (nothing left is eligible for eviction right now).
	nextQueueId = 0
	freedAnyThisLap := false
	for l.overSizeLocked() {
		inode = nil
		l.sizeQ.Ascend(nextQueueId, func(queueId uint64, i *Inode) bool {
			inode = i
			nextQueueId = queueId + 1
			return false
		})
		if inode == nil {
			// Reached the end of the LRU order. Stop if this lap freed
			// nothing at all (everything left is open/dirty/pinned), to
			// avoid spinning forever - the goroutine will retry on the
			// next wakeup (Goofys.DiskCacheRetrier sends one periodically
			// while we're over the limit). Otherwise start another lap.
			if !freedAnyThisLap {
				break
			}
			freedAnyThisLap = false
			nextQueueId = 0
			continue
		}
		l.mu.Unlock()
		inode.mu.Lock()
		freed := evictCacheFile(inode)
		inode.mu.Unlock()
		l.mu.Lock()
		if freed > 0 {
			freedAnyThisLap = true
		}
	}
}
