package engine

import "sync"

type keyedLock struct {
	mu   sync.Mutex
	refs int
}

// KeyedMutex serializes work by Telegram user ID.
type KeyedMutex struct {
	mu    sync.Mutex
	locks map[int64]*keyedLock
}

// NewKeyedMutex returns an empty process-wide keyed mutex.
func NewKeyedMutex() *KeyedMutex {
	return &KeyedMutex{locks: make(map[int64]*keyedLock)}
}

// Lock locks one key and returns an unlock function.
func (m *KeyedMutex) Lock(key int64) func() {
	m.mu.Lock()

	lock := m.locks[key]
	if lock == nil {
		lock = &keyedLock{}
		m.locks[key] = lock
	}

	lock.refs++
	m.mu.Unlock()

	lock.mu.Lock()

	return func() {
		lock.mu.Unlock()

		m.mu.Lock()
		defer m.mu.Unlock()

		lock.refs--
		if lock.refs == 0 {
			delete(m.locks, key)
		}
	}
}
