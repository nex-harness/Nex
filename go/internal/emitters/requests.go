package emitters

// completedModelRequestsSlice returns the queue stored under key as a typed
// slice (empty when absent).
func (t *Telemetry) completedModelRequestsSlice(key string) []completedRequest {
	v, ok := t.completedModelRequests.get(key)
	if !ok {
		return nil
	}
	return append([]completedRequest(nil), v.([]completedRequest)...)
}

// dequeueCompletedRequest shifts the head of the queue for key, mirroring the
// TS shift() plus delete-when-empty. Returns nil when the queue is empty.
func (t *Telemetry) dequeueCompletedRequest(key string) *completedRequest {
	v, ok := t.completedModelRequests.get(key)
	if !ok {
		return nil
	}
	queue := v.([]completedRequest)
	if len(queue) == 0 {
		t.completedModelRequests.delete(key)
		return nil
	}
	head := queue[0]
	rest := queue[1:]
	if len(rest) > 0 {
		t.completedModelRequests.set(key, rest)
	} else {
		t.completedModelRequests.delete(key)
	}
	return &head
}
