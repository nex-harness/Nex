// registryhelpers.go: the refresh-waiter plumbing of RegistryService
// (registry-service.ts waiter resolve/reject + account read).
package modelprovider

import (
	"context"
	"errors"
)

type registryRefreshWaiter struct {
	generation int
	resolve    func(*RegistryServiceSnapshot)
	reject     func(error)
}

func (s *RegistryService) readAccount(ctx context.Context) (*AccountSnapshot, error) {
	if s.account == nil {
		return nil, errors.New("no account source")
	}
	return s.account.Read(ctx)
}

func (s *RegistryService) completeWithError(generation int, err error, reasons []string) {
	s.mu.Lock()
	s.completedGen = generation
	listeners := append([]func(error, []string){}, s.errorListeners...)
	s.rejectWaiters(generation, err)
	s.mu.Unlock()
	for _, listener := range listeners {
		listener(err, reasons)
	}
}

func (s *RegistryService) resolveWaiters(generation int, snapshot *RegistryServiceSnapshot) {
	var remaining []registryRefreshWaiter
	for _, waiter := range s.refreshWait {
		if waiter.generation <= generation {
			waiter.resolve(snapshot)
		} else {
			remaining = append(remaining, waiter)
		}
	}
	s.refreshWait = remaining
}

func (s *RegistryService) rejectWaiters(generation int, err error) {
	var remaining []registryRefreshWaiter
	for _, waiter := range s.refreshWait {
		if waiter.generation <= generation {
			waiter.reject(err)
		} else {
			remaining = append(remaining, waiter)
		}
	}
	s.refreshWait = remaining
}

func joinStrings(values []string, sep string) string {
	out := ""
	for i, v := range values {
		if i > 0 {
			out += sep
		}
		out += v
	}
	return out
}
