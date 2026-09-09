// SPDX-License-Identifier: Apache-2.0

package dolt

// UseTransactionTimestampForTest replaces only the in-transaction clock for
// deterministic direct-Dolt boundary tests. Production callers cannot access
// the unexported clock field and always use Dolt server time.
func UseTransactionTimestampForTest(store *DoltTaskStore, nowMillis *int64) func() {
	prior := store.testNowMillis
	store.testNowMillis = func() int64 { return *nowMillis }
	return func() { store.testNowMillis = prior }
}
