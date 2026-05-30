package store

import (
	"hash/fnv"
	"sync"
)

const defaultStripedStoreLockCount = 64

type stripedStoreLocks struct {
	locks []sync.Mutex
}

func newStripedStoreLocks(n int) stripedStoreLocks {
	if n <= 0 {
		n = defaultStripedStoreLockCount
	}
	return stripedStoreLocks{locks: make([]sync.Mutex, n)}
}

func (s *stripedStoreLocks) Lock(key string) func() {
	if s == nil || len(s.locks) == 0 {
		return func() {}
	}
	index := stripedStoreLockIndex(key, len(s.locks))
	lock := &s.locks[index]
	lock.Lock()
	return lock.Unlock
}

func stripedStoreLockIndex(key string, size int) int {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(key))
	return int(hash.Sum32() % uint32(size))
}
