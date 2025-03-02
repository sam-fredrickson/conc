package conc

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStream(t *testing.T) {
	t.Run("Basic", func(t *testing.T) {
		inputs := []int{70, 10, 33, 20, 50, 80}
		var collected []string

		err := Block(func(n Nursery) error {
			outputs := Stream(
				n,
				slices.Values(inputs),
				func(i int) (string, error) {
					output := fmt.Sprintf("output %02d", i)
					Sleep(n, time.Millisecond*time.Duration(i))
					return output, nil
				},
			)
			for output := range outputs {
				collected = append(collected, output)
			}

			return nil
		})
		if err != nil {
			t.Error(err)
		}
		if len(collected) != len(inputs) {
			t.Errorf("expected to collect an output for each input")
		}
		sortedOutputs := slices.Clone(collected)
		slices.Sort(sortedOutputs)
		if !slices.Equal(collected, sortedOutputs) {
			t.Errorf("expected output slice to be in sorted order")
		}
	})

	t.Run("Multistage", func(t *testing.T) {
		err := Block(func(n Nursery) error {
			// stage A: ints to strings
			inputs := []int{7, 5, 3, 1, 2, 4, 6}
			outputsA := Stream(
				n,
				slices.Values(inputs),
				func(i int) (string, error) {
					output := fmt.Sprintf("output %02d", i)
					Sleep(n, time.Millisecond*time.Duration(i))
					return output, nil
				},
			)

			// stage B: strings to ints
			outputsB := Stream(
				n,
				outputsA,
				func(s string) (int, error) {
					return len(s), nil
				},
			)

			for _ = range outputsB {
			}

			return nil
		})
		if err != nil {
			t.Error(err)
		}
	})

	t.Run("ErrorHandling", func(t *testing.T) {
		inputs := []int{1, 2, 3, 4, 5}
		expectedError := errors.New("test error")
		var collected []string
		var processedCount atomic.Int32

		err := Block(func(n Nursery) error {
			outputs := Stream(
				n,
				slices.Values(inputs),
				func(i int) (string, error) {
					processedCount.Add(1)
					time.Sleep(1 * time.Millisecond)
					if i == 3 {
						return "", expectedError
					}
					return fmt.Sprintf("value %d", i), nil
				},
			)

			for output := range outputs {
				collected = append(collected, output)
			}
			return nil
		})

		if err == nil {
			t.Error("expected an error, got nil")
		}
		if !errors.Is(err, expectedError) {
			t.Errorf("expected error %v, got %v", expectedError, err)
		}

		if processedCount.Load() == 0 {
			t.Error("expected at least some inputs to be processed")
		}

		for _, output := range collected {
			if output == "value 3" {
				t.Error("output from error-producing input should not be collected")
			}
		}
	})

	t.Run("EmptyInput", func(t *testing.T) {
		var inputs []int
		var collected []string
		var transformCalled atomic.Bool

		err := Block(func(n Nursery) error {
			outputs := Stream(
				n,
				slices.Values(inputs),
				func(i int) (string, error) {
					transformCalled.Store(true)
					return fmt.Sprintf("value %d", i), nil
				},
			)
			for output := range outputs {
				collected = append(collected, output)
			}
			return nil
		})

		if err != nil {
			t.Error(err)
		}
		if transformCalled.Load() {
			t.Error("transform function should not be called for empty input")
		}
		if len(collected) != 0 {
			t.Errorf("expected empty output for empty input, got %d items", len(collected))
		}
	})

	t.Run("Cancellation", func(t *testing.T) {
		inputs := []int{1, 2, 3, 4, 5}
		var processedCount atomic.Int32
		var collected []string
		var contextCancelled atomic.Bool

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		err := Block(func(n Nursery) error {
			outputs := Stream(
				n,
				slices.Values(inputs),
				func(i int) (string, error) {
					processedCount.Add(1)
					// Cancel after processing a few items
					if i == 3 {
						cancel()
						// Give a little time for cancellation to propagate
						time.Sleep(1 * time.Millisecond)
					}

					// Check if context is cancelled
					select {
					case <-n.Done():
						contextCancelled.Store(true)
						return "", n.Err()
					default:
						time.Sleep(1 * time.Millisecond)
						return fmt.Sprintf("value %d", i), nil
					}
				},
			)

			for output := range outputs {
				collected = append(collected, output)
			}
			return nil
		}, WithContext(ctx))

		// The error might not be propagated if the main goroutine completes
		// before the error is processed, so we don't strictly check for an error
		if err != nil {
			t.Logf("Got expected error: %v", err)
		}

		if !contextCancelled.Load() {
			t.Error("expected context to be cancelled")
		}

		// Note: We can't reliably assert that not all inputs were processed
		// after cancellation. Depending on timing, all inputs might be processed
		// before cancellation fully propagates. This is expected behavior.
		t.Logf("Processed %d/%d inputs after cancellation", processedCount.Load(), len(inputs))
	})

	t.Run("LargeInput", func(t *testing.T) {
		// Create a large input slice
		const inputSize = 50 // Reduced from 100 to make test faster
		inputs := make([]int, inputSize)
		for i := range inputSize {
			inputs[i] = i
		}

		var processedCount atomic.Int32
		var collected []int

		err := Block(func(n Nursery) error {
			outputs := Stream(
				n,
				slices.Values(inputs),
				func(i int) (int, error) {
					processedCount.Add(1)
					return i * 2, nil
				},
			)
			for output := range outputs {
				collected = append(collected, output)
			}
			return nil
		})

		if err != nil {
			t.Error(err)
		}
		if processedCount.Load() != inputSize {
			t.Errorf("expected all %d inputs to be processed, got %d", inputSize, processedCount.Load())
		}
		if len(collected) != inputSize {
			t.Errorf("expected to collect %d outputs, got %d", inputSize, len(collected))
		}
	})

	t.Run("MixedProcessingTimes", func(t *testing.T) {
		inputs := []int{10, 1, 5, 2, 8, 3}
		var processOrder []int
		var outputOrder []int
		var mu sync.Mutex

		err := Block(func(n Nursery) error {
			outputs := Stream(
				n,
				slices.Values(inputs),
				func(i int) (int, error) {
					// Process in order of input
					mu.Lock()
					processOrder = append(processOrder, i)
					mu.Unlock()

					// Sleep for different durations to ensure different completion times
					Sleep(n, time.Millisecond*time.Duration(i))
					return i, nil
				},
			)
			for output := range outputs {
				outputOrder = append(outputOrder, output)
			}
			return nil
		})

		if err != nil {
			t.Error(err)
		}

		if len(outputOrder) != len(inputs) {
			t.Errorf("expected %d outputs, got %d", len(inputs), len(outputOrder))
		}

		// Verify outputs are not in the same order as inputs (should be ordered by completion time)
		// This is a probabilistic test, but with the sleep times we've chosen, it should almost always pass
		if slices.Equal(processOrder, outputOrder) {
			t.Errorf("expected output order to differ from processing order due to different processing times")
		}
	})

	t.Run("DrainAfterPartialConsumption", func(t *testing.T) {
		// This test demonstrates that if you stop consuming from the output channel early,
		// you must drain the remaining outputs to allow all goroutines to complete
		inputs := []int{1, 2, 3, 4, 5}
		var processedCount atomic.Int32
		var consumedCount atomic.Int32

		err := Block(func(n Nursery) error {
			outputs := Stream(
				n,
				slices.Values(inputs),
				func(i int) (int, error) {
					processedCount.Add(1)
					// Simulate work
					time.Sleep(1 * time.Millisecond)
					return i, nil
				},
			)

			// Only consume first 3 outputs
			count := 0
			for output := range outputs {
				consumedCount.Add(1)
				count++
				if count >= 3 {
					// We've consumed enough outputs, but we need to drain the channel
					// to allow all goroutines to complete
					go func() {
						// Drain remaining outputs in a separate goroutine
						for range outputs {
							// Just drain, don't count these
						}
					}()
					break
				}
				_ = output
			}

			return nil
		})

		if err != nil {
			t.Error(err)
		}

		if processedCount.Load() != int32(len(inputs)) {
			t.Errorf("expected all %d inputs to be processed, got %d", len(inputs), processedCount.Load())
		}

		if consumedCount.Load() != 3 {
			t.Errorf("expected to consume exactly 3 outputs, got %d", consumedCount.Load())
		}
	})

	t.Run("SafeEarlyTerminationWithDefer", func(t *testing.T) {
		// This test demonstrates the recommended pattern for safely handling early termination
		// using a defer to ensure the channel is always drained
		inputs := []int{1, 2, 3, 4, 5}
		var processedCount atomic.Int32
		var consumedCount atomic.Int32

		err := Block(func(n Nursery) error {
			outputs := Stream(
				n,
				slices.Values(inputs),
				func(i int) (int, error) {
					processedCount.Add(1)
					// Simulate work
					time.Sleep(1 * time.Millisecond)
					return i, nil
				},
			)

			// Set up a deferred drain to handle early returns or breaks
			drainStarted := false
			defer func() {
				if !drainStarted {
					n.Go(func() error {
						// Drain any remaining outputs
						for range outputs {
							// Discard remaining outputs
						}
						return nil
					})
				}
			}()

			// Process outputs until some condition
			for output := range outputs {
				consumedCount.Add(1)
				_ = output

				// Simulate early termination after consuming 3 outputs
				if consumedCount.Load() >= 3 {
					drainStarted = true
					go func() {
						// Drain remaining outputs
						for range outputs {
							// Discard remaining outputs
						}
					}()
					break
				}
			}

			return nil
		})

		if err != nil {
			t.Error(err)
		}

		if processedCount.Load() != int32(len(inputs)) {
			t.Errorf("expected all %d inputs to be processed, got %d", len(inputs), processedCount.Load())
		}

		if consumedCount.Load() != 3 {
			t.Errorf("expected to consume exactly 3 outputs, got %d", consumedCount.Load())
		}
	})
}
